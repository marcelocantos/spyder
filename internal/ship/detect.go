// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package ship

import (
	"encoding/json"
	"encoding/pem"
	"fmt"
	"regexp"
	"strings"
)

// Clipboard absorb recognisers — lifted from storectl (🎯T133.3).
// Surrounding chat text is ignored; IDs keep their labels (or are a
// bare paste of exactly one value).

const linePrefix = `^[ \t>*_\-•]*`

const uuidPat = `[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`

const keyIDPat = `[A-Z0-9]{10}`

var (
	reIssuerLabelled = regexp.MustCompile(`(?mi)` + linePrefix + `Issuer[ \t_]*ID[ \t]*[:=][ \t*_]*(` + uuidPat + `)`)
	reKeyIDLabelled  = regexp.MustCompile(`(?m)` + linePrefix + `(?i:Key[ \t_]*ID)[ \t]*[:=][ \t*_]*(` + keyIDPat + `)\b`)
	reOnlyUUID       = regexp.MustCompile(`^` + uuidPat + `$`)
	reOnlyKeyID      = regexp.MustCompile(`^` + keyIDPat + `$`)
	rePEM            = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)
)

// Detected is what Absorb recognised in one paste (partial OK).
type Detected struct {
	ASCIssuerID string
	ASCKeyID    string
	ASCP8       string
	PlaySAJSON  json.RawMessage
	PlaySAEmail string
	// FirebaseAdminJSON is set when the paste looks like a Firebase
	// Admin SDK key (service_account whose client_email contains
	// "firebase" or project_id is confirmed later by the human).
	FirebaseAdminJSON json.RawMessage
	MatchPassword     string
}

// Empty reports whether nothing was recognised.
func (d Detected) Empty() bool {
	return d.ASCIssuerID == "" && d.ASCKeyID == "" && d.ASCP8 == "" &&
		len(d.PlaySAJSON) == 0 && len(d.FirebaseAdminJSON) == 0 &&
		d.MatchPassword == ""
}

// Detect parses a clipboard paste. Chat wrappers around labelled IDs
// and PEMs are ignored. Refuses to guess a bare 10-char Key ID from
// prose (must be labelled or the entire paste).
func Detect(s string) (Detected, error) {
	var d Detected
	rest := s

	if obj, kind, ok := findServiceAccount(s); ok {
		var sa struct {
			ClientEmail string `json:"client_email"`
			ProjectID   string `json:"project_id"`
		}
		_ = json.Unmarshal([]byte(obj), &sa)
		raw := json.RawMessage(obj)
		if kind == "firebase" {
			d.FirebaseAdminJSON = raw
		} else {
			d.PlaySAJSON = raw
			d.PlaySAEmail = sa.ClientEmail
		}
		rest = strings.Replace(rest, obj, "", 1)
	}

	if block := rePEM.FindString(rest); block != "" {
		d.ASCP8 = repairPEM(block)
		rest = strings.Replace(rest, block, "", 1)
	}
	if m := reIssuerLabelled.FindStringSubmatch(rest); m != nil {
		d.ASCIssuerID = m[1]
	}
	if m := reKeyIDLabelled.FindStringSubmatch(rest); m != nil {
		d.ASCKeyID = m[1]
	}

	if d.Empty() {
		switch bare := strings.TrimSpace(s); {
		case reOnlyUUID.MatchString(bare):
			d.ASCIssuerID = bare
		case reOnlyKeyID.MatchString(bare):
			d.ASCKeyID = bare
		case strings.HasPrefix(bare, "match:") || strings.HasPrefix(bare, "MATCH_PASSWORD="):
			d.MatchPassword = strings.TrimPrefix(strings.TrimPrefix(bare, "match:"), "MATCH_PASSWORD=")
			d.MatchPassword = strings.TrimSpace(d.MatchPassword)
		}
	}

	if d.Empty() {
		return d, fmt.Errorf("nothing recognisable in the paste\n" +
			"  expected any of:\n" +
			"    IssuerID: 69a6de79-9175-47e3-e053-5b8c7c11a4d1\n" +
			"    KeyID: ABC123DEF4\n" +
			"    -----BEGIN PRIVATE KEY----- … -----END PRIVATE KEY-----\n" +
			"    { \"type\": \"service_account\", … }\n" +
			"    match:<passphrase>   (or MATCH_PASSWORD=… as the whole paste)\n" +
			"  surrounding text is ignored; IDs must keep their labels\n" +
			"  (or be pasted alone)")
	}
	return d, nil
}

// MergeDetected applies Detected fields onto an envelope (partial merge).
func (e *Envelope) MergeDetected(d Detected) {
	if e.Version == 0 {
		e.Version = EnvelopeVersion
	}
	if d.MatchPassword != "" {
		e.MatchPassword = d.MatchPassword
	}
	if d.ASCIssuerID != "" || d.ASCKeyID != "" || d.ASCP8 != "" {
		if e.ASC == nil {
			e.ASC = &ASCCreds{}
		}
		if d.ASCIssuerID != "" {
			e.ASC.IssuerID = d.ASCIssuerID
		}
		if d.ASCKeyID != "" {
			e.ASC.KeyID = d.ASCKeyID
		}
		if d.ASCP8 != "" {
			e.ASC.P8 = d.ASCP8
		}
	}
	if len(d.PlaySAJSON) > 0 {
		e.PlayServiceAccount = d.PlaySAJSON
	}
	if len(d.FirebaseAdminJSON) > 0 {
		e.FirebaseAdmin = d.FirebaseAdminJSON
	}
}

func repairPEM(s string) string {
	if _, rest := pem.Decode([]byte(s)); len(rest) == 0 {
		return s
	}
	m := regexp.MustCompile(`(?s)-----BEGIN ([A-Z ]*PRIVATE KEY)-----(.*?)-----END [A-Z ]*PRIVATE KEY-----`).FindStringSubmatch(s)
	if m == nil {
		return s
	}
	body := regexp.MustCompile(`\s+`).ReplaceAllString(m[2], "")
	var b strings.Builder
	fmt.Fprintf(&b, "-----BEGIN %s-----\n", m[1])
	for i := 0; i < len(body); i += 64 {
		end := i + 64
		if end > len(body) {
			end = len(body)
		}
		b.WriteString(body[i:end] + "\n")
	}
	fmt.Fprintf(&b, "-----END %s-----\n", m[1])
	return b.String()
}

// findServiceAccount returns embedded service_account JSON and whether
// it looks like Firebase Admin (client_email / project heuristics).
func findServiceAccount(s string) (obj string, kind string, ok bool) {
	for start := 0; start < len(s); start++ {
		if s[start] != '{' {
			continue
		}
		depth, inStr, esc := 0, false, false
		for i := start; i < len(s); i++ {
			c := s[i]
			switch {
			case esc:
				esc = false
			case c == '\\' && inStr:
				esc = true
			case c == '"':
				inStr = !inStr
			case inStr:
			case c == '{':
				depth++
			case c == '}':
				depth--
				if depth == 0 {
					cand := s[start : i+1]
					var probe struct {
						Type        string `json:"type"`
						ClientEmail string `json:"client_email"`
						PrivateKey  string `json:"private_key"`
						ProjectID   string `json:"project_id"`
					}
					if err := json.Unmarshal([]byte(cand), &probe); err == nil &&
						probe.Type == "service_account" &&
						probe.PrivateKey != "" && probe.ClientEmail != "" {
						kind = "play"
						low := strings.ToLower(probe.ClientEmail + " " + probe.ProjectID)
						if strings.Contains(low, "firebase") || strings.Contains(low, "gserviceaccount.com") && strings.Contains(low, "firebase") {
							kind = "firebase"
						}
						// firebase-adminsdk is the usual email prefix.
						if strings.Contains(strings.ToLower(probe.ClientEmail), "firebase-adminsdk") {
							kind = "firebase"
						}
						return cand, kind, true
					}
					start = i
					goto next
				}
			}
		}
	next:
	}
	return "", "", false
}

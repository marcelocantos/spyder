// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package ship

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/marcelocantos/spyder/internal/paths"
)

// SpyderVersion is set by main from ldflags (defaults to "dev").
var SpyderVersion = "dev"

// AuditRoot returns ~/.spyder/ship-audit (injectable for hermetic tests).
var AuditRoot = paths.ShipAuditBase

// AuditRecord is one redacted JSONL line under ship-audit (🎯T133.6).
type AuditRecord struct {
	ID             string            `json:"id"`
	Timestamp      time.Time         `json:"ts"`
	Hostname       string            `json:"hostname,omitempty"`
	SpyderVersion  string            `json:"spyder_version"`
	CDHash         string            `json:"cdhash,omitempty"`
	Studio         string            `json:"studio"`
	Class          LaneClass         `json:"class"`
	Action         string            `json:"action"`
	Argv           []string          `json:"argv"`
	Cwd            string            `json:"cwd,omitempty"`
	GitSHA         string            `json:"git_sha,omitempty"`
	GitDirty       bool              `json:"git_dirty,omitempty"`
	SecretsPresent map[string]bool   `json:"secrets_present,omitempty"`
	Fingerprints   map[string]string `json:"fingerprints,omitempty"`
	ExitCode       int               `json:"exit_code"`
	DurationMS     int64             `json:"duration_ms"`
	DryRun         bool              `json:"dry_run"`
	Confirm        bool              `json:"confirm"`
	LogPath        string            `json:"log_path,omitempty"`
	ReflectPath    string            `json:"reflect_path,omitempty"`
}

// WriteAudit appends a JSONL line and a markdown section. For non-dry-run
// test_publish / prod_publish it also writes a reflection stub first so
// ReflectPath is present in the JSONL record.
func WriteAudit(rec *AuditRecord) error {
	if rec.ID == "" {
		rec.ID = newRunID()
	}
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now().UTC()
	}
	if rec.SpyderVersion == "" {
		rec.SpyderVersion = SpyderVersion
	}
	if rec.Hostname == "" {
		rec.Hostname, _ = os.Hostname()
	}

	root := AuditRoot()
	day := rec.Timestamp.UTC().Format("2006-01-02")
	runsDir := filepath.Join(root, "runs")
	if err := os.MkdirAll(runsDir, 0o700); err != nil {
		return err
	}

	if !rec.DryRun && (rec.Class == ClassTestPublish || rec.Class == ClassProdPublish) {
		reflectPath := filepath.Join(runsDir, rec.ID+"-reflect.md")
		if err := os.WriteFile(reflectPath, []byte(formatReflectStub(rec)), 0o600); err != nil {
			return err
		}
		rec.ReflectPath = reflectPath
	}

	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	jsonl := filepath.Join(root, day+".jsonl")
	f, err := os.OpenFile(jsonl, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	_ = f.Close()

	md := filepath.Join(root, day+".md")
	mf, err := os.OpenFile(md, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := mf.WriteString(formatAuditMD(rec)); err != nil {
		_ = mf.Close()
		return err
	}
	_ = mf.Close()
	return nil
}

// WriteRunLog writes a scrubbed child stdout/stderr capture.
func WriteRunLog(id, content string) (string, error) {
	root := AuditRoot()
	if err := os.MkdirAll(filepath.Join(root, "runs"), 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(root, "runs", id+".log")
	if err := os.WriteFile(path, []byte(ScrubSecrets(content)), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

var (
	rePEMBegin = regexp.MustCompile(`(?m)^-----BEGIN [A-Z0-9 ]+-----[\s\S]*?-----END [A-Z0-9 ]+-----`)
	rePassword = regexp.MustCompile(`(?i)(password|passwd|MATCH_PASSWORD)\s*[=:]\s*\S+`)
)

// ScrubSecrets redacts PEMs and password= assignments from captured logs.
func ScrubSecrets(s string) string {
	s = rePEMBegin.ReplaceAllString(s, "[REDACTED PEM]")
	s = rePassword.ReplaceAllString(s, "${1}=[REDACTED]")
	return s
}

func newRunID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func formatAuditMD(rec *AuditRecord) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n## %s — %s / %s\n", rec.Timestamp.Format(time.RFC3339), rec.Studio, rec.Class)
	fmt.Fprintf(&b, "- id: `%s`\n", rec.ID)
	fmt.Fprintf(&b, "- action: `%s`\n", rec.Action)
	fmt.Fprintf(&b, "- argv: `%s`\n", strings.Join(rec.Argv, " "))
	fmt.Fprintf(&b, "- exit: %d  duration_ms: %d  dry_run: %v  confirm: %v\n",
		rec.ExitCode, rec.DurationMS, rec.DryRun, rec.Confirm)
	if rec.LogPath != "" {
		fmt.Fprintf(&b, "- log: `%s`\n", rec.LogPath)
	}
	if rec.ReflectPath != "" {
		fmt.Fprintf(&b, "- reflect: `%s`\n", rec.ReflectPath)
	}
	return b.String()
}

func formatReflectStub(rec *AuditRecord) string {
	return fmt.Sprintf(`# Run %s

Lane class: %s
Argv: %s
What we expected:
What happened (fill in):
Surprises:
Change the consumer recipe? (yes/no — where)
Change spyder? (yes/no — file a target)
`, rec.ID, rec.Class, strings.Join(rec.Argv, " "))
}

// GitMeta reads HEAD SHA and dirty flag from cwd (best-effort).
func GitMeta(cwd string) (sha string, dirty bool) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	sha = strings.TrimSpace(string(out))
	cmd = exec.Command("git", "status", "--porcelain")
	cmd.Dir = cwd
	st, err := cmd.Output()
	if err != nil {
		return sha, false
	}
	return sha, len(strings.TrimSpace(string(st))) > 0
}

// CDHashSelf returns the running binary's CDHash if available.
func CDHashSelf() string {
	sig, err := InspectSelf()
	if err != nil {
		return ""
	}
	return sig.CDHash
}

// ListUnfilledReflections returns paths to *-reflect.md stubs that still
// contain the fill-in prompts (unfilled).
func ListUnfilledReflections() ([]string, error) {
	root := filepath.Join(AuditRoot(), "runs")
	ents, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "-reflect.md") {
			continue
		}
		path := filepath.Join(root, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if isUnfilledReflect(string(raw)) {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out, nil
}

func isUnfilledReflect(raw string) bool {
	// Stub is unfilled while the "What happened" line is still empty
	// (immediately followed by Surprises:).
	return strings.Contains(raw, "What happened (fill in):\nSurprises:")
}

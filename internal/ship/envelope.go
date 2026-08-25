// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package ship

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// EnvelopeVersion is the JSON schema version stored in the keychain item.
const EnvelopeVersion = 1

// Kind names inside a studio envelope (🎯T133.2).
const (
	KindMatchPassword      = "match_password"
	KindASC                = "asc"
	KindPlayUpload         = "play_upload"
	KindPlayServiceAccount = "play_service_account"
	KindFirebaseAdmin      = "firebase_admin"
	KindFirebaseAdminDev   = "firebase_admin_dev"
)

// Envelope is the versioned JSON blob stored as one generic-password
// item per studio (service spyder.studio / account squz|minicades).
type Envelope struct {
	Version int `json:"version"`

	MatchPassword string `json:"match_password,omitempty"`

	ASC *ASCCreds `json:"asc,omitempty"`

	PlayUpload *PlayUploadCreds `json:"play_upload,omitempty"`

	PlayServiceAccount json.RawMessage `json:"play_service_account,omitempty"`

	FirebaseAdmin    json.RawMessage `json:"firebase_admin,omitempty"`
	FirebaseAdminDev json.RawMessage `json:"firebase_admin_dev,omitempty"`
}

// ASCCreds is the App Store Connect API key (.p8 + ids).
type ASCCreds struct {
	IssuerID string `json:"issuer_id"`
	KeyID    string `json:"key_id"`
	P8       string `json:"p8"`
}

// PlayUploadCreds holds a PKCS12/JKS blob for Play upload signing.
// Minicades may carry multiple aliases inside the same keystore bytes;
// Squz uses a single "upload" alias.
type PlayUploadCreds struct {
	// Keystore is raw PKCS12 or JKS bytes (JSON-encoded as base64 via
	// custom marshal — stored as base64 string in the envelope JSON).
	Keystore []byte `json:"keystore"`
	// Format is "pkcs12" or "jks".
	Format string `json:"format,omitempty"`
	// Password unlocks the keystore.
	Password string `json:"password"`
	// Alias is the default signing alias (squz: "upload"). Empty means
	// multi-alias JKS (minicades) — consumer picks the alias.
	Alias string `json:"alias,omitempty"`
	// Aliases lists known aliases when Format is multi-alias JKS.
	Aliases []string `json:"aliases,omitempty"`
}

// Status is the redacted view of an envelope (booleans + fingerprints).
type Status struct {
	Studio       string            `json:"studio"`
	TeamID       string            `json:"apple_team_id"`
	Present      map[string]bool   `json:"present"`
	Fingerprints map[string]string `json:"fingerprints,omitempty"`
}

// ParseEnvelope unmarshals a keychain blob. Empty/missing → zero envelope.
func ParseEnvelope(raw []byte) (*Envelope, error) {
	if len(raw) == 0 {
		return &Envelope{Version: EnvelopeVersion}, nil
	}
	var e Envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, fmt.Errorf("envelope JSON: %w", err)
	}
	if e.Version == 0 {
		e.Version = EnvelopeVersion
	}
	if e.Version > EnvelopeVersion {
		return nil, fmt.Errorf("envelope version %d newer than supported %d", e.Version, EnvelopeVersion)
	}
	return &e, nil
}

// Bytes marshals the envelope for keychain storage.
func (e *Envelope) Bytes() ([]byte, error) {
	if e.Version == 0 {
		e.Version = EnvelopeVersion
	}
	return json.Marshal(e)
}

// LoadStudio reads and parses the studio envelope from the keychain.
func LoadStudio(studio string) (*Envelope, error) {
	studio, err := NormalizeStudio(studio)
	if err != nil {
		return nil, err
	}
	raw, err := GetItem(KeychainService, studio)
	if err != nil {
		return nil, err
	}
	return ParseEnvelope(raw)
}

// SaveStudio writes the studio envelope (requires secrets access gate
// to have already passed at the CLI layer).
func SaveStudio(studio string, e *Envelope) error {
	studio, err := NormalizeStudio(studio)
	if err != nil {
		return err
	}
	raw, err := e.Bytes()
	if err != nil {
		return err
	}
	return SetItem(KeychainService, studio, raw)
}

// NormalizeStudio lowercases and validates squz|minicades.
func NormalizeStudio(studio string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(studio))
	if _, ok := AppleTeamID[s]; !ok {
		return "", fmt.Errorf("unknown studio %q (want squz|minicades)", studio)
	}
	return s, nil
}

// RedactedStatus never includes secret values — only presence + fingerprints.
func (e *Envelope) RedactedStatus(studio string) Status {
	studio = strings.ToLower(studio)
	st := Status{
		Studio:       studio,
		TeamID:       AppleTeamID[studio],
		Present:      map[string]bool{},
		Fingerprints: map[string]string{},
	}
	st.Present[KindMatchPassword] = e.MatchPassword != ""
	if e.ASC != nil && e.ASC.P8 != "" {
		st.Present[KindASC] = true
		st.Fingerprints["asc_key_id"] = e.ASC.KeyID
	}
	if e.PlayUpload != nil && len(e.PlayUpload.Keystore) > 0 {
		st.Present[KindPlayUpload] = true
		sum := sha256.Sum256(e.PlayUpload.Keystore)
		st.Fingerprints["play_upload_sha256"] = hex.EncodeToString(sum[:])
		if e.PlayUpload.Alias != "" {
			st.Fingerprints["play_upload_alias"] = e.PlayUpload.Alias
		}
	}
	if len(e.PlayServiceAccount) > 0 {
		st.Present[KindPlayServiceAccount] = true
		if email := jsonEmail(e.PlayServiceAccount); email != "" {
			st.Fingerprints["play_sa_email"] = email
		}
	}
	if len(e.FirebaseAdmin) > 0 {
		st.Present[KindFirebaseAdmin] = true
		if email := jsonEmail(e.FirebaseAdmin); email != "" {
			st.Fingerprints["firebase_admin_email"] = email
		}
	}
	if len(e.FirebaseAdminDev) > 0 {
		st.Present[KindFirebaseAdminDev] = true
		if email := jsonEmail(e.FirebaseAdminDev); email != "" {
			st.Fingerprints["firebase_admin_dev_email"] = email
		}
	}
	return st
}

func jsonEmail(raw json.RawMessage) string {
	var m struct {
		ClientEmail string `json:"client_email"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.ClientEmail
}

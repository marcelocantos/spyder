// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package ship

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseEnvelope_Empty(t *testing.T) {
	e, err := ParseEnvelope(nil)
	if err != nil {
		t.Fatal(err)
	}
	if e.Version != EnvelopeVersion {
		t.Fatalf("version %d", e.Version)
	}
}

func TestEnvelope_RoundTripAndRedacted(t *testing.T) {
	e := &Envelope{
		Version:       EnvelopeVersion,
		MatchPassword: "sekrit",
		ASC: &ASCCreds{
			IssuerID: "11111111-2222-3333-4444-555555555555",
			KeyID:    "ABCDEF1234",
			P8:       "-----BEGIN PRIVATE KEY-----\nMII\n-----END PRIVATE KEY-----\n",
		},
		PlayUpload: &PlayUploadCreds{
			Keystore: []byte{0x01, 0x02, 0x03, 0x04},
			Format:   "pkcs12",
			Password: "pw",
			Alias:    "upload",
		},
		PlayServiceAccount: json.RawMessage(`{"type":"service_account","client_email":"sa@proj.iam.gserviceaccount.com"}`),
	}
	raw, err := e.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sekrit") {
		// match_password is in the blob — that's fine for keychain storage;
		// redacted status must not leak it.
	}
	got, err := ParseEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.MatchPassword != "sekrit" || got.ASC.KeyID != "ABCDEF1234" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	st := got.RedactedStatus(StudioSquz)
	if !st.Present[KindMatchPassword] || !st.Present[KindASC] || !st.Present[KindPlayUpload] {
		t.Fatalf("present map: %+v", st.Present)
	}
	if st.Fingerprints["asc_key_id"] != "ABCDEF1234" {
		t.Fatalf("asc fingerprint: %+v", st.Fingerprints)
	}
	if st.Fingerprints["play_sa_email"] != "sa@proj.iam.gserviceaccount.com" {
		t.Fatalf("sa email: %+v", st.Fingerprints)
	}
	enc, _ := json.Marshal(st)
	if strings.Contains(string(enc), "sekrit") || strings.Contains(string(enc), "BEGIN PRIVATE") {
		t.Fatalf("redacted status leaked secrets: %s", enc)
	}
}

func TestNormalizeStudio(t *testing.T) {
	s, err := NormalizeStudio("Squz")
	if err != nil || s != StudioSquz {
		t.Fatalf("got %q %v", s, err)
	}
	if _, err := NormalizeStudio("other"); err == nil {
		t.Fatal("expected error")
	}
}

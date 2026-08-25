// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package ship

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestValidateEnvelopeShape_ASC(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	p8 := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	env := &Envelope{ASC: &ASCCreds{IssuerID: "11111111-2222-3333-4444-555555555555", KeyID: "ABCDEF1234", P8: p8}}
	if err := ValidateEnvelopeShape(env); err != nil {
		t.Fatal(err)
	}
	env.ASC.KeyID = ""
	if err := ValidateEnvelopeShape(env); err == nil {
		t.Fatal("want key_id error")
	}
}

func TestVerifyEnvelopeLive_MockHTTP(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	p8 := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	env := &Envelope{ASC: &ASCCreds{
		IssuerID: "11111111-2222-3333-4444-555555555555",
		KeyID:    "ABCDEF1234",
		P8:       p8,
	}}

	prev := HTTPDo
	t.Cleanup(func() { HTTPDo = prev })
	HTTPDo = func(req *http.Request) (*http.Response, error) {
		if !strings.Contains(req.URL.Path, "/v1/apps") {
			t.Fatalf("unexpected %s", req.URL)
		}
		if !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer ") {
			t.Fatal("missing bearer")
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"data":[]}`))),
			Header:     make(http.Header),
		}, nil
	}
	if err := VerifyEnvelopeLive(env); err != nil {
		t.Fatal(err)
	}
}

func TestValidateEnvelopeShape_PlaySA(t *testing.T) {
	raw := json.RawMessage(`{"type":"service_account","client_email":"a@b.iam.gserviceaccount.com","private_key":"-----BEGIN PRIVATE KEY-----\nMII\n-----END PRIVATE KEY-----\n","token_uri":"https://oauth2.googleapis.com/token"}`)
	// PEM won't parse — expect error from shape on live path; shape only checks parsePlaySA fields.
	if _, _, _, err := parsePlaySA(raw); err != nil {
		t.Fatal(err)
	}
}

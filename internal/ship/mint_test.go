// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package ship

import (
	"strings"
	"testing"

	"software.sslmate.com/src/go-pkcs12"
)

func TestMintPlayUpload_Squz(t *testing.T) {
	creds, err := MintPlayUpload(StudioSquz)
	if err != nil {
		t.Fatal(err)
	}
	if creds.Alias != "upload" || creds.Format != "pkcs12" || creds.Password == "" {
		t.Fatalf("%+v", creds)
	}
	if len(creds.Keystore) == 0 {
		t.Fatal("empty keystore")
	}
	_, cert, err := pkcs12.Decode(creds.Keystore, creds.Password)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != "upload" {
		t.Fatalf("cn %s", cert.Subject.CommonName)
	}
}

func TestMintPlayUpload_RefuseMinicades(t *testing.T) {
	_, err := MintPlayUpload(StudioMinicades)
	if err == nil || !strings.Contains(err.Error(), "minicades") {
		t.Fatalf("got %v", err)
	}
}

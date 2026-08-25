// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package ship

import (
	"strings"
	"testing"
	"time"
)

func TestDetect_ChatWrappedASC(t *testing.T) {
	paste := "hey can you store this?\n\n" +
		"> **Issuer ID:** 69a6de79-9175-47e3-e053-5b8c7c11a4d1\n" +
		"> KeyID: ABC123DEF4\n" +
		"-----BEGIN PRIVATE KEY-----\n" +
		"MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7\n" +
		"-----END PRIVATE KEY-----\n" +
		"thanks!"
	d, err := Detect(paste)
	if err != nil {
		t.Fatal(err)
	}
	if d.ASCIssuerID != "69a6de79-9175-47e3-e053-5b8c7c11a4d1" {
		t.Fatalf("issuer %q", d.ASCIssuerID)
	}
	if d.ASCKeyID != "ABC123DEF4" {
		t.Fatalf("key id %q", d.ASCKeyID)
	}
	if !strings.Contains(d.ASCP8, "BEGIN PRIVATE KEY") {
		t.Fatalf("pem %q", d.ASCP8)
	}
}

func TestDetect_PlayServiceAccount(t *testing.T) {
	paste := `noise {"type":"service_account","client_email":"sa@proj.iam.gserviceaccount.com","private_key":"-----BEGIN PRIVATE KEY-----\nMII\n-----END PRIVATE KEY-----\n"} more`
	d, err := Detect(paste)
	if err != nil {
		t.Fatal(err)
	}
	if d.PlaySAEmail != "sa@proj.iam.gserviceaccount.com" {
		t.Fatalf("email %q", d.PlaySAEmail)
	}
	if len(d.PlaySAJSON) == 0 {
		t.Fatal("missing JSON")
	}
}

func TestDetect_FirebaseAdmin(t *testing.T) {
	paste := `{"type":"service_account","client_email":"firebase-adminsdk-x@myproj.iam.gserviceaccount.com","private_key":"-----BEGIN PRIVATE KEY-----\nMII\n-----END PRIVATE KEY-----\n","project_id":"myproj"}`
	d, err := Detect(paste)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.FirebaseAdminJSON) == 0 {
		t.Fatalf("want firebase, got play email=%q", d.PlaySAEmail)
	}
}

func TestDetect_RefusesBareProseKeyID(t *testing.T) {
	// TESTFLIGHT is ten uppercase chars — must not be absorbed from prose.
	_, err := Detect("we use TESTFLIGHT for internal builds")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDetect_BareKeyIDAlone(t *testing.T) {
	d, err := Detect("ABC123DEF4")
	if err != nil {
		t.Fatal(err)
	}
	if d.ASCKeyID != "ABC123DEF4" {
		t.Fatalf("got %q", d.ASCKeyID)
	}
}

func TestMergeDetected_Partial(t *testing.T) {
	e := &Envelope{Version: EnvelopeVersion}
	e.MergeDetected(Detected{ASCKeyID: "AAAAAAAAAA"})
	e.MergeDetected(Detected{ASCIssuerID: "11111111-2222-3333-4444-555555555555"})
	e.MergeDetected(Detected{ASCP8: "-----BEGIN PRIVATE KEY-----\nx\n-----END PRIVATE KEY-----\n"})
	if e.ASC.KeyID != "AAAAAAAAAA" || e.ASC.IssuerID == "" || e.ASC.P8 == "" {
		t.Fatalf("%+v", e.ASC)
	}
}

type memPB struct {
	s string
}

func (m *memPB) Get() (string, error) { return m.s, nil }
func (m *memPB) Set(s string) error   { m.s = s; return nil }

func TestAwaitPaste_Clears(t *testing.T) {
	pb := &memPB{}
	done := make(chan string, 1)
	go func() {
		s, err := AwaitPaste(pb, 2*time.Second)
		if err != nil {
			t.Errorf("await: %v", err)
			done <- ""
			return
		}
		done <- s
	}()
	time.Sleep(clipPoll + 20*time.Millisecond)
	pb.s = "ABC123DEF4"
	got := <-done
	if got != "ABC123DEF4" {
		t.Fatalf("got %q", got)
	}
	if pb.s != "" {
		t.Fatalf("clipboard not cleared: %q", pb.s)
	}
}

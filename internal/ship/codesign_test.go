// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package ship

import (
	"errors"
	"strings"
	"testing"
)

func TestRequireSecretsAccess_AllowUnsignedEnv(t *testing.T) {
	t.Setenv(AllowUnsignedEnv, "1")
	InspectExe = func(path string) (Signature, error) {
		t.Fatal("InspectExe must not run when allow-unsigned is set")
		return Signature{}, nil
	}
	t.Cleanup(func() { InspectExe = inspectExe })
	if err := RequireSecretsAccess(); err != nil {
		t.Fatalf("expected nil with %s=1: %v", AllowUnsignedEnv, err)
	}
}

func TestRequireSecretsAccess_RefusesUnsigned(t *testing.T) {
	t.Setenv(AllowUnsignedEnv, "")
	InspectExe = func(path string) (Signature, error) {
		return Signature{Path: path, Signed: false}, nil
	}
	t.Cleanup(func() { InspectExe = inspectExe })
	err := RequireSecretsAccess()
	if !errors.Is(err, ErrUnsigned) {
		t.Fatalf("want ErrUnsigned, got %v", err)
	}
}

func TestRequireSecretsAccess_RefusesAdHoc(t *testing.T) {
	t.Setenv(AllowUnsignedEnv, "")
	InspectExe = func(path string) (Signature, error) {
		return Signature{
			Path:       path,
			Signed:     true,
			AdHoc:      true,
			Identifier: BundleID,
		}, nil
	}
	t.Cleanup(func() { InspectExe = inspectExe })
	err := RequireSecretsAccess()
	if !errors.Is(err, ErrUnsigned) {
		t.Fatalf("want ErrUnsigned for ad-hoc, got %v", err)
	}
}

func TestRequireSecretsAccess_RefusesWrongBundleID(t *testing.T) {
	t.Setenv(AllowUnsignedEnv, "")
	InspectExe = func(path string) (Signature, error) {
		return Signature{
			Path:       path,
			Signed:     true,
			Identifier: "com.example.other",
			Authority:  "Apple Development: Test",
		}, nil
	}
	t.Cleanup(func() { InspectExe = inspectExe })
	err := RequireSecretsAccess()
	if !errors.Is(err, ErrUnsigned) {
		t.Fatalf("want ErrUnsigned for wrong id, got %v", err)
	}
}

func TestRequireSecretsAccess_AcceptsStablePrincipal(t *testing.T) {
	t.Setenv(AllowUnsignedEnv, "")
	InspectExe = func(path string) (Signature, error) {
		return Signature{
			Path:       path,
			Signed:     true,
			Identifier: BundleID,
			Authority:  "Apple Development: MARCELO CANTOS (GJF5DNC392)",
			TeamID:     "SWA3H3N7TW",
		}, nil
	}
	t.Cleanup(func() { InspectExe = inspectExe })
	if err := RequireSecretsAccess(); err != nil {
		t.Fatalf("expected accept: %v", err)
	}
}

func TestStablePrincipal(t *testing.T) {
	cases := []struct {
		name string
		sig  Signature
		want bool
	}{
		{"empty", Signature{}, false},
		{"unsigned", Signature{Signed: false}, false},
		{"adhoc", Signature{Signed: true, AdHoc: true, Identifier: BundleID}, false},
		{"wrong id", Signature{Signed: true, Identifier: "a.out"}, false},
		{"ok", Signature{Signed: true, Identifier: BundleID, Authority: "Dev"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sig.StablePrincipal(); got != tc.want {
				t.Fatalf("StablePrincipal()=%v want %v", got, tc.want)
			}
		})
	}
}

func TestFormatSignature(t *testing.T) {
	if got := FormatSignature(Signature{}); got != "unsigned" {
		t.Fatalf("got %q", got)
	}
	s := Signature{
		Signed:     true,
		Identifier: BundleID,
		Authority:  "Apple Development: X",
		TeamID:     "TEAM",
	}
	got := FormatSignature(s)
	if !strings.Contains(got, "id="+BundleID) || !strings.Contains(got, "principal=ok") {
		t.Fatalf("got %q", got)
	}
}

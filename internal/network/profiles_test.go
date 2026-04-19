// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package network_test

import (
	"testing"

	"github.com/marcelocantos/spyder/internal/network"
)

func TestParse_namedProfiles(t *testing.T) {
	cases := []struct {
		name        string
		wantUp      int
		wantDown    int
		wantDelay   int
		wantLoss    int
		wantOffline bool
	}{
		{"wifi", 0, 0, 0, 0, false},
		{"4g", 5760, 14400, 20, 0, false},
		{"3g", 384, 2000, 100, 0, false},
		{"edge", 128, 384, 400, 0, false},
		{"gsm", 40, 114, 600, 0, false},
		{"offline", 0, 0, 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := network.Parse(tc.name)
			if err != nil {
				t.Fatalf("Parse(%q): unexpected error: %v", tc.name, err)
			}
			if p.Name != tc.name {
				t.Errorf("Name = %q, want %q", p.Name, tc.name)
			}
			if p.UploadKbps != tc.wantUp {
				t.Errorf("UploadKbps = %d, want %d", p.UploadKbps, tc.wantUp)
			}
			if p.DownloadKbps != tc.wantDown {
				t.Errorf("DownloadKbps = %d, want %d", p.DownloadKbps, tc.wantDown)
			}
			if p.DelayMs != tc.wantDelay {
				t.Errorf("DelayMs = %d, want %d", p.DelayMs, tc.wantDelay)
			}
			if p.LossPct != tc.wantLoss {
				t.Errorf("LossPct = %d, want %d", p.LossPct, tc.wantLoss)
			}
			if p.IsOffline != tc.wantOffline {
				t.Errorf("IsOffline = %v, want %v", p.IsOffline, tc.wantOffline)
			}
		})
	}
}

func TestParse_dynamicLossy(t *testing.T) {
	cases := []struct {
		input    string
		wantLoss int
		wantErr  bool
	}{
		{"lossy-0", 0, false},
		{"lossy-25", 25, false},
		{"lossy-100", 100, false},
		{"lossy-101", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			p, err := network.Parse(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Parse(%q): expected error, got nil", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q): unexpected error: %v", tc.input, err)
			}
			if p.LossPct != tc.wantLoss {
				t.Errorf("LossPct = %d, want %d", p.LossPct, tc.wantLoss)
			}
		})
	}
}

func TestParse_dynamicDelay(t *testing.T) {
	cases := []struct {
		input     string
		wantDelay int
		wantErr   bool
	}{
		{"delay-0", 0, false},
		{"delay-50", 50, false},
		{"delay-2000", 2000, false},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			p, err := network.Parse(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Parse(%q): expected error, got nil", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q): unexpected error: %v", tc.input, err)
			}
			if p.DelayMs != tc.wantDelay {
				t.Errorf("DelayMs = %d, want %d", p.DelayMs, tc.wantDelay)
			}
		})
	}
}

func TestParse_unknownProfile(t *testing.T) {
	_, err := network.Parse("bogus")
	if err == nil {
		t.Error("Parse(\"bogus\"): expected error, got nil")
	}
}

func TestADBSpeedClass(t *testing.T) {
	cases := []struct {
		profile string
		wantKw  string
		wantOK  bool
	}{
		{"4g", "hsdpa", true},
		{"3g", "umts", true},
		{"edge", "edge", true},
		{"gsm", "gprs", true},
		{"wifi", "", false},
		{"offline", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.profile, func(t *testing.T) {
			p, _ := network.Parse(tc.profile)
			kw, ok := network.ADBSpeedClass(p)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
			if kw != tc.wantKw {
				t.Errorf("keyword = %q, want %q", kw, tc.wantKw)
			}
		})
	}
}

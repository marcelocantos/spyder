// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package playerweb

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAnnexBAccessUnits_FromFixture(t *testing.T) {
	path := filepath.Join("testdata", "baseline_320x240_60fps.h264")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	aus, err := annexBAccessUnits(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(aus) < 10 {
		t.Fatalf("expected many access units, got %d", len(aus))
	}
	if !accessUnitIsKeyframe(aus[0]) {
		t.Fatal("first access unit must be a keyframe (SPS/PPS/IDR)")
	}
	avcc, err := annexBToAVCC(aus[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(avcc) < 50 {
		t.Fatalf("avcc too small: %d", len(avcc))
	}
}

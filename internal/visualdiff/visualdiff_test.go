// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package visualdiff_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math"
	"testing"

	"github.com/marcelocantos/spyder/internal/baselines"
	"github.com/marcelocantos/spyder/internal/visualdiff"
)

// makePNG creates an n×n single-colour PNG in memory.
func makePNG(t *testing.T, n int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	for y := range n {
		for x := range n {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

// ---------------------------------------------------------------------------
// Pixel tests
// ---------------------------------------------------------------------------

func TestPixel_Identical(t *testing.T) {
	png1 := makePNG(t, 4, color.RGBA{R: 100, G: 150, B: 200, A: 255})
	rep, err := visualdiff.Pixel(png1, png1)
	if err != nil {
		t.Fatalf("Pixel: %v", err)
	}
	if rep.RMSError != 0 {
		t.Fatalf("identical images: RMSError = %v, want 0", rep.RMSError)
	}
	if !math.IsNaN(rep.SSIMScore) {
		t.Fatalf("expected NaN SSIMScore (stubbed), got %v", rep.SSIMScore)
	}
	if rep.SSIMNote == "" {
		t.Fatal("expected SSIMNote to explain the stub")
	}
	if rep.Width != 4 || rep.Height != 4 {
		t.Fatalf("size: got %dx%d, want 4x4", rep.Width, rep.Height)
	}
}

func TestPixel_MaxError(t *testing.T) {
	black := makePNG(t, 4, color.RGBA{A: 255})
	white := makePNG(t, 4, color.RGBA{R: 255, G: 255, B: 255, A: 255})
	rep, err := visualdiff.Pixel(black, white)
	if err != nil {
		t.Fatalf("Pixel: %v", err)
	}
	// RMS should be 1.0 for fully contrasted images.
	if math.Abs(rep.RMSError-1.0) > 0.01 {
		t.Fatalf("max error: got %.4f, want ~1.0", rep.RMSError)
	}
}

func TestPixel_SizeMismatch(t *testing.T) {
	a := makePNG(t, 4, color.RGBA{A: 255})
	b := makePNG(t, 8, color.RGBA{A: 255})
	rep, err := visualdiff.Pixel(a, b)
	if err != nil {
		t.Fatalf("Pixel: %v", err)
	}
	if !rep.SizeMismatch {
		t.Fatal("expected SizeMismatch=true")
	}
	if rep.RMSError != 1.0 {
		t.Fatalf("SizeMismatch: RMSError = %v, want 1.0", rep.RMSError)
	}
}

func TestPixel_PartialDiff(t *testing.T) {
	// One red, one blue — same brightness but different channels.
	red := makePNG(t, 2, color.RGBA{R: 255, A: 255})
	blue := makePNG(t, 2, color.RGBA{B: 255, A: 255})
	rep, err := visualdiff.Pixel(red, blue)
	if err != nil {
		t.Fatalf("Pixel: %v", err)
	}
	// Each pixel: dR=1, dG=0, dB=-1 → sumSq per px = 2; rms = sqrt(2/3)
	want := math.Sqrt(2.0 / 3.0)
	if math.Abs(rep.RMSError-want) > 0.01 {
		t.Fatalf("partial diff: got %.4f, want ~%.4f", rep.RMSError, want)
	}
}

func TestPixel_BadData(t *testing.T) {
	_, err := visualdiff.Pixel([]byte("not a png"), []byte("not a png"))
	if err == nil {
		t.Fatal("expected error for bad PNG data")
	}
}

// ---------------------------------------------------------------------------
// Manifest tests
// ---------------------------------------------------------------------------

func marshalManifest(t *testing.T, m baselines.Manifest) []byte {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func makeManifest(els ...baselines.Element) baselines.Manifest {
	return baselines.Manifest{SchemaVersion: 1, Elements: els}
}

func el(id, kind string, bbox [4]int, attrs map[string]any) baselines.Element {
	return baselines.Element{ID: id, Kind: kind, BBox: bbox, Attrs: attrs}
}

func TestManifest_Identical(t *testing.T) {
	m := makeManifest(
		el("a/b/btn", "button", [4]int{0, 0, 100, 40}, nil),
		el("a/b/lbl", "label", [4]int{0, 50, 200, 20}, nil),
	)
	data := marshalManifest(t, m)
	rep, err := visualdiff.Manifest(data, data)
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if len(rep.Added)+len(rep.Removed)+len(rep.Moved) > 0 {
		t.Fatalf("expected no changes: %+v", rep)
	}
	if rep.Unchanged != 2 {
		t.Fatalf("Unchanged: got %d, want 2", rep.Unchanged)
	}
}

func TestManifest_Added(t *testing.T) {
	ma := makeManifest(el("a/b/btn", "button", [4]int{0, 0, 100, 40}, nil))
	mb := makeManifest(
		el("a/b/btn", "button", [4]int{0, 0, 100, 40}, nil),
		el("a/b/new", "label", [4]int{0, 50, 200, 20}, nil),
	)
	rep, err := visualdiff.Manifest(marshalManifest(t, ma), marshalManifest(t, mb))
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if len(rep.Added) != 1 || rep.Added[0] != "a/b/new" {
		t.Fatalf("Added: got %v, want [a/b/new]", rep.Added)
	}
	if len(rep.Removed) != 0 {
		t.Fatalf("Removed: got %v, want []", rep.Removed)
	}
}

func TestManifest_Removed(t *testing.T) {
	ma := makeManifest(
		el("a/b/btn", "button", [4]int{0, 0, 100, 40}, nil),
		el("a/b/old", "label", [4]int{0, 50, 200, 20}, nil),
	)
	mb := makeManifest(el("a/b/btn", "button", [4]int{0, 0, 100, 40}, nil))
	rep, err := visualdiff.Manifest(marshalManifest(t, ma), marshalManifest(t, mb))
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if len(rep.Removed) != 1 || rep.Removed[0] != "a/b/old" {
		t.Fatalf("Removed: got %v, want [a/b/old]", rep.Removed)
	}
}

func TestManifest_Moved(t *testing.T) {
	ma := makeManifest(el("a/b/btn", "button", [4]int{0, 0, 100, 40}, nil))
	mb := makeManifest(el("a/b/btn", "button", [4]int{10, 5, 100, 40}, nil))
	rep, err := visualdiff.Manifest(marshalManifest(t, ma), marshalManifest(t, mb))
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if len(rep.Moved) != 1 {
		t.Fatalf("Moved: got %v, want 1", rep.Moved)
	}
	if rep.Moved[0].ID != "a/b/btn" {
		t.Fatalf("Moved[0].ID = %q, want a/b/btn", rep.Moved[0].ID)
	}
	if rep.Moved[0].From != [4]int{0, 0, 100, 40} {
		t.Fatalf("Moved.From = %v", rep.Moved[0].From)
	}
	if rep.Moved[0].To != [4]int{10, 5, 100, 40} {
		t.Fatalf("Moved.To = %v", rep.Moved[0].To)
	}
}

func TestManifest_AttrChanged(t *testing.T) {
	ma := makeManifest(el("a/b/btn", "button", [4]int{0, 0, 100, 40},
		map[string]any{"label": "OK", "enabled": true}))
	mb := makeManifest(el("a/b/btn", "button", [4]int{0, 0, 100, 40},
		map[string]any{"label": "Cancel", "enabled": true}))
	rep, err := visualdiff.Manifest(marshalManifest(t, ma), marshalManifest(t, mb))
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if len(rep.AttrChanged) != 1 || rep.AttrChanged[0].ID != "a/b/btn" {
		t.Fatalf("AttrChanged: got %v", rep.AttrChanged)
	}
}

func TestManifest_BadJSON(t *testing.T) {
	_, err := visualdiff.Manifest([]byte("not json"), []byte("{}"))
	if err == nil {
		t.Fatal("expected error for bad JSON in a")
	}
}

// ---------------------------------------------------------------------------
// Combined tests
// ---------------------------------------------------------------------------

func TestCombined_PixelOnly(t *testing.T) {
	same := makePNG(t, 2, color.RGBA{R: 100, A: 255})
	rep, err := visualdiff.Combined(same, same, nil, nil, visualdiff.CombinedOptions{})
	if err != nil {
		t.Fatalf("Combined: %v", err)
	}
	if rep.Tier != "pixel" {
		t.Fatalf("Tier: got %q, want pixel", rep.Tier)
	}
	if !rep.Pass {
		t.Fatalf("expected Pass=true for identical images")
	}
	if rep.ManifestDiff != nil {
		t.Fatal("expected nil ManifestDiff when no manifests provided")
	}
}

func TestCombined_WithManifest_Pass(t *testing.T) {
	same := makePNG(t, 2, color.RGBA{A: 255})
	m := marshalManifest(t, makeManifest(el("a/b/btn", "button", [4]int{0, 0, 50, 20}, nil)))
	rep, err := visualdiff.Combined(same, same, m, m, visualdiff.CombinedOptions{})
	if err != nil {
		t.Fatalf("Combined: %v", err)
	}
	if rep.Tier != "manifest+pixel" {
		t.Fatalf("Tier: got %q, want manifest+pixel", rep.Tier)
	}
	if !rep.Pass {
		t.Fatalf("expected Pass=true")
	}
}

func TestCombined_WithManifest_StructuralChange(t *testing.T) {
	same := makePNG(t, 2, color.RGBA{A: 255})
	ma := marshalManifest(t, makeManifest(el("a/b/btn", "button", [4]int{0, 0, 50, 20}, nil)))
	mb := marshalManifest(t, makeManifest(
		el("a/b/btn", "button", [4]int{0, 0, 50, 20}, nil),
		el("a/b/new", "label", [4]int{0, 30, 50, 20}, nil),
	))
	rep, err := visualdiff.Combined(same, same, ma, mb, visualdiff.CombinedOptions{})
	if err != nil {
		t.Fatalf("Combined: %v", err)
	}
	if rep.Pass {
		t.Fatal("expected Pass=false when structural change exists")
	}
	if len(rep.ManifestDiff.Added) != 1 {
		t.Fatalf("expected 1 added element, got %d", len(rep.ManifestDiff.Added))
	}
	// Regions should contain the added element's bbox.
	if len(rep.Regions) == 0 {
		t.Fatal("expected Regions to be populated for added element")
	}
}

func TestCombined_PixelTolerance(t *testing.T) {
	// Same pixel content means RMS=0 → pass even with tight tolerance.
	same := makePNG(t, 2, color.RGBA{A: 255})
	rep, err := visualdiff.Combined(same, same, nil, nil,
		visualdiff.CombinedOptions{PixelTolerance: 0.0001})
	if err != nil {
		t.Fatalf("Combined: %v", err)
	}
	if !rep.Pass {
		t.Fatal("expected pass for identical images regardless of tolerance")
	}
}

// TestCombined_VLMCalled verifies the VLM hook is invoked.
func TestCombined_VLMCalled(t *testing.T) {
	same := makePNG(t, 2, color.RGBA{A: 255})
	ma := marshalManifest(t, makeManifest(el("a/b/btn", "button", [4]int{0, 0, 50, 20}, nil)))
	mb := marshalManifest(t, makeManifest(el("a/b/btn", "button", [4]int{10, 10, 50, 20}, nil)))

	v := &fakeVLM{summary: "button moved slightly"}
	rep, err := visualdiff.Combined(same, same, ma, mb, visualdiff.CombinedOptions{VLM: v})
	if err != nil {
		t.Fatalf("Combined: %v", err)
	}
	if !v.called {
		t.Fatal("expected VLM.Describe to be called")
	}
	if rep.VLMSummary != "button moved slightly" {
		t.Fatalf("VLMSummary: got %q", rep.VLMSummary)
	}
}

type fakeVLM struct {
	called  bool
	summary string
}

func (f *fakeVLM) Describe(_, _ []byte, _ []visualdiff.Region) (string, error) {
	f.called = true
	return f.summary, nil
}

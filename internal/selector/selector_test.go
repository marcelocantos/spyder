// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package selector

import (
	"testing"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/inventory"
)

// testCandidates is a small canned set used across multiple tests.
var testCandidates = []Candidate{
	{
		Info:  device.Info{UUID: "ipad-uuid", Name: "Pippa", Platform: "ios", Model: "iPad Air", OS: "17.4"},
		Entry: inventory.Entry{Alias: "Pippa", Platform: "ios", Tags: []string{"ipad", "arm64"}},
	},
	{
		Info:  device.Info{UUID: "iphone-uuid", Name: "Mini", Platform: "ios", Model: "iPhone 15 Mini", OS: "17.2"},
		Entry: inventory.Entry{Alias: "Mini", Platform: "ios", Tags: []string{"iphone", "arm64"}},
	},
	{
		Info:  device.Info{UUID: "android-uuid", Name: "Pixel", Platform: "android", Model: "Pixel 7", OS: "34"},
		Entry: inventory.Entry{Alias: "Pixel", Platform: "android", Tags: []string{"phone", "arm64"}},
	},
}

// --- Resolve happy paths ---------------------------------------------------

func TestResolve_PlatformOnly(t *testing.T) {
	info, err := Resolve(Selector{Platform: "ios"}, testCandidates, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Platform != "ios" {
		t.Errorf("got platform %q; want ios", info.Platform)
	}
}

func TestResolve_PlatformAndModelFamily(t *testing.T) {
	info, err := Resolve(Selector{Platform: "ios", ModelFamily: "ipad"}, testCandidates, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.UUID != "ipad-uuid" {
		t.Errorf("got UUID %q; want ipad-uuid", info.UUID)
	}
}

func TestResolve_AndroidPhone(t *testing.T) {
	info, err := Resolve(Selector{Platform: "android", ModelFamily: "phone"}, testCandidates, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.UUID != "android-uuid" {
		t.Errorf("got UUID %q; want android-uuid", info.UUID)
	}
}

func TestResolve_TagSubsetMatch(t *testing.T) {
	// arm64 is present on all candidates, but model_family narrows to ipad.
	info, err := Resolve(
		Selector{Platform: "ios", ModelFamily: "ipad", Tags: []string{"arm64"}},
		testCandidates, nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.UUID != "ipad-uuid" {
		t.Errorf("got UUID %q; want ipad-uuid", info.UUID)
	}
}

func TestResolve_OSMin(t *testing.T) {
	// ipad is 17.4, iphone is 17.2; os_min=17.3 should pick ipad only.
	info, err := Resolve(
		Selector{Platform: "ios", OSMin: "17.3"},
		testCandidates, nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.UUID != "ipad-uuid" {
		t.Errorf("got UUID %q; want ipad-uuid (highest OS)", info.UUID)
	}
}

func TestResolve_OSMax(t *testing.T) {
	// os_max=17.3 excludes ipad (17.4) → should pick iphone (17.2).
	info, err := Resolve(
		Selector{Platform: "ios", OSMax: "17.3"},
		testCandidates, nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.UUID != "iphone-uuid" {
		t.Errorf("got UUID %q; want iphone-uuid", info.UUID)
	}
}

func TestResolve_Attrs(t *testing.T) {
	candidates := []Candidate{
		{
			Info:  device.Info{UUID: "dev-a", Platform: "ios", Model: "iPad Air"},
			Entry: inventory.Entry{Alias: "A", Platform: "ios", Tags: []string{"ipad"}, Attrs: map[string]string{"env": "ci"}},
		},
		{
			Info:  device.Info{UUID: "dev-b", Platform: "ios", Model: "iPad Air"},
			Entry: inventory.Entry{Alias: "B", Platform: "ios", Tags: []string{"ipad"}, Attrs: map[string]string{"env": "staging"}},
		},
	}
	info, err := Resolve(
		Selector{Platform: "ios", Attrs: map[string]string{"env": "ci"}},
		candidates, nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.UUID != "dev-a" {
		t.Errorf("got UUID %q; want dev-a", info.UUID)
	}
}

// --- Resolve: reserved device is skipped ----------------------------------

func TestResolve_SkipsReservedDevice(t *testing.T) {
	candidates := []Candidate{
		{
			Info:       device.Info{UUID: "ipad-held", Platform: "ios", Model: "iPad"},
			Entry:      inventory.Entry{Alias: "HeldPad", Platform: "ios"},
			IsReserved: true,
		},
		{
			Info:  device.Info{UUID: "ipad-free", Platform: "ios", Model: "iPad Air"},
			Entry: inventory.Entry{Alias: "FreePad", Platform: "ios"},
		},
	}
	info, err := Resolve(Selector{Platform: "ios"}, candidates, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.UUID != "ipad-free" {
		t.Errorf("got UUID %q; want ipad-free (held device skipped)", info.UUID)
	}
}

// --- Resolve: no match → NoMatchError with near-misses -------------------

func TestResolve_NoMatch_ReturnsNearMisses(t *testing.T) {
	_, err := Resolve(
		Selector{Platform: "ios", ModelFamily: "watch"}, // no watches in our set
		testCandidates, nil,
	)
	if err == nil {
		t.Fatal("expected NoMatchError, got nil")
	}
	nme, ok := err.(*NoMatchError)
	if !ok {
		t.Fatalf("expected *NoMatchError, got %T: %v", err, err)
	}
	if len(nme.NearMisses) == 0 {
		t.Error("expected at least one near-miss")
	}
	for _, nm := range nme.NearMisses {
		if nm.FailedPredicate == "" {
			t.Error("near-miss should have a non-empty FailedPredicate")
		}
	}
	// Error message should be non-empty and mention the selector.
	msg := nme.Error()
	if msg == "" {
		t.Error("Error() is empty")
	}
}

func TestResolve_NoCandidatesAtAll(t *testing.T) {
	_, err := Resolve(Selector{Platform: "ios"}, nil, nil)
	if err == nil {
		t.Fatal("expected error when no candidates given")
	}
	nme, ok := err.(*NoMatchError)
	if !ok {
		t.Fatalf("expected *NoMatchError, got %T", err)
	}
	if len(nme.NearMisses) != 0 {
		t.Errorf("no candidates → no near-misses; got %d", len(nme.NearMisses))
	}
}

// --- Pool resolver hook ---------------------------------------------------

type stubPool struct {
	uuid string
	err  error
}

func (p *stubPool) Resolve(_ Selector) (string, error) { return p.uuid, p.err }

func TestResolve_UsesPoolWhenNoPhysicalMatch(t *testing.T) {
	// All physical candidates are android; selector is ios → no match → pool consulted.
	android := []Candidate{
		{Info: device.Info{UUID: "android-only", Platform: "android", Model: "Pixel"}},
	}
	pool := &stubPool{uuid: "pool-sim-uuid"}
	info, err := Resolve(Selector{Platform: "ios"}, android, pool)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.UUID != "pool-sim-uuid" {
		t.Errorf("got UUID %q; want pool-sim-uuid", info.UUID)
	}
}

func TestResolve_PoolReturnsEmptyMeansNoMatch(t *testing.T) {
	pool := &stubPool{uuid: ""} // pool has nothing
	_, err := Resolve(Selector{Platform: "ios"}, nil, pool)
	if err == nil {
		t.Fatal("expected NoMatchError when pool returns empty UUID")
	}
}

// --- Orientation-capable predicate ----------------------------------------

func TestResolve_OrientationCapable_PhysicalExcluded(t *testing.T) {
	candidates := []Candidate{
		{
			Info:       device.Info{UUID: "physical-ipad", Platform: "ios", Model: "iPad"},
			Entry:      inventory.Entry{Platform: "ios"},
			IsSimOrEmu: false,
		},
		{
			Info:       device.Info{UUID: "sim-ipad", Platform: "ios", Model: "iPad Simulator"},
			Entry:      inventory.Entry{Platform: "ios"},
			IsSimOrEmu: true,
		},
	}
	info, err := Resolve(Selector{Platform: "ios", OrientationCapable: true}, candidates, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.UUID != "sim-ipad" {
		t.Errorf("got UUID %q; want sim-ipad (physical excluded by OrientationCapable)", info.UUID)
	}
}

// --- compareVersion -------------------------------------------------------

func TestCompareVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"17.4", "17.4", 0},
		{"17.4", "17.3", 1},
		{"17.2", "17.3", -1},
		{"17.10", "17.9", 1}, // numeric: 10 > 9
		{"34", "33", 1},
		{"34", "34.0", 0},
		{"1.0.0", "1.0", 0},
		{"2.0", "1.9", 1},
		{"10.0", "9.9", 1},
	}
	for _, c := range cases {
		got := compareVersion(c.a, c.b)
		if got != c.want {
			t.Errorf("compareVersion(%q, %q) = %d; want %d", c.a, c.b, got, c.want)
		}
	}
}

// --- Selector.String() readability ----------------------------------------

func TestSelector_String(t *testing.T) {
	s := Selector{
		Platform:    "ios",
		ModelFamily: "ipad",
		Tags:        []string{"arm64"},
	}
	str := s.String()
	if str == "" {
		t.Error("String() is empty")
	}
	for _, want := range []string{"platform=ios", "model_family=ipad", "tag=arm64"} {
		if !contains(str, want) {
			t.Errorf("String() = %q; missing %q", str, want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := range s {
		if i+len(sub) <= len(s) && s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

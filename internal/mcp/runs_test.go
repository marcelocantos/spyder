// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/reservations"
	"github.com/marcelocantos/spyder/internal/runs"
)

// newHandlerWithRuns wires a Handler with an in-memory reservation
// store and a tempdir-backed runs store, so the end-to-end artefact
// path can be exercised without touching the user's ~/.spyder.
func newHandlerWithRuns(t *testing.T, ios, android device.Adapter) (*Handler, *reservations.Store, *runs.Store) {
	t.Helper()
	resv, err := reservations.New("")
	if err != nil {
		t.Fatalf("reservations.New: %v", err)
	}
	r, err := runs.New(t.TempDir())
	if err != nil {
		t.Fatalf("runs.New: %v", err)
	}
	h := newTestHandler(t)
	if ios != nil {
		h.ios = ios
	}
	if android != nil {
		h.android = android
	}
	h.reservations = resv
	h.runs = r
	return h, resv, r
}

func TestReserve_OpensRun(t *testing.T) {
	h, _, r := newHandlerWithRuns(t, nil, nil)

	res := dispatchJSON(t, h, "reserve", map[string]any{
		"device": "Pippa", "owner": "tiltbuggy", "note": "ui sweep",
	})
	if res.IsError {
		t.Fatalf("reserve: %s", resultText(t, &res))
	}

	active, err := r.Active("Pippa", "tiltbuggy")
	if err != nil {
		t.Fatalf("runs.Active: %v", err)
	}
	if active == nil {
		t.Fatal("no active run after reserve")
	}
	if active.Device != "Pippa" || active.Owner != "tiltbuggy" || active.Note != "ui sweep" {
		t.Errorf("unexpected run: %+v", active)
	}
}

func TestReserve_SameOwnerReuseDoesNotStackRuns(t *testing.T) {
	h, _, r := newHandlerWithRuns(t, nil, nil)

	_ = dispatchJSON(t, h, "reserve", map[string]any{"device": "Pippa", "owner": "tiltbuggy"})
	_ = dispatchJSON(t, h, "reserve", map[string]any{"device": "Pippa", "owner": "tiltbuggy"})

	list, err := r.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var active []runs.Run
	for _, run := range list {
		if run.ClosedAt == nil && run.Device == "Pippa" && run.Owner == "tiltbuggy" {
			active = append(active, run)
		}
	}
	if len(active) != 1 {
		t.Errorf("expected exactly 1 active run; got %d", len(active))
	}
}

func TestRelease_ClosesRun(t *testing.T) {
	h, _, r := newHandlerWithRuns(t, nil, nil)
	_ = dispatchJSON(t, h, "reserve", map[string]any{"device": "Pippa", "owner": "tiltbuggy"})

	res := dispatchJSON(t, h, "release", map[string]any{"device": "Pippa", "owner": "tiltbuggy"})
	if res.IsError {
		t.Fatalf("release: %s", resultText(t, &res))
	}

	active, _ := r.Active("Pippa", "tiltbuggy")
	if active != nil {
		t.Errorf("run still active after release: %+v", active)
	}
}

func TestScreenshot_ArchivedInActiveRun(t *testing.T) {
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x42, 0x42, 0x42, 0x42}
	ios := &stubAdapter{screenshot: func(id string) ([]byte, error) { return png, nil }}
	h, _, r := newHandlerWithRuns(t, ios, nil)
	h.tunneld = &stubTunneld{}

	_ = dispatchJSON(t, h, "reserve", map[string]any{"device": "Pippa", "owner": "tiltbuggy"})

	res := dispatchJSON(t, h, "screenshot", map[string]any{
		"device": "Pippa", "owner": "tiltbuggy",
	})
	if res.IsError {
		t.Fatalf("screenshot: %s", resultText(t, &res))
	}
	// Still returns the image inline.
	var foundImage bool
	for _, c := range res.Content {
		if c.Type == "image" {
			foundImage = true
			// Base64 decodes back to the original PNG bytes.
			dec, err := base64.StdEncoding.DecodeString(c.Data)
			if err != nil {
				t.Fatalf("decode b64: %v", err)
			}
			if string(dec) != string(png) {
				t.Errorf("inline image bytes differ from source")
			}
		}
	}
	if !foundImage {
		t.Fatal("no image content block in screenshot response")
	}

	// Manifest captured the artefact.
	active, err := r.Active("Pippa", "tiltbuggy")
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if active == nil {
		t.Fatal("active run missing")
	}
	if len(active.Artefacts) != 1 {
		t.Fatalf("expected 1 artefact; got %d", len(active.Artefacts))
	}
	a := active.Artefacts[0]
	if a.Source != "screenshot" || a.MIMEType != "image/png" || a.Size != int64(len(png)) {
		t.Errorf("unexpected artefact: %+v", a)
	}
	if !strings.HasPrefix(a.Name, "screenshot-") || !strings.HasSuffix(a.Name, ".png") {
		t.Errorf("artefact name %q doesn't match screenshot-*.png", a.Name)
	}
}

func TestScreenshot_NoRunStore_StillReturnsImage(t *testing.T) {
	// No runs store wired → screenshot path still works, just doesn't
	// archive. This protects the "runs is optional" contract.
	png := []byte{0x89, 0x50, 0x4e, 0x47}
	ios := &stubAdapter{screenshot: func(id string) ([]byte, error) { return png, nil }}
	h := newHandlerWithStubs(t, ios, nil, &stubTunneld{})

	res := dispatchJSON(t, h, "screenshot", map[string]any{"device": "Pippa"})
	if res.IsError {
		t.Fatalf("screenshot should succeed without runs store; body=%s", resultText(t, &res))
	}
}

func TestScreenshot_NoActiveRun_NotArchived(t *testing.T) {
	png := []byte{0x89, 0x50, 0x4e, 0x47}
	ios := &stubAdapter{screenshot: func(id string) ([]byte, error) { return png, nil }}
	h, _, r := newHandlerWithRuns(t, ios, nil)
	h.tunneld = &stubTunneld{}

	res := dispatchJSON(t, h, "screenshot", map[string]any{"device": "Pippa"})
	if res.IsError {
		t.Fatalf("screenshot should succeed without reservation; body=%s", resultText(t, &res))
	}
	list, _ := r.List()
	if len(list) != 0 {
		t.Errorf("no reservation → no run; got %d runs", len(list))
	}
}

func TestRunsList_RoundTripsManifest(t *testing.T) {
	h, _, _ := newHandlerWithRuns(t, nil, nil)
	_ = dispatchJSON(t, h, "reserve", map[string]any{"device": "Pippa", "owner": "tiltbuggy", "note": "sweep"})

	res := dispatchJSON(t, h, "runs_list", map[string]any{})
	if res.IsError {
		t.Fatalf("runs_list: %s", resultText(t, &res))
	}
	var list []runs.Run
	if err := json.Unmarshal([]byte(resultText(t, &res)), &list); err != nil {
		t.Fatalf("parse list: %v", err)
	}
	if len(list) != 1 || list[0].Owner != "tiltbuggy" || list[0].Note != "sweep" {
		t.Errorf("unexpected list: %+v", list)
	}
}

func TestRunsShow_RequiresRunID(t *testing.T) {
	h, _, _ := newHandlerWithRuns(t, nil, nil)
	_, err := h.Dispatch("runs_show", map[string]any{})
	if err == nil {
		t.Error("runs_show without run_id should error")
	}
}

func TestRunsShow_UnknownID_Error(t *testing.T) {
	h, _, _ := newHandlerWithRuns(t, nil, nil)
	res := dispatchJSON(t, h, "runs_show", map[string]any{"run_id": "20000101-000000-deadbe"})
	if !res.IsError {
		t.Error("runs_show for missing id should be an error")
	}
}

func TestRunsList_NoStore_EmptyList(t *testing.T) {
	h := newTestHandler(t) // no runs store
	res := dispatchJSON(t, h, "runs_list", map[string]any{})
	if res.IsError {
		t.Fatalf("runs_list without store should return empty; body=%s", resultText(t, &res))
	}
	if !strings.Contains(resultText(t, &res), "[]") {
		t.Errorf("expected empty list; got %s", resultText(t, &res))
	}
}

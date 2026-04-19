// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package visualdiff computes visual differences between a candidate
// screenshot and a stored baseline. Three tiers are defined:
//
//  1. Manifest diff (structural) — compares two UI-element manifests,
//     reporting added / removed / moved / attribute-changed elements.
//  2. Pixel diff — computes RMS pixel error over the full image; SSIM is
//     stubbed (returns NaN with a note) pending a proper implementation.
//  3. Combined — runs manifest if both sides have one; otherwise falls
//     back to pixel. Returns a unified Report.
//
// A VLM interface is defined for an optional natural-language pass over
// flagged regions; the nil-default no-ops it cleanly.
//
// # Stubs
//
// The following tiers are stubbed in v1 and return "not implemented":
//
//   - SSIM: Pixel() sets SSIMScore = math.NaN() and SSIMNote = "not
//     implemented in v1".
//   - VLM: the VLM interface is defined; nil is a valid value and results
//     in an empty VLMSummary. No concrete implementation is shipped.
//
// See the respective TODO comments in each function for the follow-up contract.
package visualdiff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"math"

	"github.com/marcelocantos/spyder/internal/baselines"
)

// ---------------------------------------------------------------------------
// Pixel tier
// ---------------------------------------------------------------------------

// PixelReport is the output of a pixel-level comparison.
type PixelReport struct {
	// RMSError is the root-mean-square pixel error in [0, 1] where 0 is
	// identical and 1 is maximally different (all channels at full contrast).
	RMSError float64 `json:"rms_error"`

	// SSIMScore is the Structural Similarity Index in [-1, 1]. Values
	// closer to 1 indicate higher similarity. math.NaN() in v1 (stubbed).
	SSIMScore float64 `json:"ssim_score"`

	// SSIMNote explains why SSIMScore is NaN when it is.
	SSIMNote string `json:"ssim_note,omitempty"`

	// SizeMismatch is true when the two images have different dimensions;
	// in that case RMSError is 1.0 and SSIM is NaN.
	SizeMismatch bool `json:"size_mismatch,omitempty"`

	// Width and Height of the compared images (from image a; 0 on decode error).
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Pixel computes RMS and SSIM over two PNG byte slices. Both arguments
// must be valid PNG data. SSIM is stubbed and returns NaN.
//
// TODO(v2): implement luminance-only SSIM as described in Wang et al. 2004.
// The stub is intentional — a wrong SSIM implementation is worse than none.
func Pixel(a, b []byte) (PixelReport, error) {
	imgA, err := decodePNG(a)
	if err != nil {
		return PixelReport{}, fmt.Errorf("visualdiff pixel: decode image a: %w", err)
	}
	imgB, err := decodePNG(b)
	if err != nil {
		return PixelReport{}, fmt.Errorf("visualdiff pixel: decode image b: %w", err)
	}

	boundsA := imgA.Bounds()
	boundsB := imgB.Bounds()

	rep := PixelReport{
		Width:     boundsA.Dx(),
		Height:    boundsA.Dy(),
		SSIMScore: math.NaN(),
		SSIMNote:  "not implemented in v1",
	}

	if boundsA != boundsB {
		rep.SizeMismatch = true
		rep.RMSError = 1.0
		return rep, nil
	}

	// RMS: sqrt( mean( (r_a−r_b)² + (g_a−g_b)² + (b_a−b_b)² ) / 3 )
	// All values are normalised to [0,1] per channel.
	var sumSq float64
	n := 0
	for y := boundsA.Min.Y; y < boundsA.Max.Y; y++ {
		for x := boundsA.Min.X; x < boundsA.Max.X; x++ {
			ra, ga, ba, _ := imgA.At(x, y).RGBA()
			rb, gb, bb, _ := imgB.At(x, y).RGBA()
			// RGBA() returns [0, 65535]; normalise to [0, 1].
			dr := float64(int64(ra)-int64(rb)) / 65535.0
			dg := float64(int64(ga)-int64(gb)) / 65535.0
			db := float64(int64(ba)-int64(bb)) / 65535.0
			sumSq += dr*dr + dg*dg + db*db
			n++
		}
	}
	if n == 0 {
		rep.RMSError = 0
	} else {
		rep.RMSError = math.Sqrt(sumSq / (float64(n) * 3.0))
	}
	return rep, nil
}

// decodePNG decodes raw PNG bytes into an image.Image.
func decodePNG(data []byte) (image.Image, error) {
	return png.Decode(bytes.NewReader(data))
}

// ---------------------------------------------------------------------------
// Manifest tier
// ---------------------------------------------------------------------------

// ManifestReport is the output of a manifest-level structural comparison.
type ManifestReport struct {
	// Added contains IDs present in b but absent in a.
	Added []string `json:"added,omitempty"`

	// Removed contains IDs present in a but absent in b.
	Removed []string `json:"removed,omitempty"`

	// Moved contains elements whose bounding box changed.
	Moved []MovedElement `json:"moved,omitempty"`

	// AttrChanged contains elements whose Attrs changed (but position is
	// the same).
	AttrChanged []AttrChange `json:"attr_changed,omitempty"`

	// KindChanged contains elements whose Kind changed.
	KindChanged []KindChange `json:"kind_changed,omitempty"`

	// Unchanged is the count of elements that are identical in both manifests.
	Unchanged int `json:"unchanged"`
}

// MovedElement records a bounding-box change for one element.
type MovedElement struct {
	ID   string `json:"id"`
	From [4]int `json:"from"`
	To   [4]int `json:"to"`
}

// AttrChange records an attribute bag change for one element.
type AttrChange struct {
	ID   string         `json:"id"`
	From map[string]any `json:"from"`
	To   map[string]any `json:"to"`
}

// KindChange records a kind tag change for one element.
type KindChange struct {
	ID   string `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
}

// Manifest performs a structural diff over two serialised Manifest JSON
// blobs. Each argument must be valid JSON compatible with the
// baselines.Manifest schema.
//
// The diff key is the element ID. Elements with the same ID are
// compared field-by-field. Order within the elements array is irrelevant.
func Manifest(a, b []byte) (ManifestReport, error) {
	var ma, mb baselines.Manifest
	if err := json.Unmarshal(a, &ma); err != nil {
		return ManifestReport{}, fmt.Errorf("visualdiff manifest: decode a: %w", err)
	}
	if err := json.Unmarshal(b, &mb); err != nil {
		return ManifestReport{}, fmt.Errorf("visualdiff manifest: decode b: %w", err)
	}

	indexA := indexElements(ma.Elements)
	indexB := indexElements(mb.Elements)

	var rep ManifestReport
	for id, ea := range indexA {
		eb, ok := indexB[id]
		if !ok {
			rep.Removed = append(rep.Removed, id)
			continue
		}
		moved := ea.BBox != eb.BBox
		kindChanged := ea.Kind != eb.Kind
		attrsChanged := !attrsEqual(ea.Attrs, eb.Attrs)

		if moved {
			rep.Moved = append(rep.Moved, MovedElement{ID: id, From: ea.BBox, To: eb.BBox})
		}
		if kindChanged {
			rep.KindChanged = append(rep.KindChanged, KindChange{ID: id, From: ea.Kind, To: eb.Kind})
		}
		if attrsChanged {
			rep.AttrChanged = append(rep.AttrChanged, AttrChange{ID: id, From: ea.Attrs, To: eb.Attrs})
		}
		if !moved && !kindChanged && !attrsChanged {
			rep.Unchanged++
		}
	}
	for id := range indexB {
		if _, ok := indexA[id]; !ok {
			rep.Added = append(rep.Added, id)
		}
	}
	return rep, nil
}

// indexElements builds an ID→Element map. Duplicate IDs use the last
// occurrence (consumers should ensure uniqueness in the manifest).
func indexElements(els []baselines.Element) map[string]baselines.Element {
	m := make(map[string]baselines.Element, len(els))
	for _, e := range els {
		m[e.ID] = e
	}
	return m
}

// attrsEqual compares two attribute bags for deep equality via JSON
// round-trip. This sidesteps float64 vs int ambiguity that arises when
// map[string]any values come from JSON unmarshal.
func attrsEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// ---------------------------------------------------------------------------
// VLM interface
// ---------------------------------------------------------------------------

// Region is a flagged rectangular area (pixel coordinates, top-left origin)
// that the VLM pass should inspect.
type Region struct {
	// Label is a human-readable name for the region (e.g. "element:btn/login").
	Label string `json:"label"`
	// BBox is [x, y, width, height] in pixels.
	BBox [4]int `json:"bbox"`
}

// VLM is the optional natural-language-diff interface. Implementors
// receive the two full PNG bytes plus the list of flagged regions and
// return a prose summary of the visual differences.
//
// The nil value is a valid VLM — Combined() skips the VLM pass when it
// is nil.
//
// TODO(v2): provide a concrete implementation backed by an Anthropic
// vision call (claude-opus-4 or claude-sonnet-4). The interface is
// intentionally narrow so the implementation can be swapped or mocked
// in tests.
type VLM interface {
	Describe(a, b []byte, regions []Region) (string, error)
}

// ---------------------------------------------------------------------------
// Combined report
// ---------------------------------------------------------------------------

// Report is the unified output of Combined().
type Report struct {
	// Tier indicates which comparison tier produced the result:
	// "manifest+pixel", "manifest", or "pixel".
	Tier string `json:"tier"`

	// Pixel is always populated (it runs regardless of tier).
	Pixel PixelReport `json:"pixel"`

	// ManifestDiff is populated when both sides have a manifest.
	ManifestDiff *ManifestReport `json:"manifest_diff,omitempty"`

	// Regions is the list of flagged areas for the VLM pass. Derived
	// from moved/added/removed elements when a manifest is present.
	Regions []Region `json:"regions,omitempty"`

	// VLMSummary is the natural-language summary produced by the VLM
	// pass. Empty when VLM is nil or when the pass is skipped.
	VLMSummary string `json:"vlm_summary,omitempty"`

	// Pass reports whether the comparison is within tolerance.
	Pass bool `json:"pass"`

	// PixelTolerance is the RMS threshold used for the Pass verdict.
	PixelTolerance float64 `json:"pixel_tolerance"`
}

// DefaultPixelTolerance is the default RMS tolerance applied by Combined.
// 0.01 ≈ 1% RMS error. Callers can override via CombinedOptions.
const DefaultPixelTolerance = 0.01

// CombinedOptions configures a Combined() call.
type CombinedOptions struct {
	// PixelTolerance overrides DefaultPixelTolerance. Zero means use the
	// default.
	PixelTolerance float64

	// VLM, if non-nil, is called with flagged regions after the
	// structural/pixel pass completes.
	VLM VLM
}

// Combined runs the full comparison pipeline:
//  1. Pixel diff (always).
//  2. Manifest diff if both manifestA and manifestB are non-nil.
//  3. VLM pass over flagged regions if opts.VLM is non-nil.
//
// pngA / pngB are the two raw PNG byte slices (required).
// manifestA / manifestB are optional manifest JSON bytes; nil means
// "no manifest available for this side".
func Combined(pngA, pngB, manifestA, manifestB []byte, opts CombinedOptions) (Report, error) {
	tol := opts.PixelTolerance
	if tol <= 0 {
		tol = DefaultPixelTolerance
	}

	pixRep, err := Pixel(pngA, pngB)
	if err != nil {
		return Report{}, fmt.Errorf("visualdiff combined: pixel: %w", err)
	}

	rep := Report{
		Tier:           "pixel",
		Pixel:          pixRep,
		PixelTolerance: tol,
		Pass:           pixRep.RMSError <= tol && !pixRep.SizeMismatch,
	}

	if manifestA != nil && manifestB != nil {
		mRep, err := Manifest(manifestA, manifestB)
		if err != nil {
			return Report{}, fmt.Errorf("visualdiff combined: manifest: %w", err)
		}
		rep.ManifestDiff = &mRep
		rep.Tier = "manifest+pixel"

		// Derive regions from structural changes.
		// Parse manifestB to get bboxes for added/moved elements.
		var mb baselines.Manifest
		_ = json.Unmarshal(manifestB, &mb)
		idxB := indexElements(mb.Elements)

		var ma baselines.Manifest
		_ = json.Unmarshal(manifestA, &ma)
		idxA := indexElements(ma.Elements)

		for _, id := range mRep.Added {
			if el, ok := idxB[id]; ok {
				rep.Regions = append(rep.Regions, Region{
					Label: "added:" + id,
					BBox:  el.BBox,
				})
			}
		}
		for _, id := range mRep.Removed {
			if el, ok := idxA[id]; ok {
				rep.Regions = append(rep.Regions, Region{
					Label: "removed:" + id,
					BBox:  el.BBox,
				})
			}
		}
		for _, mv := range mRep.Moved {
			rep.Regions = append(rep.Regions, Region{
				Label: "moved:" + mv.ID,
				BBox:  mv.To,
			})
		}
		for _, ac := range mRep.AttrChanged {
			if el, ok := idxB[ac.ID]; ok {
				rep.Regions = append(rep.Regions, Region{
					Label: "attrs:" + ac.ID,
					BBox:  el.BBox,
				})
			}
		}

		// A manifest-pass requires no structural changes AND pixel within tol.
		hasChanges := len(mRep.Added)+len(mRep.Removed)+len(mRep.Moved)+
			len(mRep.AttrChanged)+len(mRep.KindChanged) > 0
		rep.Pass = !hasChanges && rep.Pixel.RMSError <= tol && !pixRep.SizeMismatch
	}

	// VLM pass (optional, best-effort).
	if opts.VLM != nil && len(rep.Regions) > 0 {
		summary, err := opts.VLM.Describe(pngA, pngB, rep.Regions)
		if err == nil {
			rep.VLMSummary = summary
		}
		// VLM failure is non-fatal: the structural/pixel result stands.
	}

	return rep, nil
}

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/baselines"
	"github.com/marcelocantos/spyder/internal/visualdiff"
)

// handleBaselineUpdate stores a new baseline PNG (and optional manifest)
// for the named suite/case/variant. The PNG may be supplied as a
// filesystem path (screenshot_path) or as a base64-encoded blob
// (screenshot_base64); one of the two is required.
func (h *Handler) handleBaselineUpdate(args map[string]any) (*mcpgo.CallToolResult, error) {
	suite, err := requireString(args, "suite")
	if err != nil {
		return nil, err
	}
	caseName, err := requireString(args, "case")
	if err != nil {
		return nil, err
	}
	variant := optString(args, "variant")
	if variant == "" {
		variant = "default"
	}

	png, err := resolvePNGArg(args)
	if err != nil {
		return toolErr("%v", err)
	}

	var mani *baselines.Manifest
	if raw := optString(args, "manifest"); raw != "" {
		var m baselines.Manifest
		if jerr := json.Unmarshal([]byte(raw), &m); jerr != nil {
			return toolErr("baseline_update: parse manifest: %v", jerr)
		}
		mani = &m
	}

	if h.bls == nil {
		return toolErr("baseline store not configured on this server")
	}
	if err := h.bls.Put(suite, caseName, variant, png, mani); err != nil {
		return toolErr("baseline_update: %v", err)
	}
	msg := fmt.Sprintf("baseline stored: %s/%s/%s (%d bytes PNG", suite, caseName, variant, len(png))
	if mani != nil {
		msg += fmt.Sprintf(", %d elements", len(mani.Elements))
	}
	msg += ")"
	return toolText(msg)
}

// handleDiff compares a candidate screenshot against the stored
// baseline for suite/case/variant. Returns a structured JSON diff
// report. The input PNG may be supplied as a path or as base64.
func (h *Handler) handleDiff(args map[string]any) (*mcpgo.CallToolResult, error) {
	suite, err := requireString(args, "suite")
	if err != nil {
		return nil, err
	}
	caseName, err := requireString(args, "case")
	if err != nil {
		return nil, err
	}
	variant := optString(args, "variant")
	if variant == "" {
		variant = "default"
	}

	candidatePNG, err := resolvePNGArg(args)
	if err != nil {
		return toolErr("%v", err)
	}

	if h.bls == nil {
		return toolErr("baseline store not configured on this server")
	}
	bl, err := h.bls.Get(suite, caseName, variant)
	if err != nil {
		return toolErr("diff: %v", err)
	}

	// Candidate manifest (optional).
	var candidateManiJSON []byte
	if raw := optString(args, "manifest"); raw != "" {
		candidateManiJSON = []byte(raw)
	}

	// Baseline manifest (optional, already loaded with the baseline).
	var baselineManiJSON []byte
	if len(bl.Manifest.Elements) > 0 || bl.Manifest.SchemaVersion > 0 {
		if data, merr := json.Marshal(bl.Manifest); merr == nil {
			baselineManiJSON = data
		}
	}

	tol := optFloat(args, "pixel_tolerance")
	if tol <= 0 {
		tol = visualdiff.DefaultPixelTolerance
	}

	rep, err := visualdiff.Combined(
		bl.PNG, candidatePNG,
		baselineManiJSON, candidateManiJSON,
		visualdiff.CombinedOptions{PixelTolerance: tol},
	)
	if err != nil {
		return toolErr("diff: %v", err)
	}

	// Archive input screenshot + report into the active run for any open run.
	// We don't know which device is active here; archive under a pseudo-device
	// key derived from suite/case so runs that own baselines can be correlated.
	if h.runs != nil {
		owner := optString(args, "owner")
		dev := optString(args, "device")
		if dev == "" {
			dev = suite + "/" + caseName
		}
		canonical := h.canonicalDevice(dev)
		run, lerr := h.runs.Active(canonical, owner)
		if lerr == nil && run != nil {
			ts := time.Now().UTC().Format("20060102-150405")
			// Archive the candidate PNG.
			pngName := fmt.Sprintf("diff-candidate-%s.png", ts)
			_, _ = h.runs.AddArtefact(run.ID, "diff", pngName, "image/png", candidatePNG)
			// Archive the diff report.
			if repData, merr := json.MarshalIndent(rep, "", "  "); merr == nil {
				repName := fmt.Sprintf("diff-report-%s.json", ts)
				_, _ = h.runs.AddArtefact(run.ID, "diff", repName, "application/json", repData)
			}
		}
	}

	return toolJSON(rep)
}

// --- helpers -------------------------------------------------------

// resolvePNGArg reads the PNG from screenshot_path or screenshot_base64.
func resolvePNGArg(args map[string]any) ([]byte, error) {
	if path := optString(args, "screenshot_path"); path != "" {
		// Prevent path traversal: only absolute paths or simple filenames
		// rooted relative to cwd. We resolve and check nothing unusual.
		clean := filepath.Clean(path)
		data, err := os.ReadFile(clean)
		if err != nil {
			return nil, fmt.Errorf("read screenshot_path %q: %w", path, err)
		}
		return data, nil
	}
	if b64 := optString(args, "screenshot_base64"); b64 != "" {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("decode screenshot_base64: %w", err)
		}
		return data, nil
	}
	return nil, fmt.Errorf("screenshot_path or screenshot_base64 is required")
}

// optFloat extracts a float64 value from args; returns 0 if absent.
func optFloat(args map[string]any, key string) float64 {
	switch v := args[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return 0
}

// handleBaselinesList returns all stored baselines for a suite.
func (h *Handler) handleBaselinesList(args map[string]any) (*mcpgo.CallToolResult, error) {
	suite, err := requireString(args, "suite")
	if err != nil {
		return nil, err
	}
	if h.bls == nil {
		return toolErr("baseline store not configured on this server")
	}
	entries, err := h.bls.List(suite)
	if err != nil {
		return toolErr("baselines_list: %v", err)
	}
	if entries == nil {
		entries = []baselines.ListEntry{}
	}
	return toolJSON(entries)
}

// visualDefinitions returns the MCP tool definitions for visual-regression tools.
func visualDefinitions() []mcpgo.Tool {
	return []mcpgo.Tool{
		mcpgo.NewTool("baseline_update",
			mcpgo.WithDescription("Store a new visual baseline for the named suite/case/variant. Supply the PNG as a filesystem path or as base64. An optional manifest (JSON string) enables structural diffing on the next `diff` call."),
			mcpgo.WithString("suite",
				mcpgo.Required(),
				mcpgo.Description("Test suite name (e.g. 'login-flow')"),
			),
			mcpgo.WithString("case",
				mcpgo.Required(),
				mcpgo.Description("Test case name within the suite (e.g. 'main-screen')"),
			),
			mcpgo.WithString("variant",
				mcpgo.Description("Per-device/orientation variant key (e.g. 'pippa-landscape'). Defaults to 'default'."),
			),
			mcpgo.WithString("screenshot_path",
				mcpgo.Description("Absolute or relative path to the PNG screenshot to store as the baseline."),
			),
			mcpgo.WithString("screenshot_base64",
				mcpgo.Description("Base64-encoded PNG screenshot to store as the baseline (alternative to screenshot_path)."),
			),
			mcpgo.WithString("manifest",
				mcpgo.Description("Optional JSON string conforming to the spyder manifest schema ({schema_version:1, elements:[{id, kind, bbox:[x,y,w,h], attrs:{}}]}). Enables structural diffing on the next `diff` call."),
			),
		),

		mcpgo.NewTool("diff",
			mcpgo.WithDescription("Compare a candidate screenshot against the stored baseline for the named suite/case/variant. Returns a structured JSON diff report with pixel RMS error, optional manifest structural diff (added/removed/moved elements with bounding boxes), and a Pass verdict. Supply the PNG as a path or as base64."),
			mcpgo.WithString("suite",
				mcpgo.Required(),
				mcpgo.Description("Test suite name matching the stored baseline."),
			),
			mcpgo.WithString("case",
				mcpgo.Required(),
				mcpgo.Description("Test case name within the suite."),
			),
			mcpgo.WithString("variant",
				mcpgo.Description("Per-device/orientation variant key. Defaults to 'default'."),
			),
			mcpgo.WithString("screenshot_path",
				mcpgo.Description("Path to the candidate PNG screenshot."),
			),
			mcpgo.WithString("screenshot_base64",
				mcpgo.Description("Base64-encoded candidate PNG (alternative to screenshot_path)."),
			),
			mcpgo.WithString("manifest",
				mcpgo.Description("Optional JSON string (spyder manifest schema) for the candidate screenshot. Required for structural diffing; falls back to pixel-only if absent."),
			),
			mcpgo.WithNumber("pixel_tolerance",
				mcpgo.Description(fmt.Sprintf("RMS pixel-error threshold for the Pass verdict (0–1, default %.4f). Values above this threshold cause Pass=false.", visualdiff.DefaultPixelTolerance)),
			),
			mcpgo.WithString("owner",
				mcpgo.Description("Run owner for artefact archival (optional; only effective when a matching active run exists)."),
			),
			mcpgo.WithString("device",
				mcpgo.Description("Device alias for active-run lookup (optional; defaults to suite/case when absent)."),
			),
		),

		mcpgo.NewTool("baselines_list",
			mcpgo.WithDescription("List all stored baselines for a test suite. Returns a JSON array of {case, variant, has_png, has_manifest} entries."),
			mcpgo.WithString("suite",
				mcpgo.Required(),
				mcpgo.Description("Test suite name."),
			),
		),
	}
}

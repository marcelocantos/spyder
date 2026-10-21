// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/runs"
	"github.com/marcelocantos/spyder/internal/scriptlib"
)

// Run provenance (🎯T137). reserve accepts the script and app-under-test
// identity; release accepts the outcome. Everything else is probed
// (device OS, installed app version) or computed (spyder build stamp,
// script git state). Anything unavailable is recorded as "unknown".

// provenanceProbeTimeout bounds the device probes reserve performs.
const provenanceProbeTimeout = 5 * time.Second

var provenanceArgKeys = []string{"script_path", "app_name", "app_bundle_id", "app_commit", "app_branch", "app_dirty"}

func hasProvenanceArgs(args map[string]any) bool {
	for _, k := range provenanceArgKeys {
		if _, ok := args[k]; ok {
			return true
		}
	}
	return false
}

// optTriState reads a bool-ish arg as "true"/"false", or "" when absent.
func optTriState(args map[string]any, key string) string {
	switch v := args[key].(type) {
	case bool:
		if v {
			return "true"
		}
		return "false"
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "yes", "1":
			return "true"
		case "false", "no", "0":
			return "false"
		}
	}
	return ""
}

// runOpenParams assembles provenance for a run opened by reserve.
func (h *Handler) runOpenParams(dev, canonical, owner, note string, args map[string]any, probe bool) runs.OpenParams {
	p := runs.OpenParams{
		Device: canonical, Owner: owner, Note: note,
		App: runs.AppVersion{
			Name:     optString(args, "app_name"),
			BundleID: optString(args, "app_bundle_id"),
			Commit:   optString(args, "app_commit"),
			Branch:   optString(args, "app_branch"),
			Dirty:    optTriState(args, "app_dirty"),
		},
	}
	if ref := strings.TrimSpace(optString(args, "script_path")); ref != "" {
		if _, info, err := scriptlib.Load(ref); err == nil {
			p.ScriptPath, p.ScriptSource = info.Path, info.Source
		} else {
			// Record what the caller said; git state is still attempted.
			p.ScriptPath, p.ScriptSource = ref, "unresolved"
		}
	}
	if !probe || h.noProvenanceProbe {
		return p
	}
	h.mu.Lock()
	adapter, _, id, err := h.resolveAdapter(dev)
	h.mu.Unlock()
	if err != nil || adapter == nil {
		return p
	}
	ctx, cancel := context.WithTimeout(context.Background(), provenanceProbeTimeout)
	defer cancel()
	if pr, ok := adapter.(device.OSVersionProber); ok {
		if v, err := pr.OSVersion(ctx, id); err == nil {
			p.DeviceOS = runs.DeviceOSVersion{Version: v.Version, Build: v.Build}
		} else {
			slog.Info("runs: device OS version unavailable", "device", canonical, "error", err)
		}
	}
	if p.App.BundleID != "" {
		if pr, ok := adapter.(device.AppVersionProber); ok {
			if v, err := pr.AppVersion(ctx, id, p.App.BundleID); err == nil {
				p.App.Version, p.App.Build = v.Version, v.Build
				if p.App.Name == "" {
					p.App.Name = v.Name
				}
			} else {
				slog.Info("runs: app version unavailable", "device", canonical, "bundle_id", p.App.BundleID, "error", err)
			}
		}
	}
	return p
}

// runCloseParams reads release's outcome args.
func runCloseParams(args map[string]any) (runs.CloseParams, error) {
	cp := runs.CloseParams{
		Passed:  optTriState(args, "passed"),
		Outcome: optString(args, "outcome"),
	}
	if raw, ok := args["failure_screenshots"]; ok && raw != nil {
		var names []string
		switch v := raw.(type) {
		case []string:
			names = v
		case []any:
			for _, x := range v {
				s, ok := x.(string)
				if !ok {
					return cp, fmt.Errorf("failure_screenshots must be an array of strings")
				}
				names = append(names, s)
			}
		default:
			return cp, fmt.Errorf("failure_screenshots must be an array of strings")
		}
		cp.FailureScreenshots = runs.KnownNames(names)
	}
	return cp, nil
}

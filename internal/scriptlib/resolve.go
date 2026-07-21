// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package scriptlib

import (
	"fmt"
	"strings"
)

// ResolveTarget derives inject coordinates (app_input 0–1 surface space) from a
// ge-slice node dict. Preference order (first match wins):
//   - center_norm: [x,y] or {x|cx, y|cy}  — preferred for hit_targets (🎯T109)
//   - bbox_norm: [x,y,w,h] or dict → centre in 0–1 space
//   - screen: {cx|center_x|x, cy|center_y|y}
//   - bbox: [x,y,w,h] or {x,y,w|width,h|height} → centre (only when already in
//     inject space; prefer center_norm/bbox_norm for pts-space targets)
//   - cx/cy or x/y at top level
// Missing required geometry returns a clear slice-contract error (no guessing).
func ResolveTarget(node map[string]any) (cx, cy float64, err error) {
	if node == nil {
		return 0, 0, fmt.Errorf("resolve_target: nil node")
	}
	if cn, ok := node["center_norm"]; ok {
		if x, y, ok := pairFromAny(cn); ok {
			return x, y, nil
		}
		return 0, 0, fmt.Errorf("resolve_target: center_norm present but not a [x,y] pair — slice contract gap")
	}
	if bn, ok := node["bbox_norm"]; ok {
		return bboxCenter(bn)
	}
	if s, ok := node["screen"].(map[string]any); ok {
		if x, y, ok := pairXY(s, "cx", "cy"); ok {
			return x, y, nil
		}
		if x, y, ok := pairXY(s, "center_x", "center_y"); ok {
			return x, y, nil
		}
		if x, y, ok := pairXY(s, "x", "y"); ok {
			return x, y, nil
		}
		return 0, 0, fmt.Errorf("resolve_target: screen present but missing cx/cy (or x/y) — slice contract gap")
	}
	if b, ok := node["bbox"]; ok {
		return bboxCenter(b)
	}
	if x, y, ok := pairXY(node, "cx", "cy"); ok {
		return x, y, nil
	}
	if x, y, ok := pairXY(node, "x", "y"); ok {
		return x, y, nil
	}
	return 0, 0, fmt.Errorf("resolve_target: no center_norm/bbox_norm/screen/bbox/cx,cy on node — slice contract gap")
}

// FindByLabel searches a list of node maps for label / name / id match
// (case-sensitive). Returns the first hit or a clear error.
// For hit_targets prefer FindHitTarget (id/role) over label matching.
func FindByLabel(nodes []map[string]any, label string) (map[string]any, error) {
	if label == "" {
		return nil, fmt.Errorf("find_by_label: empty label")
	}
	for _, n := range nodes {
		if n == nil {
			continue
		}
		for _, key := range []string{"label", "name", "id"} {
			if v, ok := n[key]; ok {
				if s, ok := asString(v); ok && s == label {
					return n, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("find_by_label: no node with label/name/id %q", label)
}

// FindHitTarget resolves a 🎯T109 hit-target by stable id, then role.
// Display label is intentionally not matched — localization-safe addressing.
// Disabled targets (enabled == false) fail closed.
func FindHitTarget(nodes []map[string]any, key string) (map[string]any, error) {
	if key == "" {
		return nil, fmt.Errorf("find_hit_target: empty id/role")
	}
	// Prefer id over role when both could match different nodes.
	for _, n := range nodes {
		if n == nil {
			continue
		}
		if s, ok := asString(n["id"]); ok && s == key {
			return requireEnabled(n, key)
		}
	}
	for _, n := range nodes {
		if n == nil {
			continue
		}
		if s, ok := asString(n["role"]); ok && s == key {
			return requireEnabled(n, key)
		}
	}
	return nil, fmt.Errorf("find_hit_target: no target with id/role %q (label is not used for addressing)", key)
}

// TargetsFromHitSlice extracts the targets list from a hit_targets slice payload
// ({"targets": [...]}) or accepts a bare list of target dicts.
func TargetsFromHitSlice(v any) ([]map[string]any, error) {
	if m, ok := v.(map[string]any); ok {
		if t, ok := m["targets"]; ok {
			return NodesFromAny(t)
		}
		// Single target dict with id/kind.
		if _, hasID := m["id"]; hasID {
			return []map[string]any{m}, nil
		}
		return nil, fmt.Errorf("targets: dict has no \"targets\" array (and is not a single target)")
	}
	return NodesFromAny(v)
}

func requireEnabled(n map[string]any, key string) (map[string]any, error) {
	if v, ok := n["enabled"]; ok {
		switch e := v.(type) {
		case bool:
			if !e {
				return nil, fmt.Errorf("find_hit_target: target %q is disabled", key)
			}
		case string:
			if e == "false" || e == "0" {
				return nil, fmt.Errorf("find_hit_target: target %q is disabled", key)
			}
		}
	}
	return n, nil
}

// pairFromAny accepts [x,y], {x,y}, or {cx,cy}.
func pairFromAny(v any) (float64, float64, bool) {
	switch t := v.(type) {
	case []any:
		if len(t) < 2 {
			return 0, 0, false
		}
		x, okx := asFloat(t[0])
		y, oky := asFloat(t[1])
		return x, y, okx && oky
	case map[string]any:
		if x, y, ok := pairXY(t, "cx", "cy"); ok {
			return x, y, true
		}
		return pairXY(t, "x", "y")
	default:
		return 0, 0, false
	}
}

// NodesFromAny coerces a state-query-ish value into a list of node maps.
// Accepts []any of maps, or a map of name→node (physics.bodies style).
func NodesFromAny(v any) ([]map[string]any, error) {
	switch t := v.(type) {
	case []any:
		out := make([]map[string]any, 0, len(t))
		for i, el := range t {
			m, ok := el.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("nodes: element %d is not a dict", i)
			}
			// Preserve list order; inject name if absent.
			out = append(out, m)
		}
		return out, nil
	case map[string]any:
		out := make([]map[string]any, 0, len(t))
		for k, el := range t {
			m, ok := el.(map[string]any)
			if !ok {
				// leaf might be geometry for a named body; wrap
				if leaf, ok := el.(map[string]any); ok {
					m = leaf
				} else {
					continue
				}
				_ = ok
			}
			// copy so we can attach name without mutating caller unexpectedly
			cp := make(map[string]any, len(m)+1)
			for kk, vv := range m {
				cp[kk] = vv
			}
			if _, has := cp["name"]; !has {
				cp["name"] = k
			}
			if _, has := cp["label"]; !has {
				cp["label"] = k
			}
			out = append(out, cp)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("nodes: want list or dict of nodes, got %T", v)
	}
}

func pairXY(m map[string]any, kx, ky string) (float64, float64, bool) {
	x, okx := asFloat(m[kx])
	y, oky := asFloat(m[ky])
	return x, y, okx && oky
}

func bboxCenter(b any) (float64, float64, error) {
	switch t := b.(type) {
	case []any:
		if len(t) < 4 {
			return 0, 0, fmt.Errorf("resolve_target: bbox list needs [x,y,w,h]")
		}
		x, ok0 := asFloat(t[0])
		y, ok1 := asFloat(t[1])
		w, ok2 := asFloat(t[2])
		h, ok3 := asFloat(t[3])
		if !ok0 || !ok1 || !ok2 || !ok3 {
			return 0, 0, fmt.Errorf("resolve_target: bbox list values must be numbers")
		}
		return x + w/2, y + h/2, nil
	case map[string]any:
		x, okx := asFloat(t["x"])
		y, oky := asFloat(t["y"])
		w, okw := asFloat(t["w"])
		if !okw {
			w, okw = asFloat(t["width"])
		}
		h, okh := asFloat(t["h"])
		if !okh {
			h, okh = asFloat(t["height"])
		}
		if !okx || !oky || !okw || !okh {
			return 0, 0, fmt.Errorf("resolve_target: bbox dict needs x,y,w|width,h|height")
		}
		return x + w/2, y + h/2, nil
	default:
		return 0, 0, fmt.Errorf("resolve_target: bbox must be list or dict, got %T", b)
	}
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	default:
		return 0, false
	}
}

func asString(v any) (string, bool) {
	switch s := v.(type) {
	case string:
		return s, true
	default:
		// starlark may give us fmt-able types only
		if s, ok := v.(fmt.Stringer); ok {
			return s.String(), true
		}
		return fmt.Sprint(v), strings.TrimSpace(fmt.Sprint(v)) != ""
	}
}

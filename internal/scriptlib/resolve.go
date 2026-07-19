// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package scriptlib

import (
	"fmt"
	"strings"
)

// ResolveTarget derives inject coordinates from a ge-slice node dict.
// Accepts any of:
//   - screen: {cx|center_x|x, cy|center_y|y}
//   - bbox: [x,y,w,h] or {x,y,w|width,h|height} → centre
//   - cx/cy or x/y at top level
// Missing required geometry returns a clear slice-contract error (no guessing).
func ResolveTarget(node map[string]any) (cx, cy float64, err error) {
	if node == nil {
		return 0, 0, fmt.Errorf("resolve_target: nil node")
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
	return 0, 0, fmt.Errorf("resolve_target: no screen/bbox/cx,cy on node — slice contract gap")
}

// FindByLabel searches a list of node maps for label / name / id match
// (case-sensitive). Returns the first hit or a clear error.
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

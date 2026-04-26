// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package selector

import (
	"fmt"
	"strings"
)

// ParseSelectorString parses a comma-separated key=value (or key>=value,
// key<=value) string into a Selector. Used by the spyder CLI so Make
// scripts can write `--on platform=ios,os>=17` instead of embedding JSON.
//
// Recognised keys:
//   - platform={ios|android|ios-sim|android-emu}      → Platform
//   - model=<family>                                   → ModelFamily
//   - os>=<version>                                    → OSMin
//   - os<=<version>                                    → OSMax
//   - os_min=<version>                                 → OSMin (alternate spelling)
//   - os_max=<version>                                 → OSMax (alternate spelling)
//   - orientation_capable=true|false                   → OrientationCapable (bool)
//   - tags=<tag>[+<tag>...]                            → Tags (plus-separated)
//   - attr.<name>=<value>                              → Attrs[name] = value
//
// Empty input is an error. Unrecognised keys are an error. Multiple
// values for the same key (other than tags) are an error. Returns a
// fully-populated Selector or a descriptive error.
func ParseSelectorString(s string) (Selector, error) {
	if strings.TrimSpace(s) == "" {
		return Selector{}, fmt.Errorf("selector parse: empty selector string")
	}

	var sel Selector
	// Track which scalar keys have been set to catch duplicates.
	set := map[string]bool{}

	parts := strings.Split(s, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return Selector{}, fmt.Errorf("selector parse: empty token in %q", s)
		}

		key, value, err := splitEntry(part)
		if err != nil {
			return Selector{}, fmt.Errorf("selector parse: %w", err)
		}

		switch {
		case key == "platform":
			if err := checkDup(set, "platform", part); err != nil {
				return Selector{}, err
			}
			sel.Platform = value

		case key == "model":
			if err := checkDup(set, "model", part); err != nil {
				return Selector{}, err
			}
			sel.ModelFamily = value

		case key == "os>=":
			if err := checkDup(set, "os>=", part); err != nil {
				return Selector{}, err
			}
			sel.OSMin = value

		case key == "os<=":
			if err := checkDup(set, "os<=", part); err != nil {
				return Selector{}, err
			}
			sel.OSMax = value

		case key == "os_min":
			if err := checkDup(set, "os_min", part); err != nil {
				return Selector{}, err
			}
			sel.OSMin = value

		case key == "os_max":
			if err := checkDup(set, "os_max", part); err != nil {
				return Selector{}, err
			}
			sel.OSMax = value

		case key == "os":
			return Selector{}, fmt.Errorf("selector parse: ambiguous key %q in %q; use os>= or os<= or os_min/os_max", key, part)

		case key == "orientation_capable":
			if err := checkDup(set, "orientation_capable", part); err != nil {
				return Selector{}, err
			}
			b, err := parseBool(value, part)
			if err != nil {
				return Selector{}, err
			}
			sel.OrientationCapable = b

		case key == "tags":
			if err := checkDup(set, "tags", part); err != nil {
				return Selector{}, err
			}
			sel.Tags = strings.Split(value, "+")

		case strings.HasPrefix(key, "attr."):
			attrName := key[len("attr."):]
			if attrName == "" {
				return Selector{}, fmt.Errorf("selector parse: empty attr name in %q", part)
			}
			if sel.Attrs == nil {
				sel.Attrs = make(map[string]string)
			}
			attrKey := "attr." + attrName
			if err := checkDup(set, attrKey, part); err != nil {
				return Selector{}, err
			}
			sel.Attrs[attrName] = value

		default:
			return Selector{}, fmt.Errorf("selector parse: unknown key %q in %q", key, part)
		}
	}

	return sel, nil
}

// splitEntry splits a token of the form key=value, key>=value, or key<=value.
// The key returned includes the operator (e.g. "os>=", "os<=") so the caller
// can switch on it directly.
func splitEntry(token string) (key, value string, err error) {
	// Check for >= and <= before plain = so we don't mismatch "os>=17".
	if idx := strings.Index(token, ">="); idx >= 0 {
		k := strings.TrimSpace(token[:idx])
		v := strings.TrimSpace(token[idx+2:])
		if k == "" {
			return "", "", fmt.Errorf("empty key in %q", token)
		}
		if v == "" {
			return "", "", fmt.Errorf("empty value in %q", token)
		}
		return k + ">=", v, nil
	}
	if idx := strings.Index(token, "<="); idx >= 0 {
		k := strings.TrimSpace(token[:idx])
		v := strings.TrimSpace(token[idx+2:])
		if k == "" {
			return "", "", fmt.Errorf("empty key in %q", token)
		}
		if v == "" {
			return "", "", fmt.Errorf("empty value in %q", token)
		}
		return k + "<=", v, nil
	}
	// Plain =.
	idx := strings.IndexByte(token, '=')
	if idx < 0 {
		return "", "", fmt.Errorf("missing '=' in %q", token)
	}
	k := strings.TrimSpace(token[:idx])
	v := strings.TrimSpace(token[idx+1:])
	if k == "" {
		return "", "", fmt.Errorf("empty key in %q", token)
	}
	if v == "" {
		return "", "", fmt.Errorf("empty value in %q", token)
	}
	return k, v, nil
}

// checkDup returns an error if the key has already been set, and otherwise
// marks it as set.
func checkDup(set map[string]bool, key, token string) error {
	if set[key] {
		return fmt.Errorf("selector parse: duplicate key %q in %q", key, token)
	}
	set[key] = true
	return nil
}

// parseBool parses a boolean value, accepting true/false/1/0 (case-insensitive).
func parseBool(s, token string) (bool, error) {
	switch strings.ToLower(s) {
	case "true", "1":
		return true, nil
	case "false", "0":
		return false, nil
	default:
		return false, fmt.Errorf("selector parse: invalid bool value %q in %q (want true/false/1/0)", s, token)
	}
}

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package scriptlib is the durable host-Starlark library for 🎯T108:
// resolve/list/load scripts, pure L1 target resolution, and fail-closed
// dynamic-behaviour asserts. Drive/observe verbs stay in app_exec.
package scriptlib

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/marcelocantos/spyder/internal/paths"
)

//go:embed recipes/*.star
var bundledFS embed.FS

// ScriptsDir is the user-writable library root (~/.spyder/scripts).
func ScriptsDir() string {
	return filepath.Join(paths.Base(), "scripts")
}

// Info describes one durable script.
type Info struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Source string `json:"source"` // "bundled" | "user" | "path"
}

// List returns bundled + user scripts (user overrides same name).
func List() ([]Info, error) {
	byName := map[string]Info{}

	entries, err := fs.ReadDir(bundledFS, "recipes")
	if err != nil {
		return nil, fmt.Errorf("scriptlib: list bundled: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".star") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".star")
		byName[name] = Info{
			Name:   name,
			Path:   "bundled:" + name,
			Source: "bundled",
		}
	}

	userDir := ScriptsDir()
	if ents, err := os.ReadDir(userDir); err == nil {
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".star") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".star")
			byName[name] = Info{
				Name:   name,
				Path:   filepath.Join(userDir, e.Name()),
				Source: "user",
			}
		}
	}

	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Info, 0, len(names))
	for _, n := range names {
		out = append(out, byName[n])
	}
	return out, nil
}

// Load reads script source by library name, filesystem path, or bundled:name.
// Names without a slash resolve: user ScriptsDir first, then bundled recipes.
func Load(ref string) (source string, info Info, err error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", Info{}, fmt.Errorf("scriptlib: empty script reference")
	}

	if strings.HasPrefix(ref, "bundled:") {
		name := strings.TrimPrefix(ref, "bundled:")
		b, err := bundledFS.ReadFile("recipes/" + name + ".star")
		if err != nil {
			return "", Info{}, fmt.Errorf("scriptlib: bundled %q: %w", name, err)
		}
		return string(b), Info{Name: name, Path: "bundled:" + name, Source: "bundled"}, nil
	}

	// Absolute or relative path with slash / .star suffix → file.
	if strings.Contains(ref, string(filepath.Separator)) || strings.HasSuffix(ref, ".star") ||
		strings.HasPrefix(ref, ".") || filepath.IsAbs(ref) {
		b, err := os.ReadFile(ref)
		if err != nil {
			return "", Info{}, fmt.Errorf("scriptlib: read %q: %w", ref, err)
		}
		base := filepath.Base(ref)
		name := strings.TrimSuffix(base, ".star")
		return string(b), Info{Name: name, Path: ref, Source: "path"}, nil
	}

	// Library name.
	userPath := filepath.Join(ScriptsDir(), ref+".star")
	if b, err := os.ReadFile(userPath); err == nil {
		return string(b), Info{Name: ref, Path: userPath, Source: "user"}, nil
	}
	b, err := bundledFS.ReadFile("recipes/" + ref + ".star")
	if err != nil {
		return "", Info{}, fmt.Errorf("scriptlib: unknown script %q (not in %s or bundled recipes)", ref, ScriptsDir())
	}
	return string(b), Info{Name: ref, Path: "bundled:" + ref, Source: "bundled"}, nil
}

// ParamsPreamble turns a string map into Starlark assignments prepended to a script.
// Keys must be valid Starlark identifiers; values are string-quoted.
func ParamsPreamble(params map[string]string) (string, error) {
	if len(params) == 0 {
		return "", nil
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# params injected by spyder run-script / app_exec\n")
	for _, k := range keys {
		if !isIdent(k) {
			return "", fmt.Errorf("scriptlib: invalid param name %q", k)
		}
		fmt.Fprintf(&b, "%s = %q\n", k, params[k])
	}
	b.WriteByte('\n')
	return b.String(), nil
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
				return false
			}
			continue
		}
		if r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

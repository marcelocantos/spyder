// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package scriptlib

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSkeletonBundled(t *testing.T) {
	src, info, err := Load("skeleton")
	if err != nil {
		t.Fatal(err)
	}
	if info.Source != "bundled" {
		t.Fatalf("source=%q", info.Source)
	}
	if !strings.Contains(src, "skeleton") {
		t.Fatalf("source body missing recipe name: %q", src)
	}
}

func TestListContainsSkeleton(t *testing.T) {
	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range list {
		if s.Name == "skeleton" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("skeleton not in list: %+v", list)
	}
}

func TestUserOverride(t *testing.T) {
	dir := ScriptsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "skeleton.star")
	// Don't clobber permanently — write unique name instead.
	upath := filepath.Join(dir, "t108_user_probe.star")
	body := `emit({"recipe":"t108_user_probe","ok":True})`
	if err := os.WriteFile(upath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(upath) })

	src, info, err := Load("t108_user_probe")
	if err != nil {
		t.Fatal(err)
	}
	if info.Source != "user" {
		t.Fatalf("source=%q path=%q", info.Source, info.Path)
	}
	if src != body {
		t.Fatalf("body mismatch")
	}
	_ = path
}

func TestParamsPreamble(t *testing.T) {
	p, err := ParamsPreamble(map[string]string{"session_id": "s1", "x": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, `session_id = "s1"`) {
		t.Fatalf("preamble: %q", p)
	}
}

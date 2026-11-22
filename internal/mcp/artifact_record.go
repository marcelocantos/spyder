// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// artifactRecord remembers which build this daemon last installed for
// each app on each device, so ensure_session(path=...) can tell whether
// the build it was given is already there. Without it, an installed
// bundle id was taken to mean the given build was installed, and a
// stale build kept launching.
type artifactRecord struct {
	mu sync.Mutex
	// m maps device id + bundle id to the artifact's path and fingerprint.
	m map[string]string
}

func artifactKey(id, bundleID string) string { return id + "\x00" + bundleID }

// note records that the artifact at path was installed as bundleID on id.
func (r *artifactRecord) note(id, bundleID, path string) {
	fp, err := artifactFingerprint(path)
	if err != nil || bundleID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]string{}
	}
	r.m[artifactKey(id, bundleID)] = fp
}

// current reports whether the artifact at path is, unchanged, the last
// build this daemon installed as bundleID on id. Anything else — a new
// build, a different artifact, an install made by someone else or
// before this daemon started — is not current.
func (r *artifactRecord) current(id, bundleID, path string) bool {
	fp, err := artifactFingerprint(path)
	if err != nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.m[artifactKey(id, bundleID)] == fp
}

// artifactFingerprint identifies a build by its absolute path and the
// size and modification time of its files. A rebuild in place changes
// it; an untouched artifact keeps it.
func artifactFingerprint(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return fmt.Sprintf("%s|%d|%d", abs, info.Size(), info.ModTime().UnixNano()), nil
	}
	var b strings.Builder
	b.WriteString(abs)
	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(abs, p)
		fmt.Fprintf(&b, "|%s:%d:%d", rel, fi.Size(), fi.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(b.String()))
	return abs + "|" + hex.EncodeToString(sum[:]), nil
}

// platformForArtifact names the platform an artifact installs on, from
// its extension.
func platformForArtifact(path string) string {
	switch strings.ToLower(filepath.Ext(strings.TrimRight(path, "/"))) {
	case ".apk":
		return "android"
	case ".app", ".ipa":
		return "ios"
	}
	return ""
}

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package ship

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var reTeamID = regexp.MustCompile(`(?i)team_id\s*\(?\s*["']([A-Z0-9]+)["']\s*\)?`)

// ReadMatchfileTeamID reads fastlane/Matchfile (or Matchfile) under cwd
// and returns the Apple team_id if present.
func ReadMatchfileTeamID(cwd string) (string, error) {
	candidates := []string{
		filepath.Join(cwd, "fastlane", "Matchfile"),
		filepath.Join(cwd, "Matchfile"),
	}
	var lastErr error
	for _, p := range candidates {
		f, err := os.Open(p)
		if err != nil {
			lastErr = err
			continue
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if strings.HasPrefix(line, "#") {
				continue
			}
			if m := reTeamID.FindStringSubmatch(line); m != nil {
				return m[1], nil
			}
		}
		if err := sc.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("Matchfile %s has no team_id", p)
	}
	if lastErr != nil {
		return "", fmt.Errorf("no Matchfile under %s: %w", cwd, lastErr)
	}
	return "", fmt.Errorf("no Matchfile under %s", cwd)
}

// CheckStudioMatchfile ensures --studio's Apple team matches the
// consumer Matchfile team_id when a Matchfile exists. Missing Matchfile
// is OK (not every consumer uses match yet).
func CheckStudioMatchfile(studio, cwd string) error {
	studio, err := NormalizeStudio(studio)
	if err != nil {
		return err
	}
	want := AppleTeamID[studio]
	got, err := ReadMatchfileTeamID(cwd)
	if err != nil {
		if os.IsNotExist(err) || strings.Contains(err.Error(), "no Matchfile") {
			return nil
		}
		// No Matchfile is fine; other read errors still fine if file missing.
		if _, statErr := os.Stat(filepath.Join(cwd, "fastlane", "Matchfile")); os.IsNotExist(statErr) {
			if _, statErr2 := os.Stat(filepath.Join(cwd, "Matchfile")); os.IsNotExist(statErr2) {
				return nil
			}
		}
		return err
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("studio %s expects Apple team %s but Matchfile has team_id %s",
			studio, want, got)
	}
	return nil
}

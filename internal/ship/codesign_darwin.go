// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package ship

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// inspectExe runs `codesign -dv` on path. We deliberately use the
// codesign tool (not /usr/bin/security) — this is identity inspection
// of our own binary, not studio-secret I/O. Secret bytes only move
// through SecItem* in keychain_darwin.go.
func inspectExe(path string) (Signature, error) {
	cmd := exec.Command("/usr/bin/codesign", "-dv", "--verbose=4", path)
	var stderr bytes.Buffer
	cmd.Stdout = &stderr // codesign writes -dv to stderr; keep both
	cmd.Stderr = &stderr
	err := cmd.Run()
	raw := stderr.String()
	sig := Signature{Path: path, Raw: strings.TrimSpace(raw)}

	low := strings.ToLower(raw)
	if strings.Contains(low, "code object is not signed") {
		return sig, nil
	}
	if err != nil && !strings.Contains(raw, "Identifier=") && !strings.Contains(raw, "Authority=") {
		return sig, fmt.Errorf("codesign -dv: %w (%s)", err, strings.TrimSpace(raw))
	}

	sig.Signed = true
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Identifier="):
			sig.Identifier = strings.TrimPrefix(line, "Identifier=")
		case strings.HasPrefix(line, "TeamIdentifier="):
			v := strings.TrimPrefix(line, "TeamIdentifier=")
			if v != "not set" {
				sig.TeamID = v
			}
		case strings.HasPrefix(line, "Authority="):
			// First Authority= is the leaf cert.
			if sig.Authority == "" {
				sig.Authority = strings.TrimPrefix(line, "Authority=")
			}
		case strings.HasPrefix(line, "CDHash="):
			sig.CDHash = strings.TrimPrefix(line, "CDHash=")
		case line == "Signature=adhoc" || strings.Contains(line, "Signature=adhoc"):
			sig.AdHoc = true
		}
	}
	if sig.Authority == "" && strings.Contains(low, "signature=adhoc") {
		sig.AdHoc = true
	}
	return sig, nil
}

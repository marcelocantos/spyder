// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package notify fires user-visible notifications on the host machine.
// macOS is the only currently supported host; other platforms silently
// no-op.
package notify

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// MacOS fires a macOS notification via osascript. Returns nil if
// invocation succeeded or if the host isn't macOS (no-op).
func MacOS(title, body string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	// osascript string literals use double quotes with backslash
	// escaping. We also strip newlines to avoid truncation surprises.
	esc := func(s string) string {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		s = strings.ReplaceAll(s, "\n", " ")
		return s
	}
	script := fmt.Sprintf(`display notification "%s" with title "%s"`, esc(body), esc(title))
	return exec.Command("osascript", "-e", script).Run()
}

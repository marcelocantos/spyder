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

// MacOS fires a macOS notification. Prefers terminal-notifier (which
// registers as its own app and is reliably delivered), falling back to
// osascript's 'display notification' (which is attributed to Script
// Editor and often silently dropped when Script Editor's notification
// style is set to None). Returns nil if invocation succeeded or if the
// host isn't macOS (no-op).
func MacOS(title, body string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	if path, err := exec.LookPath("terminal-notifier"); err == nil {
		return exec.Command(path, "-title", title, "-message", body).Run()
	}
	// Fallback: osascript.
	esc := func(s string) string {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		s = strings.ReplaceAll(s, "\n", " ")
		return s
	}
	script := fmt.Sprintf(`display notification "%s" with title "%s"`, esc(body), esc(title))
	return exec.Command("osascript", "-e", script).Run()
}

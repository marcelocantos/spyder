// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package notify fires user-visible notifications on the host machine.
// macOS is the only currently supported host; other platforms silently
// no-op.
//
// Two flavours:
//   - MacOS: transient banner (terminal-notifier, fallback osascript).
//   - MacOSAlert: persistent alert that stays until the user clicks
//     "Dismiss" OR until MacOSAlertRemove is called with the same
//     group id. Uses `alerter`, the fork of terminal-notifier
//     designed for interactive persistent alerts.
package notify

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// MacOS fires a transient macOS notification. Prefers terminal-notifier,
// falls back to osascript's display notification. Returns nil if the
// host isn't macOS (no-op).
func MacOS(title, body string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	if path, err := exec.LookPath("terminal-notifier"); err == nil {
		return exec.Command(path, "-title", title, "-message", body).Run()
	}
	esc := func(s string) string {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		s = strings.ReplaceAll(s, "\n", " ")
		return s
	}
	script := fmt.Sprintf(`display notification "%s" with title "%s"`, esc(body), esc(title))
	return exec.Command("osascript", "-e", script).Run()
}

// MacOSAlert fires a persistent macOS alert with a single "Dismiss"
// action. It blocks until the user clicks Dismiss OR MacOSAlertRemove
// is called with the same group id. Callers should invoke it in a
// goroutine so the alert doesn't stall the rest of the program.
//
// If `alerter` isn't installed, falls back to a transient MacOS
// notification (banner that fades). The group argument is ignored in
// the fallback case.
func MacOSAlert(title, body, group string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	path, err := exec.LookPath("alerter")
	if err != nil {
		return MacOS(title, body)
	}
	return exec.Command(path,
		"--title", title,
		"--message", body,
		"--group", group,
		"--actions", "Dismiss",
	).Run()
}

// MacOSAlertRemove programmatically dismisses any pending alert in the
// named group. Safe to call even if no alert is outstanding (no-op).
func MacOSAlertRemove(group string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	path, err := exec.LookPath("alerter")
	if err != nil {
		return nil
	}
	return exec.Command(path, "--remove", group).Run()
}

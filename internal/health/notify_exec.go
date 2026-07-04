// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
)

// macOSNotifier implements Notifier via os/exec. It prefers terminal-notifier
// (if on PATH) because terminal-notifier supports the -group flag, which lets
// a later call silently replace the same on-screen notification and allows
// programmatic dismissal via -remove. If terminal-notifier is absent it falls
// back to osascript, which can post but not update or dismiss — Clear is a
// no-op on that path.
//
// Delivery is best-effort: a missing binary or exec error is logged at debug
// level and treated as nil so the daemon keeps running. The caller (the
// AttentionNotifier) is already fire-and-forget here.
type macOSNotifier struct {
	// useTerminalNotifier is set once at construction; avoids a PATH lookup
	// on every Notify/Clear call.
	useTerminalNotifier bool
}

// NewMacOSNotifier returns a Notifier backed by terminal-notifier (if on
// PATH) or osascript. Intended for production use; tests inject a fake.
func NewMacOSNotifier() Notifier {
	_, err := exec.LookPath("terminal-notifier")
	return &macOSNotifier{useTerminalNotifier: err == nil}
}

// Notify posts or updates (via -group) the notification identified by key.
func (n *macOSNotifier) Notify(key, title, message string) error {
	if n.useTerminalNotifier {
		// -group lets terminal-notifier replace an existing notification that
		// carries the same group ID, giving us an effective "update in place".
		cmd := exec.Command("terminal-notifier",
			"-title", title,
			"-message", message,
			"-group", key,
		)
		if err := cmd.Run(); err != nil {
			// Best-effort: never surface a delivery failure to the daemon.
			log.Printf("debug: terminal-notifier failed for key %q: %v", key, err)
		}
		return nil
	}

	// osascript fallback: can post but cannot update or dismiss. Sanitize
	// both strings to avoid breaking the AppleScript string literal.
	safeTitle := sanitizeForAppleScript(title)
	safeMessage := sanitizeForAppleScript(message)
	script := fmt.Sprintf(
		`display notification %q with title %q`,
		safeMessage, safeTitle,
	)
	cmd := exec.Command("osascript", "-e", script)
	if err := cmd.Run(); err != nil {
		log.Printf("debug: osascript failed for key %q: %v", key, err)
	}
	return nil
}

// Clear dismisses the notification identified by key. Only effective when
// terminal-notifier is available (osascript provides no dismiss API).
func (n *macOSNotifier) Clear(key string) error {
	if !n.useTerminalNotifier {
		// osascript has no dismiss API; silently no-op rather than erroring.
		return nil
	}
	cmd := exec.Command("terminal-notifier", "-remove", key)
	if err := cmd.Run(); err != nil {
		log.Printf("debug: terminal-notifier -remove failed for key %q: %v", key, err)
	}
	return nil
}

// sanitizeForAppleScript escapes double-quotes and backslashes so the string
// is safe to embed inside an AppleScript double-quoted string literal.
func sanitizeForAppleScript(s string) string {
	// Escape backslashes first, then double-quotes, to avoid double-escaping.
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package hostwifi

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ConfirmBounce shows a macOS dialog asking the user to approve a Wi-Fi
// bounce. Returns true when the user clicks "Bounce Wi-Fi".
func ConfirmBounce(ctx context.Context, message string) (bool, error) {
	safe := sanitizeForAppleScript(message)
	script := fmt.Sprintf(
		`display dialog %q with title "spyder" buttons {"Cancel", "Bounce Wi-Fi"} default button "Bounce Wi-Fi" with icon caution`,
		safe,
	)
	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// User cancelled or closed the dialog — not an error for the monitor.
		if strings.Contains(string(out), "User canceled") ||
			strings.Contains(err.Error(), "User canceled") {
			return false, nil
		}
		return false, fmt.Errorf("osascript confirm: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.Contains(string(out), "Bounce Wi-Fi"), nil
}

func sanitizeForAppleScript(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

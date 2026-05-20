// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// spyder-killusbmuxd is a single-purpose helper: it runs `killall
// usbmuxd` and exits. macOS's launchd respawns usbmuxd within ~1s,
// recovering from the "device list desync" wedge that spyder's heavy
// device-RPC workload reliably triggers (🎯T66).
//
// This binary exists separately from `spyder` so the operator can
// grant it NOPASSWD sudo via a sudoers.d entry without giving spyder
// itself any privilege:
//
//	# /etc/sudoers.d/spyder
//	%admin ALL=(root) NOPASSWD: /opt/homebrew/bin/spyder-killusbmuxd
//
// With that entry in place, `sudo /opt/homebrew/bin/spyder-killusbmuxd`
// runs without prompting. spyder's `doctor --fix` (or any operator
// invocation) just shells out to it.
//
// Without the sudoers entry, the binary still works but requires the
// user's normal sudo auth (password or PAM touchid).
//
// The binary takes no arguments and prints what it did. It exits
// non-zero only if killall itself fails for a reason other than "no
// matching processes" (usbmuxd may not be running yet on a fresh
// boot; that's not a failure).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func main() {
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "spyder-killusbmuxd: must run as root (typically via sudo).")
		fmt.Fprintln(os.Stderr, "  For auth-free recovery, add this line to /etc/sudoers.d/spyder:")
		fmt.Fprintln(os.Stderr, "    %admin ALL=(root) NOPASSWD: /opt/homebrew/bin/spyder-killusbmuxd")
		fmt.Fprintln(os.Stderr, "  (or use your account/group and the installed path).")
		os.Exit(2)
	}
	cmd := exec.Command("/usr/bin/killall", "usbmuxd")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// killall exits 1 with "No matching processes" when usbmuxd
		// isn't running. That's fine — launchd will start it on next
		// access. Distinguish via the message; permission-denied also
		// exits 1 but only matters when not root, which we already
		// rejected above.
		txt := string(out)
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 &&
			strings.Contains(strings.ToLower(txt), "no matching processes") {
			fmt.Println("spyder-killusbmuxd: usbmuxd not currently running (launchd will respawn on next access)")
			return
		}
		fmt.Fprintf(os.Stderr, "spyder-killusbmuxd: killall failed: %v\n%s", err, txt)
		os.Exit(3)
	}
	fmt.Println("spyder-killusbmuxd: killed usbmuxd (launchd will respawn it within ~1s)")
}

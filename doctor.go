// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// runDoctor implements `spyder doctor [--fix]`. It probes the iOS
// device stack for the known wedge: usbmuxd's third-party-visible
// device list desyncs from CoreDevice's view, blocking every go-ios
// RPC. Recovery is `killall usbmuxd` (launchd respawns it within
// ~1s) which requires root.
//
// Without --fix, doctor only diagnoses and prints any recommended
// action. With --fix, it shells out to spyder-killusbmuxd via sudo;
// the sudoers entry the user installs makes that auth-free.
//
// Exit codes:
//   - 0: healthy (or --fix succeeded and post-fix probe is healthy)
//   - 2: diagnosed unhealthy without --fix (or --fix didn't recover)
//   - 3: setup error (binary missing, command unavailable)
//
// (🎯T66.)
func runDoctor(args []string) {
	fix := false
	jsonOut := false
	for _, a := range args {
		switch a {
		case "--fix":
			fix = true
		case "--json":
			jsonOut = true
		case "--install-sudoers":
			installSudoers()
			return
		case "--help", "-h":
			fmt.Print(`Usage: spyder doctor [--fix] [--json] [--install-sudoers]

Probes the local iOS device stack and reports inconsistencies between
xcrun devicectl's view and go-ios's usbmux view.

  --fix              If wedged, run the bundled spyder-killusbmuxd
                     helper via sudo to restart usbmuxd.
  --json             Emit a machine-readable report.
  --install-sudoers  One-time setup: install a sudoers.d entry that
                     grants NOPASSWD sudo for the bundled
                     spyder-killusbmuxd helper, so --fix runs without
                     a password prompt. Requires one sudo invocation
                     to write to /etc/sudoers.d/.
`)
			return
		default:
			fmt.Fprintf(os.Stderr, "doctor: unknown flag %q\n", a)
			os.Exit(2)
		}
	}

	report := probeDevices()
	if jsonOut {
		raw, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(raw))
	} else {
		printDoctorReport(report)
	}

	if !report.UsbmuxWedge {
		return
	}
	if !fix {
		fmt.Println()
		fmt.Println("Re-run with --fix to invoke the bundled spyder-killusbmuxd helper.")
		os.Exit(2)
	}

	helper, err := findKillHelper()
	if err != nil {
		fmt.Fprintf(os.Stderr, "doctor: %v\n", err)
		os.Exit(3)
	}
	fmt.Printf("\ndoctor: invoking `sudo %s` to restart usbmuxd…\n", helper)
	cmd := exec.Command("sudo", helper)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "doctor: helper failed: %v\n", err)
		os.Exit(2)
	}

	// Give launchd a moment to respawn usbmuxd and re-enumerate.
	time.Sleep(2 * time.Second)
	post := probeDevices()
	fmt.Println()
	fmt.Println("Post-fix:")
	printDoctorReport(post)
	if post.UsbmuxWedge {
		fmt.Fprintln(os.Stderr, "doctor: usbmux is still missing devices after fix — device-side state may also be stuck (try unplug+replug)")
		os.Exit(2)
	}
}

// doctorReport is the result of one device-stack probe.
type doctorReport struct {
	DevicectlUDIDs []string `json:"devicectl_udids"`
	UsbmuxUDIDs    []string `json:"usbmux_udids"`
	MissingFromMux []string `json:"missing_from_usbmux,omitempty"`
	UsbmuxWedge    bool     `json:"usbmux_wedge"`
	IosBinary      string   `json:"ios_binary"`
	IosBinaryError string   `json:"ios_binary_error,omitempty"`
	DevicectlError string   `json:"devicectl_error,omitempty"`
}

// probeDevices runs both `xcrun devicectl list devices` and `bin/ios
// list`, classifies the result. UsbmuxWedge fires when devicectl
// sees N>0 connected iOS devices and usbmux sees fewer.
func probeDevices() doctorReport {
	r := doctorReport{}
	r.IosBinary = resolveBundledIOSBinary()
	if r.IosBinary == "" {
		r.IosBinaryError = "bundled `ios` binary not found (expected next to spyder under bin/ or libexec/spyder/)"
	} else {
		out, err := exec.Command(r.IosBinary, "list").Output()
		if err != nil {
			r.IosBinaryError = fmt.Sprintf("ios list failed: %v", err)
		} else {
			var resp struct {
				DeviceList []string `json:"deviceList"`
			}
			if jerr := json.Unmarshal(out, &resp); jerr != nil {
				r.IosBinaryError = fmt.Sprintf("ios list output unparseable: %v", jerr)
			} else {
				r.UsbmuxUDIDs = resp.DeviceList
			}
		}
	}

	tmp, terr := os.MkdirTemp("", "spyder-doctor-*")
	if terr == nil {
		defer os.RemoveAll(tmp)
		path := filepath.Join(tmp, "devices.json")
		_ = exec.Command("xcrun", "devicectl", "list", "devices", "--quiet", "--json-output", path).Run()
		if data, err := os.ReadFile(path); err == nil {
			var parsed struct {
				Result struct {
					Devices []struct {
						ConnectionProperties struct {
							TunnelState   string `json:"tunnelState"`
							TransportType string `json:"transportType"`
						} `json:"connectionProperties"`
						HardwareProperties struct {
							UDID         string `json:"udid"`
							Platform     string `json:"platform"`
						} `json:"hardwareProperties"`
					} `json:"devices"`
				} `json:"result"`
			}
			if jerr := json.Unmarshal(data, &parsed); jerr == nil {
				for _, d := range parsed.Result.Devices {
					if d.ConnectionProperties.TunnelState == "connected" && d.HardwareProperties.UDID != "" {
						r.DevicectlUDIDs = append(r.DevicectlUDIDs, d.HardwareProperties.UDID)
					}
				}
			} else {
				r.DevicectlError = fmt.Sprintf("devicectl output unparseable: %v", jerr)
			}
		} else {
			r.DevicectlError = fmt.Sprintf("devicectl read failed: %v", err)
		}
	} else {
		r.DevicectlError = fmt.Sprintf("tempdir for devicectl: %v", terr)
	}

	muxSet := map[string]bool{}
	for _, u := range r.UsbmuxUDIDs {
		muxSet[u] = true
	}
	for _, u := range r.DevicectlUDIDs {
		if !muxSet[u] {
			r.MissingFromMux = append(r.MissingFromMux, u)
		}
	}
	if len(r.DevicectlUDIDs) > 0 && len(r.MissingFromMux) > 0 {
		r.UsbmuxWedge = true
	}
	return r
}

func printDoctorReport(r doctorReport) {
	fmt.Printf("ios binary:              %s\n", r.IosBinary)
	if r.IosBinaryError != "" {
		fmt.Printf("  (error: %s)\n", r.IosBinaryError)
	}
	fmt.Printf("devicectl iOS UDIDs:     %s\n", joinOrNone(r.DevicectlUDIDs))
	if r.DevicectlError != "" {
		fmt.Printf("  (error: %s)\n", r.DevicectlError)
	}
	fmt.Printf("usbmux iOS UDIDs:        %s\n", joinOrNone(r.UsbmuxUDIDs))
	if r.UsbmuxWedge {
		fmt.Printf("⚠ usbmux is missing: %s\n", joinOrNone(r.MissingFromMux))
		fmt.Println("   → usbmuxd's device list has desynced from CoreDevice's view.")
		fmt.Println("   → recovery: restart usbmuxd (`sudo killall usbmuxd`; launchd respawns it).")
	} else if len(r.UsbmuxUDIDs) > 0 {
		fmt.Println("✓ usbmux and devicectl agree on attached iOS devices.")
	} else {
		fmt.Println("(no iOS devices currently attached)")
	}
}

func joinOrNone(s []string) string {
	if len(s) == 0 {
		return "(none)"
	}
	return strings.Join(s, ", ")
}

// findKillHelper resolves the spyder-killusbmuxd binary path.
// Order: alongside the spyder binary (typical Homebrew layout) →
// $PATH lookup → return error.
func findKillHelper() (string, error) {
	const name = "spyder-killusbmuxd"
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("%s not found alongside spyder or in $PATH; install the spyder package or build bin/%s in the source tree", name, name)
}

// installSudoers writes a sudoers.d entry granting the current user
// NOPASSWD sudo for the bundled spyder-killusbmuxd helper. One sudo
// prompt at install time, none thereafter. Uses `visudo -c -f` to
// validate before installing (per sudoers best practice) and
// `install -m 0440 -o root -g wheel` for the atomic privileged copy.
func installSudoers() {
	helper, err := findKillHelper()
	if err != nil {
		fmt.Fprintf(os.Stderr, "doctor: %v\n", err)
		os.Exit(3)
	}
	// Resolve symlinks so the sudoers entry refers to the real path —
	// sudoers matches on the literal path, and a symlinked location
	// (e.g. /opt/homebrew/bin/spyder-killusbmuxd → ../Cellar/...) might
	// not match unless we name what the user actually invokes.
	// Pick whichever resolves first.
	resolved := helper
	if abs, err := filepath.Abs(helper); err == nil {
		resolved = abs
	}

	user := os.Getenv("USER")
	if user == "" {
		fmt.Fprintln(os.Stderr, "doctor: $USER is empty; can't compose the sudoers entry")
		os.Exit(3)
	}

	content := fmt.Sprintf("# Generated by `spyder doctor --install-sudoers` (🎯T66).\n# Grants %s NOPASSWD sudo for the spyder usbmuxd-restart helper.\n%s ALL=(root) NOPASSWD: %s\n", user, user, resolved)

	tmp, err := os.CreateTemp("", "spyder-sudoers.*.tmp")
	if err != nil {
		fmt.Fprintf(os.Stderr, "doctor: tempfile: %v\n", err)
		os.Exit(3)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		fmt.Fprintf(os.Stderr, "doctor: write tempfile: %v\n", err)
		os.Exit(3)
	}
	if err := tmp.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "doctor: close tempfile: %v\n", err)
		os.Exit(3)
	}

	// Validate the sudoers syntax before asking sudo to install it.
	if vErr := exec.Command("visudo", "-c", "-f", tmpPath).Run(); vErr != nil {
		fmt.Fprintf(os.Stderr, "doctor: visudo rejected the generated sudoers content (%v)\n", vErr)
		fmt.Fprintf(os.Stderr, "  content was:\n%s\n", content)
		os.Exit(3)
	}

	const dest = "/etc/sudoers.d/spyder-killusbmuxd"
	fmt.Printf("doctor: installing sudoers entry at %s (one sudo prompt incoming)…\n", dest)
	fmt.Printf("        entry: %s ALL=(root) NOPASSWD: %s\n", user, resolved)
	cmd := exec.Command("sudo", "install", "-m", "0440", "-o", "root", "-g", "wheel", tmpPath, dest)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "doctor: sudo install failed: %v\n", err)
		os.Exit(2)
	}
	fmt.Println()
	fmt.Println("doctor: installed. From now on, `spyder doctor --fix` runs without a password prompt.")
}

// resolveBundledIOSBinary mirrors daemon.resolveIOSTunnelBinary's
// search order (SPYDER_IOS_TUNNEL_BINARY → libexec/spyder/ios →
// alongside the spyder binary → bin/ios in dev tree). Kept local so
// doctor doesn't depend on internal/daemon.
func resolveBundledIOSBinary() string {
	if env := os.Getenv("SPYDER_IOS_TUNNEL_BINARY"); env != "" {
		if _, err := os.Stat(env); err == nil {
			return env
		}
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		// Homebrew layout: libexec/spyder/ios alongside bin/spyder.
		for _, rel := range []string{
			filepath.Join("..", "libexec", "spyder", "ios"),
			"ios",
		} {
			candidate := filepath.Clean(filepath.Join(dir, rel))
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}
	if p, err := exec.LookPath("ios"); err == nil {
		return p
	}
	return ""
}

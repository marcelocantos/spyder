// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Helpers for autoawake's transparent KeepAwake install flow (🎯T32).
//
// The autoawake supervisor calls these in sequence when a new iOS device
// appears: detect preconditions, build the app once per daemon lifetime,
// install on the device, launch. Each helper returns a typed error so
// autoawake can decide whether to log an actionable message (human gate)
// or a generic failure (transient / retryable).

// ── Gate errors ──────────────────────────────────────────────────────────────

// ErrNoCodesigningIdentity indicates Xcode has no provisioning team
// registered in IDEProvisioningTeams. Human fix: sign in to Xcode with
// an Apple ID at Xcode → Settings → Accounts.
var ErrNoCodesigningIdentity = errors.New("no Xcode provisioning team registered (sign in at Xcode → Settings → Accounts)")

// ErrDeveloperModeDisabled indicates the device has Developer Mode off.
// Human fix: enable at Settings → Privacy & Security → Developer Mode
// (the device will reboot).
var ErrDeveloperModeDisabled = errors.New("Developer Mode disabled on device")

// ErrTrustNotGranted indicates an install attempt surfaced the
// "untrusted developer" block. Human fix: Settings → General →
// VPN & Device Management, tap the developer entry, tap Trust.
var ErrTrustNotGranted = errors.New("developer certificate not trusted on device")

// ── Codesigning identity discovery ───────────────────────────────────────────

// teamIDPattern extracts the value of a `teamID = XXXXXXXXXX;` line in
// the old-style plist dictionary that `defaults read` emits.
var teamIDPattern = regexp.MustCompile(`teamID\s*=\s*([A-Z0-9]{10})\s*;`)

// freeProvisioningPattern locates the `isFreeProvisioningTeam = 1;`
// flag inside a team block.
var freeProvisioningPattern = regexp.MustCompile(`isFreeProvisioningTeam\s*=\s*1\s*;`)

// teamBlockPattern splits the IDEProvisioningTeams output into one
// substring per team dict. The output's structure is a top-level dict
// keyed by Apple ID whose values are arrays of team dicts; we only need
// to find every team dict, regardless of which Apple ID owns it.
var teamBlockPattern = regexp.MustCompile(`\{[^{}]*teamID\s*=\s*[A-Z0-9]{10}[^{}]*\}`)

// DetectCodesigningTeam returns the Xcode-registered provisioning team
// to use for the KeepAwake build. Reads `defaults read com.apple.dt.Xcode
// IDEProvisioningTeams` (the same data Xcode shows under Settings →
// Accounts).
//
// Preference order:
//
//  1. **Paid Developer Program team** (`isFreeProvisioningTeam = 0`).
//     Provisioning profiles last ~1 year, so an autoawake-built
//     KeepAwake install survives well past the 7-day reinstall churn
//     that the free path imposes.
//  2. **Free Personal Team** (`isFreeProvisioningTeam = 1`). Profiles
//     expire after 7 days; the convergence loop will see the install
//     fail to launch with CoreDeviceError 1002 ("No provider was
//     found") shortly after expiry, and (once 🎯T34 lands) auto-
//     uninstall + reinstall to refresh the profile. Used only when no
//     paid team is available.
//
// Returns ErrNoCodesigningIdentity when Xcode has no registered teams,
// which means the user has not signed in to Xcode.
//
// Why not `security find-identity`? The keychain may contain certs for
// teams Xcode has no registered Apple ID for (e.g. enterprise certs
// imported from a colleague's keychain export). Building with such a
// team fails with "No Account for Team". IDEProvisioningTeams is the
// authoritative list of teams Xcode can fetch profiles for.
func DetectCodesigningTeam() (string, error) {
	cmd := exec.Command("defaults", "read", "com.apple.dt.Xcode", "IDEProvisioningTeams")
	out, err := cmd.Output()
	if err != nil {
		// `defaults read` exits non-zero if the key is missing.
		return "", ErrNoCodesigningIdentity
	}
	blocks := teamBlockPattern.FindAllString(string(out), -1)
	if len(blocks) == 0 {
		return "", ErrNoCodesigningIdentity
	}
	// First pass: prefer a paid (non-free) team.
	for _, block := range blocks {
		if freeProvisioningPattern.MatchString(block) {
			continue
		}
		if m := teamIDPattern.FindStringSubmatch(block); len(m) >= 2 {
			return m[1], nil
		}
	}
	// Second pass: fall back to a free Personal Team if that's all
	// the user has.
	for _, block := range blocks {
		if m := teamIDPattern.FindStringSubmatch(block); len(m) >= 2 {
			return m[1], nil
		}
	}
	return "", ErrNoCodesigningIdentity
}

// ── Developer Mode probe ─────────────────────────────────────────────────────

// DetectDeveloperMode queries the device's Developer Mode state via
// pymobiledevice3. Returns (true, nil) when enabled, (false, nil) when
// disabled, and (false, error) when the probe itself fails (e.g.
// pmd3 missing, device unreachable). Callers that get a probe error
// should fall back to optimistic-install rather than blocking.
func DetectDeveloperMode(udid string) (bool, error) {
	cmd := exec.Command("pymobiledevice3", "amfi", "developer-mode-status",
		"--udid", udid)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("developer-mode-status: %w\n%s",
			err, strings.TrimSpace(stderr.String()))
	}
	// pmd3 prints "true" / "false" with trailing newline.
	output := strings.TrimSpace(stdout.String())
	switch strings.ToLower(output) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("unexpected developer-mode-status output: %q", output)
	}
}

// ── KeepAwake build (cached per daemon lifetime) ─────────────────────────────

// keepAwakeBuild memoises the xcodebuild result so multiple devices
// detected in the same daemon session share one build. The build is
// expensive (10-30 s cold); caching drops per-device install latency to
// just devicectl install + launch.
type keepAwakeBuild struct {
	once sync.Once
	path string
	err  error
}

var globalKeepAwakeBuild keepAwakeBuild

// BuildKeepAwake runs xcodebuild for KeepAwake with the given team ID.
// Idempotent within a daemon lifetime: the first call drives the actual
// build, subsequent calls return the cached result.
//
// Returns the path to the built .app bundle on success. The DEVELOPMENT_TEAM
// is passed on the command line; the committed project.pbxproj is never
// mutated.
func BuildKeepAwake(teamID string) (string, error) {
	globalKeepAwakeBuild.once.Do(func() {
		globalKeepAwakeBuild.path, globalKeepAwakeBuild.err = buildKeepAwakeNow(teamID)
	})
	return globalKeepAwakeBuild.path, globalKeepAwakeBuild.err
}

func buildKeepAwakeNow(teamID string) (string, error) {
	repoRoot, err := findRepoRoot()
	if err != nil {
		return "", fmt.Errorf("locate spyder source: %w", err)
	}
	projectPath := filepath.Join(repoRoot, "ios", "KeepAwake", "KeepAwake.xcodeproj")
	if _, err := os.Stat(projectPath); err != nil {
		return "", fmt.Errorf("KeepAwake.xcodeproj not found at %s: %w", projectPath, err)
	}

	derivedData := filepath.Join(os.TempDir(), "spyder-keepawake-build")
	started := time.Now()
	cmd := exec.Command("xcodebuild",
		"-project", projectPath,
		"-scheme", "KeepAwake",
		"-configuration", "Release",
		"-destination", "generic/platform=iOS",
		"-derivedDataPath", derivedData,
		"-allowProvisioningUpdates",
		"DEVELOPMENT_TEAM="+teamID,
		"CODE_SIGN_STYLE=Automatic",
		"build",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	elapsedMs := time.Since(started).Milliseconds()
	if err != nil {
		slog.Warn("xcodebuild KeepAwake failed",
			"team", teamID, "duration_ms", elapsedMs,
			"error", err.Error(),
			"stderr_tail", truncate(stderr.String(), 400))
		return "", fmt.Errorf("xcodebuild: %w\n%s",
			err, truncate(stderr.String(), 400))
	}
	appPath := filepath.Join(derivedData, "Build", "Products",
		"Release-iphoneos", "KeepAwake.app")
	if _, err := os.Stat(appPath); err != nil {
		return "", fmt.Errorf("build succeeded but .app not found at %s: %w", appPath, err)
	}
	slog.Info("KeepAwake built",
		"team", teamID, "duration_ms", elapsedMs, "app", appPath)
	return appPath, nil
}

// findRepoRoot returns a directory under which `ios/KeepAwake/KeepAwake.
// xcodeproj` exists. Three resolution paths in order:
//
//  1. **Production install**: `<real-exe-dir>/../libexec/spyder-source`.
//     The Homebrew tarball bundles the KeepAwake Swift source under
//     `libexec/spyder-source/ios/KeepAwake/`. Resolved via EvalSymlinks
//     on `os.Executable()` so the Cellar path is found through the
//     `/opt/homebrew/bin/spyder` symlink (same trick as 🎯T35).
//  2. **Repo bin layout**: walk up from the (unresolved) executable
//     directory looking for `ios/KeepAwake/KeepAwake.xcodeproj`.
//     Catches `bin/spyder` when invoked from anywhere under the repo.
//  3. **CWD-relative**: `cwd/ios/KeepAwake/KeepAwake.xcodeproj`. Dev
//     fallback for `go run .` from the repo root.
func findRepoRoot() (string, error) {
	// 1. Production install layout (Homebrew).
	if exe, err := os.Executable(); err == nil {
		if real, evalErr := filepath.EvalSymlinks(exe); evalErr == nil {
			candidate := filepath.Join(filepath.Dir(real), "..", "libexec", "spyder-source")
			if _, err := os.Stat(filepath.Join(candidate, "ios", "KeepAwake", "KeepAwake.xcodeproj")); err == nil {
				return candidate, nil
			}
		}
	}

	// 2. Repo-bin layout.
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for range 8 {
			if _, err := os.Stat(filepath.Join(dir, "ios", "KeepAwake", "KeepAwake.xcodeproj")); err == nil {
				return dir, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	// 3. CWD-relative.
	if cwd, err := os.Getwd(); err == nil {
		if _, err := os.Stat(filepath.Join(cwd, "ios", "KeepAwake", "KeepAwake.xcodeproj")); err == nil {
			return cwd, nil
		}
	}
	return "", errors.New("ios/KeepAwake/KeepAwake.xcodeproj not found in libexec/spyder-source, relative to executable, or in cwd")
}

// ── Install ──────────────────────────────────────────────────────────────────

// untrustedPattern matches devicectl's "untrusted developer" failure
// output. Surfaces as ErrTrustNotGranted.
var untrustedPattern = regexp.MustCompile(`(?i)untrusted developer|not.*trusted|trust.*developer`)

// InstallKeepAwake invokes `xcrun devicectl device install app` to
// install the pre-built KeepAwake.app onto the device. Returns
// ErrTrustNotGranted when devicectl's output indicates the developer
// certificate has not been trusted on-device.
func InstallKeepAwake(udid, appPath string) error {
	if udid == "" {
		return errors.New("device identifier is empty")
	}
	started := time.Now()
	cmd := exec.Command("xcrun", "devicectl", "device", "install", "app",
		"--device", udid, appPath)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	err := cmd.Run()
	elapsedMs := time.Since(started).Milliseconds()
	output := combined.String()
	if err != nil {
		if untrustedPattern.MatchString(output) {
			slog.Warn("devicectl install KeepAwake: trust not granted",
				"device", udid, "duration_ms", elapsedMs)
			return ErrTrustNotGranted
		}
		slog.Warn("devicectl install KeepAwake failed",
			"device", udid, "duration_ms", elapsedMs,
			"error", err.Error(),
			"output_tail", truncate(output, 400))
		return fmt.Errorf("devicectl install: %w\n%s", err, truncate(output, 400))
	}
	slog.Info("KeepAwake installed",
		"device", udid, "duration_ms", elapsedMs, "app", appPath)
	return nil
}

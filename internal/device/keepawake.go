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

// ErrNoCodesigningIdentity indicates no Apple Development codesigning
// identity is in the Mac's keychain. Human fix: sign in to Xcode with
// an Apple ID at Xcode → Settings → Accounts.
var ErrNoCodesigningIdentity = errors.New("no Apple Development codesigning identity in keychain")

// ErrDeveloperModeDisabled indicates the device has Developer Mode off.
// Human fix: enable at Settings → Privacy & Security → Developer Mode
// (the device will reboot).
var ErrDeveloperModeDisabled = errors.New("Developer Mode disabled on device")

// ErrTrustNotGranted indicates an install attempt surfaced the
// "untrusted developer" block. Human fix: Settings → General →
// VPN & Device Management, tap the developer entry, tap Trust.
var ErrTrustNotGranted = errors.New("developer certificate not trusted on device")

// ── Codesigning identity discovery ───────────────────────────────────────────

// codesigningTeamPattern matches the TEAM ID in parentheses within a
// `security find-identity` line. Example line:
//
//	1) ABCDEF0123 "Apple Development: jane@example.com (TEAMID)"
var codesigningTeamPattern = regexp.MustCompile(`Apple Development:[^)]*\(([A-Z0-9]{10})\)`)

// DetectCodesigningTeam scans the Mac's keychain for an Apple Development
// codesigning identity and returns the team ID. Returns
// ErrNoCodesigningIdentity if none present.
func DetectCodesigningTeam() (string, error) {
	cmd := exec.Command("security", "find-identity", "-p", "codesigning", "-v")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("security find-identity: %w", err)
	}
	matches := codesigningTeamPattern.FindStringSubmatch(string(out))
	if len(matches) < 2 {
		return "", ErrNoCodesigningIdentity
	}
	return matches[1], nil
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

// findRepoRoot walks up from the executable's directory looking for the
// ios/KeepAwake/ tree. Matches the same discovery pattern
// resolveBridgeBinary uses in daemon.go.
func findRepoRoot() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
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
	// Fall back to CWD-relative lookup (dev-bin case).
	if cwd, err := os.Getwd(); err == nil {
		if _, err := os.Stat(filepath.Join(cwd, "ios", "KeepAwake", "KeepAwake.xcodeproj")); err == nil {
			return cwd, nil
		}
	}
	return "", errors.New("ios/KeepAwake/KeepAwake.xcodeproj not found relative to executable or cwd")
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

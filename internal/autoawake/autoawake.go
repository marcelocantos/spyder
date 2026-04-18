// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package autoawake supervises iOS devices: whenever a paired iOS
// device appears via tunneld, ensure the KeepAwake companion app is
// installed, running, and — if newly launched — fire a macOS
// notification so the user knows to unlock the device if needed.
//
// The supervisor is started by daemon.Start. It runs for the lifetime
// of the server and exits on context cancel.
package autoawake

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/inventory"
	"github.com/marcelocantos/spyder/internal/notify"
	"github.com/marcelocantos/spyder/internal/tunneld"
)

const (
	pollInterval             = 2 * time.Second
	settleDelay              = 3 * time.Second
	retryWhileLockedInterval = 10 * time.Second
	retryWhileLockedBudget   = 30 // ~5 minutes of retries
)

// Supervisor polls tunneld and ensures KeepAwake is running on every
// iOS device that appears.
type Supervisor struct {
	tunneld   *tunneld.Client
	inventory *inventory.Store
	ios       *device.IOSAdapter

	// projectDir is the path containing ios/KeepAwake's project.yml.
	// Discovered on first auto-deploy; empty means deploy is disabled.
	projectDir string

	mu       sync.Mutex
	inFlight map[string]bool // UDIDs currently being handled
}

// New constructs a Supervisor. Pass the already-initialised tunneld
// client from daemon.Start.
func New(tun *tunneld.Client) *Supervisor {
	return &Supervisor{
		tunneld:    tun,
		inventory:  inventory.New(),
		ios:        device.NewIOSAdapter(),
		projectDir: findKeepAwakeProject(),
		inFlight:   map[string]bool{},
	}
}

// Run blocks polling tunneld until ctx is cancelled.
func (s *Supervisor) Run(ctx context.Context) {
	if s.projectDir == "" {
		slog.Warn("autoawake: KeepAwake project not found — auto-deploy disabled. Set SPYDER_KEEPAWAKE_PROJECT to a directory containing project.yml, or run spyder from a working tree with ios/KeepAwake")
	} else {
		slog.Info("autoawake: ready", "project_dir", s.projectDir)
	}

	seen := map[string]bool{}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// First tick immediately so existing paired devices are handled on startup.
	s.tick(ctx, seen)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx, seen)
		}
	}
}

func (s *Supervisor) tick(ctx context.Context, seen map[string]bool) {
	udids, err := s.tunneld.Probe()
	if err != nil {
		return // tunneld unavailable — quiet retry next tick
	}
	current := map[string]bool{}
	for _, udid := range udids {
		current[udid] = true
		if seen[udid] {
			continue
		}
		seen[udid] = true
		go s.handleNewDevice(ctx, udid)
	}
	// Forget devices that disappeared so replug retriggers.
	for udid := range seen {
		if !current[udid] {
			delete(seen, udid)
		}
	}
}

// handleNewDevice runs the install/launch/notify sequence for one
// newly-seen UDID. Designed to be safe to run concurrently (per-UDID
// lock guards against overlapping handlers, though in practice the
// seen-set gates this too).
func (s *Supervisor) handleNewDevice(ctx context.Context, udid string) {
	s.mu.Lock()
	if s.inFlight[udid] {
		s.mu.Unlock()
		return
	}
	s.inFlight[udid] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.inFlight, udid)
		s.mu.Unlock()
	}()

	alias := s.aliasOf(udid)
	slog.Info("autoawake: new device", "udid", udid, "alias", alias)

	// Wait briefly for tunneld/DDI to settle before DVT calls.
	select {
	case <-ctx.Done():
		return
	case <-time.After(settleDelay):
	}

	// Check installed state. If not installed, try to auto-deploy.
	installed, err := s.isKeepAwakeInstalled(udid)
	if err != nil {
		slog.Warn("autoawake: install check failed", "udid", udid, "alias", alias, "error", err)
		return
	}
	if !installed {
		if s.projectDir == "" {
			slog.Warn("autoawake: KeepAwake not installed and auto-deploy disabled",
				"udid", udid, "alias", alias)
			return
		}
		slog.Info("autoawake: deploying KeepAwake", "udid", udid, "alias", alias)
		if err := s.deployKeepAwake(ctx, udid); err != nil {
			slog.Warn("autoawake: deploy failed", "udid", udid, "alias", alias, "error", err)
			return
		}
	}

	// Launch + retry-on-lock loop. dvt launch fails fast on a locked
	// device with a distinctive error; we notify once and retry until
	// the user unlocks.
	lockedNotified := false
	for attempt := 0; attempt < retryWhileLockedBudget; attempt++ {
		// Re-check running each iteration — user might have launched
		// it manually between attempts.
		if running, _ := s.isKeepAwakeRunning(udid); running {
			slog.Info("autoawake: KeepAwake already running, skip", "udid", udid, "alias", alias)
			return
		}

		err := s.ios.LaunchApp(udid, device.KeepAwakeBundleID)
		if err == nil {
			slog.Info("autoawake: KeepAwake launched", "udid", udid, "alias", alias)
			_ = notify.MacOS("spyder",
				fmt.Sprintf("Launched KeepAwake on %s", alias))
			return
		}
		if errors.Is(err, device.ErrLocked) {
			if !lockedNotified {
				slog.Info("autoawake: device locked — notifying user", "udid", udid, "alias", alias)
				_ = notify.MacOS("spyder",
					fmt.Sprintf("Unlock %s to enable keep-awake", alias))
				lockedNotified = true
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(retryWhileLockedInterval):
			}
			continue
		}
		// Some other failure — not worth retrying.
		slog.Warn("autoawake: launch failed", "udid", udid, "alias", alias, "error", summariseErr(err))
		return
	}
	slog.Info("autoawake: giving up on locked device", "udid", udid, "alias", alias)
}

// summariseErr strips pymobiledevice3's rich-console traceback
// decorations so log lines stay readable.
func summariseErr(err error) string {
	s := err.Error()
	if i := strings.Index(s, "DvtException:"); i >= 0 {
		return strings.TrimSpace(s[i:])
	}
	// Pull the first non-decorative line.
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "│") || strings.HasPrefix(line, "╰") ||
			strings.HasPrefix(line, "╭") || strings.HasPrefix(line, "→") {
			continue
		}
		return line
	}
	return truncate(s, 200)
}

// aliasOf resolves a UDID to an inventory alias, falling back to a
// short UDID form for readability.
func (s *Supervisor) aliasOf(udid string) string {
	if a := s.inventory.AliasFor(udid); a != "" {
		return a
	}
	if len(udid) > 12 {
		return udid[:8] + "…"
	}
	return udid
}

// isKeepAwakeInstalled checks whether the KeepAwake bundle id is
// present in the device's User apps.
func (s *Supervisor) isKeepAwakeInstalled(udid string) (bool, error) {
	apps, err := s.ios.ListApps(udid)
	if err != nil {
		return false, err
	}
	for _, a := range apps {
		if a.BundleID == device.KeepAwakeBundleID {
			return true, nil
		}
	}
	return false, nil
}

// isKeepAwakeRunning resolves the PID of KeepAwake via DVT. A valid
// PID means running; "app not running" is treated as false without
// bubbling the error.
func (s *Supervisor) isKeepAwakeRunning(udid string) (bool, error) {
	cmd := exec.Command("pymobiledevice3", "developer", "dvt", "process-id-for-bundle-id",
		device.KeepAwakeBundleID, "--udid", udid)
	out, _ := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	// pymobiledevice3 formats the result variously; accept any trailing integer > 0.
	if i := strings.LastIndexAny(text, " :->"); i >= 0 {
		text = strings.TrimSpace(text[i+1:])
	}
	if pid, err := strconv.Atoi(text); err == nil && pid > 0 {
		return true, nil
	}
	return false, nil
}

// deployKeepAwake regenerates the xcodeproj (idempotent via
// xcodegen), builds against the target UDID so the device is
// registered with the provisioning profile, and installs via
// devicectl.
func (s *Supervisor) deployKeepAwake(ctx context.Context, udid string) error {
	if s.projectDir == "" {
		return errors.New("no project dir")
	}

	// 1. xcodegen generate (idempotent)
	if err := run(ctx, s.projectDir, "xcodegen", "generate"); err != nil {
		return fmt.Errorf("xcodegen: %w", err)
	}

	// 2. xcodebuild targeting this UDID
	xcodeproj := filepath.Join(s.projectDir, "KeepAwake.xcodeproj")
	if err := run(ctx, "", "xcodebuild",
		"-project", xcodeproj,
		"-scheme", "KeepAwake",
		"-destination", "platform=iOS,id="+udid,
		"-allowProvisioningUpdates",
		"build",
	); err != nil {
		return fmt.Errorf("xcodebuild: %w", err)
	}

	// 3. Locate the .app in DerivedData.
	app, err := findBuiltApp()
	if err != nil {
		return err
	}

	// 4. devicectl install
	if err := run(ctx, "", "xcrun", "devicectl", "device", "install", "app",
		"--device", udid, app,
	); err != nil {
		return fmt.Errorf("devicectl install: %w", err)
	}

	return nil
}

// findKeepAwakeProject locates the Xcode project containing
// ios/KeepAwake's project.yml. Search order:
//  1. $SPYDER_KEEPAWAKE_PROJECT
//  2. walk up from the current working directory looking for ios/KeepAwake/project.yml
func findKeepAwakeProject() string {
	if p := os.Getenv("SPYDER_KEEPAWAKE_PROJECT"); p != "" {
		if _, err := os.Stat(filepath.Join(p, "project.yml")); err == nil {
			return p
		}
	}
	wd, err := os.Getwd()
	if err == nil {
		dir := wd
		for {
			candidate := filepath.Join(dir, "ios", "KeepAwake", "project.yml")
			if _, err := os.Stat(candidate); err == nil {
				return filepath.Dir(candidate)
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return ""
}

// findBuiltApp globs for KeepAwake.app under Xcode's DerivedData.
// Returns the path of the most recently modified match.
func findBuiltApp() (string, error) {
	pattern := filepath.Join(
		os.Getenv("HOME"),
		"Library/Developer/Xcode/DerivedData/KeepAwake-*/Build/Products/Debug-iphoneos/KeepAwake.app",
	)
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return "", fmt.Errorf("no built KeepAwake.app under DerivedData")
	}
	// Pick the newest by mtime.
	best := matches[0]
	bestMtime := mustStat(best).ModTime()
	for _, m := range matches[1:] {
		if mt := mustStat(m).ModTime(); mt.After(bestMtime) {
			best = m
			bestMtime = mt
		}
	}
	return best, nil
}

func mustStat(p string) os.FileInfo {
	st, err := os.Stat(p)
	if err != nil {
		// Shouldn't happen for Glob hits; return a zero-value sentinel.
		return stubFileInfo{}
	}
	return st
}

type stubFileInfo struct{}

func (stubFileInfo) Name() string       { return "" }
func (stubFileInfo) Size() int64        { return 0 }
func (stubFileInfo) Mode() os.FileMode  { return 0 }
func (stubFileInfo) ModTime() time.Time { return time.Time{} }
func (stubFileInfo) IsDir() bool        { return false }
func (stubFileInfo) Sys() any           { return nil }

// run executes cmd with args in dir (empty = cwd) and streams
// output to spyder's own stderr via slog at debug level.
func run(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %v\n%s", name, err, truncate(string(out), 400))
	}
	return nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

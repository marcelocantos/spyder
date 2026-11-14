// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/marcelocantos/spyder/internal/network"
)

// SimulatorAdapter talks to iOS simulators through `xcrun simctl`
// (🎯T188). Simulators are not USB devices, so go-ios cannot reach
// them; the handler routes a simulator UDID here instead of to the
// IOSAdapter.
type SimulatorAdapter struct{}

// NewSimulatorAdapter returns a new iOS simulator adapter.
func NewSimulatorAdapter() *SimulatorAdapter { return &SimulatorAdapter{} }

// simctlTimeout bounds one simctl call that is not an install.
const simctlTimeout = 60 * time.Second

func runSimctl(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "xcrun", append([]string{"simctl"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("simctl %s: %w: %s", args[0], err, truncate(strings.TrimSpace(stderr.String()), 240))
	}
	return out, nil
}

func simctlQuick(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), simctlTimeout)
	defer cancel()
	return runSimctl(ctx, args...)
}

// BootedSimulators lists booted simulators as device Info rows.
func BootedSimulators(ctx context.Context) ([]Info, error) {
	out, err := runSimctl(ctx, "list", "devices", "booted", "--json")
	if err != nil {
		return nil, err
	}
	var raw struct {
		Devices map[string][]struct {
			UDID       string `json:"udid"`
			Name       string `json:"name"`
			State      string `json:"state"`
			DeviceType string `json:"deviceTypeIdentifier"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse simctl devices: %w", err)
	}
	var infos []Info
	for runtime, devs := range raw.Devices {
		for _, d := range devs {
			if d.State != "Booted" {
				continue
			}
			infos = append(infos, Info{
				UUID:     d.UDID,
				Name:     d.Name,
				Platform: "ios",
				Model:    strings.TrimPrefix(d.DeviceType, "com.apple.CoreSimulator.SimDeviceType."),
				OS:       simRuntimeLabel(runtime) + " (simulator)",
			})
		}
	}
	return infos, nil
}

// simRuntimeLabel turns "com.apple.CoreSimulator.SimRuntime.iOS-18-6"
// into "iOS 18.6".
func simRuntimeLabel(id string) string {
	s := strings.TrimPrefix(id, "com.apple.CoreSimulator.SimRuntime.")
	platform, version, ok := strings.Cut(s, "-")
	if !ok {
		return s
	}
	return platform + " " + strings.ReplaceAll(version, "-", ".")
}

func (a *SimulatorAdapter) List() ([]Info, error) {
	ctx, cancel := context.WithTimeout(context.Background(), simctlTimeout)
	defer cancel()
	return BootedSimulators(ctx)
}

func (a *SimulatorAdapter) State(id string) (State, error) {
	return State{Notes: []string{"simulator: battery, thermal and storage are not applicable"}}, nil
}

func (a *SimulatorAdapter) Screenshot(id string) ([]byte, error) {
	tmp, err := os.CreateTemp("", "spyder-simshot-*.png")
	if err != nil {
		return nil, fmt.Errorf("simctl screenshot: create temp: %w", err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	if _, err := simctlQuick("io", id, "screenshot", tmp.Name()); err != nil {
		return nil, err
	}
	return os.ReadFile(tmp.Name())
}

// simApp is one entry of `simctl listapps`, converted to JSON.
type simApp struct {
	BundleID   string `json:"CFBundleIdentifier"`
	Display    string `json:"CFBundleDisplayName"`
	Name       string `json:"CFBundleName"`
	Executable string `json:"CFBundleExecutable"`
	Version    string `json:"CFBundleShortVersionString"`
	Type       string `json:"ApplicationType"` // "User" or "System"
}

func (a *SimulatorAdapter) apps(ctx context.Context, id string) (map[string]simApp, error) {
	out, err := runSimctl(ctx, "listapps", id)
	if err != nil {
		return nil, err
	}
	// listapps prints an old-style plist; plutil converts it to JSON.
	conv := exec.CommandContext(ctx, "plutil", "-convert", "json", "-o", "-", "-")
	conv.Stdin = bytes.NewReader(out)
	js, err := conv.Output()
	if err != nil {
		return nil, fmt.Errorf("simctl listapps: convert plist: %w", err)
	}
	return parseSimApps(js)
}

func parseSimApps(js []byte) (map[string]simApp, error) {
	var apps map[string]simApp
	if err := json.Unmarshal(js, &apps); err != nil {
		return nil, fmt.Errorf("simctl listapps: parse: %w", err)
	}
	return apps, nil
}

// ListApps lists user-installed apps, matching what IOSAdapter lists
// on a physical device.
func (a *SimulatorAdapter) ListApps(id string) ([]AppInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), simctlTimeout)
	defer cancel()
	return a.ListAppsContext(ctx, id)
}

func (a *SimulatorAdapter) ListAppsContext(ctx context.Context, id string) ([]AppInfo, error) {
	apps, err := a.apps(ctx, id)
	if err != nil {
		return nil, err
	}
	out := []AppInfo{}
	for bundle, app := range apps {
		if app.Type != "User" {
			continue
		}
		name := app.Display
		if name == "" {
			name = app.Name
		}
		out = append(out, AppInfo{BundleID: bundle, Name: name, Executable: app.Executable, Version: app.Version})
	}
	return out, nil
}

func (a *SimulatorAdapter) ResolveExecutable(id, bundleID string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), simctlTimeout)
	defer cancel()
	apps, err := a.apps(ctx, id)
	if err != nil {
		return "", false, err
	}
	app, ok := apps[bundleID]
	if !ok {
		return "", false, nil
	}
	return app.Executable, true, nil
}

func (a *SimulatorAdapter) LaunchApp(id, bundleID string, env map[string]string) error {
	ctx, cancel := context.WithTimeout(context.Background(), simctlTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "xcrun", "simctl", "launch", id, bundleID)
	if len(env) > 0 {
		// simctl forwards SIMCTL_CHILD_<KEY>=<VALUE> entries from its
		// own environment to the launched process as <KEY>=<VALUE>.
		cmd.Env = os.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, "SIMCTL_CHILD_"+k+"="+v)
		}
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("simctl launch: %w\n%s", err, truncate(string(out), 200))
	}
	return nil
}

func (a *SimulatorAdapter) TerminateApp(id, bundleID string) error {
	if _, err := simctlQuick("terminate", id, bundleID); err != nil {
		if strings.Contains(err.Error(), "found nothing to terminate") {
			return fmt.Errorf("%s is not running", bundleID)
		}
		return err
	}
	return nil
}

// AppPID reads the app's pid from the simulator's launchd, where a
// running app is labelled UIKitApplication:<bundle-id>[...].
func (a *SimulatorAdapter) AppPID(id, bundleID string) (int, error) {
	out, err := simctlQuick("spawn", id, "launchctl", "list")
	if err != nil {
		return 0, err
	}
	if pid, ok := parseSimLaunchctlPID(string(out), bundleID); ok {
		return pid, nil
	}
	return 0, fmt.Errorf("%s is not running", bundleID)
}

func parseSimLaunchctlPID(list, bundleID string) (int, bool) {
	prefix := "UIKitApplication:" + bundleID + "["
	for _, line := range strings.Split(list, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || !strings.HasPrefix(fields[2], prefix) {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			return 0, false // "-": loaded but not running
		}
		return pid, true
	}
	return 0, false
}

func (a *SimulatorAdapter) InstallApp(id, path string) error {
	if id == "" || path == "" {
		return errors.New("device id and path are required")
	}
	if strings.HasSuffix(strings.ToLower(path), ".ipa") {
		return errors.New("simulators install .app bundles built for the simulator, not .ipa archives")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	_, err := runSimctl(ctx, "install", id, path)
	return err
}

func (a *SimulatorAdapter) UninstallApp(id, bundleID string) error {
	_, err := simctlQuick("uninstall", id, bundleID)
	return err
}

func (a *SimulatorAdapter) Rotate(id, orientation string) error {
	arg, ok := iosSimOrientations[orientation]
	if !ok {
		return fmt.Errorf("unknown orientation %q", orientation)
	}
	_, err := simctlQuick("io", id, "rotate", arg)
	return err
}

func (a *SimulatorAdapter) Crashes(id string, since time.Time, process string) ([]CrashReport, error) {
	return nil, errors.New("crash reports are not collected from simulators; see ~/Library/Logs/DiagnosticReports on the host")
}

func (a *SimulatorAdapter) ApplyNetwork(string, network.NetworkProfile) error {
	return errors.New("network shaping is not supported on simulators")
}

func (a *SimulatorAdapter) ClearNetwork(string) error {
	return errors.New("network shaping is not supported on simulators")
}

func (a *SimulatorAdapter) LogRange(id string, filter LogFilter, since, until time.Time) ([]LogLine, error) {
	return nil, errors.New("log range is not supported on simulators; use `xcrun simctl spawn <udid> log show`")
}

func (a *SimulatorAdapter) LogStream(ctx context.Context, id string, filter LogFilter, out chan<- LogLine) error {
	return errors.New("log stream is not supported on simulators; use `xcrun simctl spawn <udid> log stream`")
}

// StartRecording starts `xcrun simctl io <id> recordVideo <dest>` in the
// background. The process runs until StopRecording sends it SIGINT.
func (a *SimulatorAdapter) StartRecording(id, dest string) (func() error, int, error) {
	if id == "" {
		return nil, 0, errors.New("simctl: device identifier is empty")
	}
	if dest == "" {
		return nil, 0, errors.New("simctl: dest path is required")
	}

	// Remove the file if it already exists so simctl doesn't complain.
	_ = os.Remove(dest)

	cmd := exec.Command("xcrun", "simctl", "io", id, "recordVideo", dest)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, 0, fmt.Errorf("simctl recordVideo: %w", err)
	}

	pid := cmd.Process.Pid
	stopFn := func() error {
		// SIGINT causes simctl to flush and close the mp4 cleanly.
		if err := cmd.Process.Signal(syscall.SIGINT); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		// Wait up to 10 s for clean shutdown so the caller can rely on the file.
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		case <-done:
		}
		return nil
	}

	return stopFn, pid, nil
}

// StopRecording sends SIGINT to the simctl process and waits for it
// to flush and exit. pid is the value returned by StartRecording.
func (a *SimulatorAdapter) StopRecording(id string, pid int) error {
	if pid <= 0 {
		return fmt.Errorf("simctl StopRecording: invalid pid %d", pid)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("simctl StopRecording: find process %d: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGINT); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("simctl StopRecording: signal %d: %w", pid, err)
	}
	return nil
}

var _ Adapter = (*SimulatorAdapter)(nil)

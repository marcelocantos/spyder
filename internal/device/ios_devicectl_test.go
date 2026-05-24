// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/marcelocantos/spyder/internal/devicectl"
)

// stubDevicectl returns an ExecFunc that routes by subcommand label,
// returning the supplied JSON per op. A bundle-id-filtered "device info
// apps" call (ResolveBundleApp) is distinguished from a plain ListApps by
// the presence of --bundle-id in args.
type devicectlStub struct {
	apps       string // info apps (ListApps and bundle-filtered)
	processes  string // info processes
	launch     string // process launch
	devices    string // list devices
	details    string // info details
	detailsErr error  // error to return from info details
	launchErr  error  // error to return from launch
	lastArgs   []string
}

func (s *devicectlStub) exec(_ context.Context, _ int, sub string, args []string) ([]byte, error) {
	s.lastArgs = args
	switch sub {
	case "device info apps":
		return []byte(s.apps), nil
	case "device info processes":
		return []byte(s.processes), nil
	case "device info details":
		if s.detailsErr != nil {
			return nil, s.detailsErr
		}
		return []byte(s.details), nil
	case "list devices":
		return []byte(s.devices), nil
	case "device process launch":
		if s.launchErr != nil {
			return nil, s.launchErr
		}
		return []byte(s.launch), nil
	case "device process signal", "device install app", "device uninstall app":
		return []byte("{}"), nil
	}
	return []byte("{}"), nil
}

func adapterWithStub(s *devicectlStub) *IOSAdapter {
	a := NewIOSAdapter()
	a.dctl = devicectl.NewWithExec(0, s.exec)
	return a
}

const fxApps = `{"result":{"apps":[
  {"bundleIdentifier":"com.minicades.MultiMaze","name":"MultiMaze","version":"1.4.2","removable":true,
   "url":"file:///private/var/containers/Bundle/Application/ABC/MultiMaze.app/"}
]}}`

const fxProcs = `{"result":{"runningProcesses":[
  {"processIdentifier":1,"executable":"file:///sbin/launchd"},
  {"processIdentifier":742,"executable":"file:///private/var/containers/Bundle/Application/ABC/MultiMaze.app/MultiMaze"}
]}}`

func TestIOSListAppsMapsExecutable(t *testing.T) {
	a := adapterWithStub(&devicectlStub{apps: fxApps})
	apps, err := a.ListApps("UDID1")
	if err != nil {
		t.Fatalf("ListApps: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("want 1 app, got %d", len(apps))
	}
	if apps[0].BundleID != "com.minicades.MultiMaze" {
		t.Errorf("BundleID = %q", apps[0].BundleID)
	}
	// Executable is derived from the .app folder name (devicectl has no
	// CFBundleExecutable).
	if apps[0].Executable != "MultiMaze" {
		t.Errorf("Executable = %q, want MultiMaze", apps[0].Executable)
	}
}

func TestIOSResolveExecutable(t *testing.T) {
	a := adapterWithStub(&devicectlStub{apps: fxApps})
	exe, installed, err := a.ResolveExecutable("UDID1", "com.minicades.MultiMaze")
	if err != nil || !installed {
		t.Fatalf("ResolveExecutable: exe=%q installed=%v err=%v", exe, installed, err)
	}
	if exe != "MultiMaze" {
		t.Errorf("exe = %q", exe)
	}

	// Not installed → ("", false, nil).
	a2 := adapterWithStub(&devicectlStub{apps: `{"result":{"apps":[]}}`})
	exe, installed, err = a2.ResolveExecutable("UDID1", "com.nope.Absent")
	if err != nil || installed || exe != "" {
		t.Errorf("absent: exe=%q installed=%v err=%v", exe, installed, err)
	}
}

func TestIOSAppPIDFound(t *testing.T) {
	a := adapterWithStub(&devicectlStub{apps: fxApps, processes: fxProcs})
	pid, err := a.AppPID("UDID1", "com.minicades.MultiMaze")
	if err != nil {
		t.Fatalf("AppPID: %v", err)
	}
	if pid != 742 {
		t.Errorf("pid = %d, want 742", pid)
	}
}

func TestIOSAppPIDNotRunning(t *testing.T) {
	// App installed but no matching process.
	a := adapterWithStub(&devicectlStub{
		apps:      fxApps,
		processes: `{"result":{"runningProcesses":[{"processIdentifier":1,"executable":"file:///sbin/launchd"}]}}`,
	})
	_, err := a.AppPID("UDID1", "com.minicades.MultiMaze")
	if err == nil || !strings.HasPrefix(err.Error(), "app not running") {
		t.Errorf("want 'app not running' sentinel, got %v", err)
	}
}

func TestIOSAppPIDNotInstalled(t *testing.T) {
	a := adapterWithStub(&devicectlStub{apps: `{"result":{"apps":[]}}`, processes: fxProcs})
	_, err := a.AppPID("UDID1", "com.nope.Absent")
	if err == nil || !strings.Contains(err.Error(), "app not installed") {
		t.Errorf("want 'app not installed', got %v", err)
	}
}

func TestIOSTerminateAppNoopWhenNotRunning(t *testing.T) {
	a := adapterWithStub(&devicectlStub{
		apps:      fxApps,
		processes: `{"result":{"runningProcesses":[]}}`,
	})
	// Not running → TerminateApp is a no-op success.
	if err := a.TerminateApp("UDID1", "com.minicades.MultiMaze"); err != nil {
		t.Errorf("TerminateApp should no-op when not running, got %v", err)
	}
}

func TestIOSLaunchAppNotInstalled(t *testing.T) {
	// Launch fails AND the bundle resolves as not-installed → clean sentinel.
	a := adapterWithStub(&devicectlStub{
		apps:      `{"result":{"apps":[]}}`,
		launchErr: errors.New("devicectl boom"),
	})
	err := a.LaunchApp("UDID1", "com.nope.Absent")
	if err == nil || !strings.Contains(err.Error(), "app not installed") {
		t.Errorf("want 'app not installed', got %v", err)
	}
}

func TestIOSLaunchAppArgsAndSuccess(t *testing.T) {
	s := &devicectlStub{launch: `{"result":{"process":{"processIdentifier":99}}}`}
	a := adapterWithStub(s)
	if err := a.LaunchApp("UDID1", "com.minicades.MultiMaze"); err != nil {
		t.Fatalf("LaunchApp: %v", err)
	}
	if !slices.Contains(s.lastArgs, "com.minicades.MultiMaze") {
		t.Errorf("launch args missing bundle id: %v", s.lastArgs)
	}
}

func TestIOSInstallUninstallRoute(t *testing.T) {
	s := &devicectlStub{}
	a := adapterWithStub(s)
	if err := a.InstallApp("UDID1", "/tmp/App.ipa"); err != nil {
		t.Fatalf("InstallApp: %v", err)
	}
	if !slices.Contains(s.lastArgs, "/tmp/App.ipa") {
		t.Errorf("install args missing path: %v", s.lastArgs)
	}
	if err := a.UninstallApp("UDID1", "com.minicades.MultiMaze"); err != nil {
		t.Fatalf("UninstallApp: %v", err)
	}
	if !slices.Contains(s.lastArgs, "com.minicades.MultiMaze") {
		t.Errorf("uninstall args missing bundle id: %v", s.lastArgs)
	}
}

// fxDevices: WIRED-IOS connected (blank model → enrichment target),
// OFF-IOS unavailable (must be dropped even if usbmux sees it),
// MAC connected but non-iOS (filtered).
const fxDevices = `{"result":{"devices":[
  {"identifier":"id-wired","hardwareProperties":{"udid":"WIRED-IOS","platform":"iOS","marketingName":"","productType":""},
   "deviceProperties":{"name":"Pippa","osVersionNumber":"17.5"},
   "connectionProperties":{"tunnelState":"connected","transportType":"wired"}},
  {"identifier":"id-off","hardwareProperties":{"udid":"OFF-IOS","platform":"iOS"},
   "deviceProperties":{"name":"spare"},
   "connectionProperties":{"tunnelState":"unavailable","transportType":"wired"}},
  {"identifier":"id-mac","hardwareProperties":{"udid":"MAC","platform":"macOS"},
   "deviceProperties":{"name":"laptop"},
   "connectionProperties":{"tunnelState":"connected","transportType":"wired"}}
]}}`

func adapterForList(devicesJSON string, usbmux []Info) *IOSAdapter {
	s := &devicectlStub{devices: devicesJSON}
	a := NewIOSAdapter()
	a.dctl = devicectl.NewWithExec(0, s.exec)
	a.usbmuxList = func() []Info { return usbmux }
	return a
}

func TestIOSListDevicectlPrimary(t *testing.T) {
	usbmux := []Info{
		{UUID: "WIRED-IOS", Platform: "ios", Name: "Pippa-usbmux", Model: "iPad8,9"}, // enriches blank Model only
		{UUID: "OFF-IOS", Platform: "ios", Name: "spare"},                            // devicectl says unavailable → drop
		{UUID: "USB-ONLY", Platform: "ios", Name: "old ipad", Model: "iPad5,1", OS: "iOS 12.0"},
	}
	devices, err := adapterForList(fxDevices, usbmux).List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byUDID := map[string]Info{}
	for _, d := range devices {
		byUDID[d.UUID] = d
	}
	if len(devices) != 2 {
		t.Fatalf("want 2 devices (WIRED-IOS + USB-ONLY), got %d: %+v", len(devices), devices)
	}
	if _, ok := byUDID["OFF-IOS"]; ok {
		t.Error("OFF-IOS (devicectl unavailable) should be dropped even though usbmux saw it")
	}
	if _, ok := byUDID["MAC"]; ok {
		t.Error("non-iOS macOS device should be filtered")
	}
	w := byUDID["WIRED-IOS"]
	if w.Name != "Pippa" { // devicectl identity wins over usbmux
		t.Errorf("WIRED-IOS Name = %q, want devicectl's 'Pippa'", w.Name)
	}
	if w.Model != "iPad8,9" { // blank in devicectl → enriched from usbmux
		t.Errorf("WIRED-IOS Model = %q, want usbmux enrichment 'iPad8,9'", w.Model)
	}
	if w.OS != "iOS 17.5" {
		t.Errorf("WIRED-IOS OS = %q", w.OS)
	}
	if u, ok := byUDID["USB-ONLY"]; !ok || u.Model != "iPad5,1" {
		t.Errorf("USB-ONLY (devicectl-unknown) should be included: %+v", u)
	}
}

func TestIOSListUsbmuxWedgedStillReturnsDevicectl(t *testing.T) {
	// usbmuxList returns nil — simulating the hang-guard firing on a wedged
	// usbmuxd. devicectl's connected devices must still come through.
	devices, err := adapterForList(fxDevices, nil).List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(devices) != 1 || devices[0].UUID != "WIRED-IOS" {
		t.Fatalf("want only WIRED-IOS from devicectl, got %+v", devices)
	}
}

func TestStateDevicectlFallback(t *testing.T) {
	const synthUDID = "NO-SUCH-DEVICE-FALLBACK"

	// Lockdown (go-ios) fails for a synthetic UDID; devicectl details
	// succeeds → State returns a degraded result with an explanatory note,
	// not an error.
	s := &devicectlStub{details: `{"result":{"deviceProperties":{"name":"Pippa","osVersionNumber":"17.5"},"hardwareProperties":{"udid":"` + synthUDID + `","platform":"iOS"}}}`}
	a := NewIOSAdapter()
	a.dctl = devicectl.NewWithExec(0, s.exec)
	st, err := a.State(synthUDID)
	if err != nil {
		t.Fatalf("State should degrade, not error, when devicectl fallback works: %v", err)
	}
	foundNote := false
	for _, n := range st.Notes {
		if strings.Contains(n, "CoreDevice fallback") {
			foundNote = true
		}
	}
	if !foundNote {
		t.Errorf("expected a CoreDevice-fallback note; got %v", st.Notes)
	}

	// Both paths fail → State returns a combined error.
	s2 := &devicectlStub{detailsErr: errors.New("devicectl no device")}
	a2 := NewIOSAdapter()
	a2.dctl = devicectl.NewWithExec(0, s2.exec)
	if _, err := a2.State(synthUDID + "-2"); err == nil {
		t.Error("State should error when both lockdown and devicectl fallback fail")
	}
}

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
	apps      string // info apps (ListApps and bundle-filtered)
	processes string // info processes
	launch    string // process launch
	launchErr error  // error to return from launch
	lastArgs  []string
}

func (s *devicectlStub) exec(_ context.Context, _ int, sub string, args []string) ([]byte, error) {
	s.lastArgs = args
	switch sub {
	case "device info apps":
		return []byte(s.apps), nil
	case "device info processes":
		return []byte(s.processes), nil
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

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package devicectl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func TestParseDevices(t *testing.T) {
	got, err := parseDevices(readFixture(t, "list_devices.json"))
	if err != nil {
		t.Fatalf("parseDevices: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 devices, got %d", len(got))
	}

	// First: unavailable iPhone, marketingName wins as Model.
	if got[0].UDID != "00008110-001C10590C28201E" {
		t.Errorf("dev0 UDID = %q", got[0].UDID)
	}
	if got[0].Model != "iPhone 14" {
		t.Errorf("dev0 Model = %q, want iPhone 14", got[0].Model)
	}
	if got[0].Connected() {
		t.Errorf("dev0 (unavailable) should not be Connected")
	}

	// Second: connected wired iPad.
	if !got[1].Connected() {
		t.Errorf("dev1 (connected/wired) should be Connected")
	}
	if got[1].OSVersion != "17.5" {
		t.Errorf("dev1 OSVersion = %q", got[1].OSVersion)
	}

	// Third: no UDID, no marketingName → Model falls back to productType,
	// localNetwork transport is not Connected.
	if got[2].Model != "iPhone15,2" {
		t.Errorf("dev2 Model = %q, want productType fallback", got[2].Model)
	}
	if got[2].Connected() {
		t.Errorf("dev2 (localNetwork) should not be Connected")
	}
	if got[2].Identifier != "2222CAFE-0000-5000-9000-000000000002" {
		t.Errorf("dev2 Identifier = %q", got[2].Identifier)
	}
}

func TestParseApps(t *testing.T) {
	got, err := parseApps(readFixture(t, "info_apps.json"))
	if err != nil {
		t.Fatalf("parseApps: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 apps, got %d", len(got))
	}
	if got[0].BundleID != "com.minicades.MultiMaze" {
		t.Errorf("app0 BundleID = %q", got[0].BundleID)
	}
	if got[0].AppFolder != "MultiMaze.app" {
		t.Errorf("app0 AppFolder = %q, want MultiMaze.app", got[0].AppFolder)
	}
	if got[0].Version != "1.4.2" {
		t.Errorf("app0 Version = %q", got[0].Version)
	}
	// Second app's URL has a percent-encoded space; fileURLToPath decodes it.
	if got[1].AppFolder != "Field Notes.app" {
		t.Errorf("app1 AppFolder = %q, want decoded 'Field Notes.app'", got[1].AppFolder)
	}
	if got[1].BundlePath != "/private/var/containers/Bundle/Application/AAAA1111-2222-3333-4444-555566667777/Field Notes.app" {
		t.Errorf("app1 BundlePath = %q", got[1].BundlePath)
	}
}

func TestParseProcesses(t *testing.T) {
	got, err := parseProcesses(readFixture(t, "info_processes.json"))
	if err != nil {
		t.Fatalf("parseProcesses: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 processes, got %d", len(got))
	}
	if got[0].PID != 1 || got[0].Path != "/sbin/launchd" {
		t.Errorf("proc0 = %+v", got[0])
	}
	want := "/private/var/containers/Bundle/Application/9C1F0E2A-1111-2222-3333-444455556666/MultiMaze.app/MultiMaze"
	if got[1].Path != want {
		t.Errorf("proc1 Path = %q", got[1].Path)
	}
	// Process with no executable field → empty Path, pid still parsed.
	if got[2].PID != 98 || got[2].Path != "" {
		t.Errorf("proc2 = %+v, want pid 98 empty path", got[2])
	}
}

func TestParseDetails(t *testing.T) {
	got, err := parseDetails(readFixture(t, "info_details.json"))
	if err != nil {
		t.Fatalf("parseDetails: %v", err)
	}
	want := Details{
		UDID:      "00008103-000D39301A6A201E",
		Name:      "Pippa",
		Model:     "iPad Pro (11-inch)",
		OSVersion: "17.5",
		Platform:  "iOS",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseDetails = %+v, want %+v", got, want)
	}
}

func TestParseLaunch(t *testing.T) {
	pid, err := parseLaunch(readFixture(t, "process_launch.json"))
	if err != nil {
		t.Fatalf("parseLaunch: %v", err)
	}
	if pid != 1337 {
		t.Errorf("parseLaunch pid = %d, want 1337", pid)
	}
}

func TestParseInvalidJSON(t *testing.T) {
	if _, err := parseApps([]byte("not json")); err == nil {
		t.Error("parseApps should error on invalid JSON")
	}
	if _, err := parseDevices([]byte("{")); err == nil {
		t.Error("parseDevices should error on invalid JSON")
	}
}

// stubClient returns a Client whose exec captures the arg vector it was
// given and returns the supplied document (or error).
func stubClient(doc []byte, retErr error) (*Client, *[]string) {
	var gotArgs []string
	c := &Client{
		timeoutSeconds: DefaultTimeoutSeconds,
		exec: func(_ context.Context, _ int, _ string, args []string) ([]byte, error) {
			gotArgs = args
			return doc, retErr
		},
	}
	return c, &gotArgs
}

func TestListAppsArgs(t *testing.T) {
	c, args := stubClient(readFixture(t, "info_apps.json"), nil)
	apps, err := c.ListApps(context.Background(), "UDID1")
	if err != nil {
		t.Fatalf("ListApps: %v", err)
	}
	if len(apps) != 2 {
		t.Fatalf("want 2 apps, got %d", len(apps))
	}
	want := []string{"device", "info", "apps", "--device", "UDID1"}
	if !reflect.DeepEqual(*args, want) {
		t.Errorf("ListApps args = %v, want %v", *args, want)
	}
}

func TestResolveBundleApp(t *testing.T) {
	c, args := stubClient(readFixture(t, "info_apps.json"), nil)
	app, ok, err := c.ResolveBundleApp(context.Background(), "UDID1", "com.minicades.MultiMaze")
	if err != nil {
		t.Fatalf("ResolveBundleApp: %v", err)
	}
	if !ok {
		t.Fatal("expected app found")
	}
	if app.AppFolder != "MultiMaze.app" {
		t.Errorf("AppFolder = %q", app.AppFolder)
	}
	want := []string{"device", "info", "apps", "--device", "UDID1", "--include-all-apps", "--bundle-id", "com.minicades.MultiMaze"}
	if !reflect.DeepEqual(*args, want) {
		t.Errorf("args = %v, want %v", *args, want)
	}

	// A bundle not present in the doc → (false, nil).
	c2, _ := stubClient(readFixture(t, "info_apps.json"), nil)
	_, ok, err = c2.ResolveBundleApp(context.Background(), "UDID1", "com.nope.Absent")
	if err != nil || ok {
		t.Errorf("absent bundle: ok=%v err=%v, want false,nil", ok, err)
	}
}

func TestLaunchAppArgs(t *testing.T) {
	c, args := stubClient(readFixture(t, "process_launch.json"), nil)
	pid, err := c.LaunchApp(context.Background(), "UDID1", "com.minicades.MultiMaze",
		&LaunchArgs{TerminateExisting: true, Args: []string{"--demo"}})
	if err != nil {
		t.Fatalf("LaunchApp: %v", err)
	}
	if pid != 1337 {
		t.Errorf("pid = %d", pid)
	}
	want := []string{"device", "process", "launch", "--device", "UDID1", "--terminate-existing", "com.minicades.MultiMaze", "--demo"}
	if !reflect.DeepEqual(*args, want) {
		t.Errorf("args = %v, want %v", *args, want)
	}
}

func TestSignalProcessArgs(t *testing.T) {
	c, args := stubClient([]byte("{}"), nil)
	if err := c.SignalProcess(context.Background(), "UDID1", 1337, "SIGKILL"); err != nil {
		t.Fatalf("SignalProcess: %v", err)
	}
	want := []string{"device", "process", "signal", "--device", "UDID1", "--pid", "1337", "--signal", "SIGKILL"}
	if !reflect.DeepEqual(*args, want) {
		t.Errorf("args = %v, want %v", *args, want)
	}
}

func TestInstallUninstallArgs(t *testing.T) {
	c, args := stubClient([]byte("{}"), nil)
	if err := c.InstallApp(context.Background(), "UDID1", "/tmp/App.ipa"); err != nil {
		t.Fatalf("InstallApp: %v", err)
	}
	if want := []string{"device", "install", "app", "--device", "UDID1", "/tmp/App.ipa"}; !reflect.DeepEqual(*args, want) {
		t.Errorf("install args = %v, want %v", *args, want)
	}

	c2, args2 := stubClient([]byte("{}"), nil)
	if err := c2.UninstallApp(context.Background(), "UDID1", "com.minicades.MultiMaze"); err != nil {
		t.Fatalf("UninstallApp: %v", err)
	}
	if want := []string{"device", "uninstall", "app", "--device", "UDID1", "com.minicades.MultiMaze"}; !reflect.DeepEqual(*args2, want) {
		t.Errorf("uninstall args = %v, want %v", *args2, want)
	}
}

func TestExecErrorPropagates(t *testing.T) {
	sentinel := &CommandError{Subcommand: "device info apps", ExitCode: 1, Stderr: "boom"}
	c, _ := stubClient(nil, sentinel)
	_, err := c.ListApps(context.Background(), "UDID1")
	var ce *CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("want *CommandError, got %T (%v)", err, err)
	}
	if ce.ExitCode != 1 || ce.Stderr != "boom" {
		t.Errorf("CommandError = %+v", ce)
	}
}

func TestEmptyArgsRejected(t *testing.T) {
	c := New()
	if _, err := c.ListApps(context.Background(), ""); err == nil {
		t.Error("ListApps with empty udid should error")
	}
	if _, err := c.LaunchApp(context.Background(), "U", "", nil); err == nil {
		t.Error("LaunchApp with empty bundleID should error")
	}
	if err := c.SignalProcess(context.Background(), "U", 0, "SIGKILL"); err == nil {
		t.Error("SignalProcess with pid 0 should error")
	}
}

func TestCommandErrorUnwrap(t *testing.T) {
	ce := &CommandError{Subcommand: "x", Err: context.DeadlineExceeded}
	if !errors.Is(ce, context.DeadlineExceeded) {
		t.Error("CommandError should unwrap to its Err")
	}
}

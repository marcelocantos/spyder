// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"strings"
	"testing"

	"github.com/danielpaulus/go-ios/ios/appservice"
	"github.com/danielpaulus/go-ios/ios/installationproxy"
)

type identityProcesses []appservice.Process

func (p identityProcesses) ListProcesses() ([]appservice.Process, error) {
	return []appservice.Process(p), nil
}
func identityProbe(processes []appservice.Process) *IOSAdapter {
	return NewIOSProcessProbeAdapter(IOSProcessProbe{InstalledExecutable: func(string, string) (string, error) {
		return "/var/containers/Bundle/Application/EXPECTED/StockCars.app/StockCars", nil
	}, MajorVersion: func(string) (int, error) { return 27, nil }, Acquire: func(string) (IOSProcessListConn, func(), error) { return identityProcesses(processes), func() {}, nil }, Invalidate: func(string) {}})
}
func TestAppPIDModern_RejectsOtherContainerAndNestedHelper(t *testing.T) {
	for _, processes := range [][]appservice.Process{
		{{Pid: 222, Path: "/var/containers/Bundle/Application/OTHER/StockCars.app/StockCars"}},
		{{Pid: 333, Path: "/var/containers/Bundle/Application/EXPECTED/StockCars.app/PlugIns/Helper.appex/StockCars"}},
	} {
		pid, err := identityProbe(processes).AppPIDContext(context.Background(), "dut", "com.fixture.stockcars")
		if pid != 0 || err == nil || !strings.HasPrefix(err.Error(), "app not running:") {
			t.Fatalf("wrong process was accepted pid=%d err=%v", pid, err)
		}
	}
}
func TestAppPIDModern_UniqueFullExecutableIncludesLocalURLAndRootAlias(t *testing.T) {
	processes := []appservice.Process{{Pid: 222, Path: "/var/containers/Bundle/Application/OTHER/StockCars.app/StockCars"}, {Pid: 42, Path: "file:///private/var/containers/Bundle/Application/EXPECTED/StockCars.app/StockCars"}}
	pid, err := identityProbe(processes).AppPIDContext(context.Background(), "dut", "com.fixture.stockcars")
	if pid != 42 || err != nil {
		t.Fatalf("pid=%d err=%v", pid, err)
	}
}
func TestAppPIDModern_DuplicateOrInvalidMainPIDRejects(t *testing.T) {
	const path = "/var/containers/Bundle/Application/EXPECTED/StockCars.app/StockCars"
	for _, processes := range [][]appservice.Process{{{Pid: 0, Path: path}}, {{Pid: -1, Path: path}}, {{Pid: 42, Path: path}, {Pid: 42, Path: path}}, {{Pid: 42, Path: path}, {Pid: 43, Path: path}}} {
		if pid, err := identityProbe(processes).AppPIDContext(context.Background(), "dut", "com.fixture.stockcars"); pid != 0 || err == nil {
			t.Fatalf("pid=%d err=%v", pid, err)
		}
	}
}
func TestInstalledIOSExecutable_RequiresUniqueExactBundleAndMainExecutable(t *testing.T) {
	fixture := installationproxy.AppInfo{"CFBundleIdentifier": "com.fixture.stockcars", "Path": "file:///private/var/containers/Bundle/Application/EXPECTED/StockCars.app", "CFBundleExecutable": "StockCars"}
	path, err := installedIOSExecutable([]installationproxy.AppInfo{{"CFBundleIdentifier": "other", "Path": "/Applications/StockCars.app", "CFBundleExecutable": "StockCars"}, fixture}, "com.fixture.stockcars")
	if err != nil || path != "/var/containers/Bundle/Application/EXPECTED/StockCars.app/StockCars" {
		t.Fatalf("path=%q err=%v", path, err)
	}
	for _, key := range []string{"Path", "CFBundleExecutable"} {
		altered := installationproxy.AppInfo{}
		for k, v := range fixture {
			altered[k] = v
		}
		delete(altered, key)
		if _, err := installedIOSExecutable([]installationproxy.AppInfo{altered}, "com.fixture.stockcars"); err == nil {
			t.Fatalf("missing %s accepted", key)
		}
	}
	if _, err := installedIOSExecutable([]installationproxy.AppInfo{fixture, fixture}, "com.fixture.stockcars"); err == nil {
		t.Fatal("duplicate bundle accepted")
	}
	absent, err := installedIOSExecutable([]installationproxy.AppInfo{fixture}, "absent")
	if absent != "" || err != nil {
		t.Fatalf("absent=%q err=%v", absent, err)
	}
}
func TestIOSExecutablePath_RejectsNonlocalRelativeAndAmbiguousIdentity(t *testing.T) {
	for _, value := range []string{"StockCars.app/StockCars", "file://foreign-host/Applications/StockCars.app/StockCars", "https://localhost/Applications/StockCars.app/StockCars", "/Applications/../StockCars.app/StockCars", "file:///Applications/StockCars.app/StockCars?other=1", "file:///Applications/StockCars.app/StockCars#other", "/Applications/StockCars.app/StockCars\x00"} {
		if path, err := canonicalIOSExecutablePath(value); err == nil {
			t.Fatalf("path=%q accepted %q", path, value)
		}
	}
	for _, value := range []string{"../Other", "Nested/Other", "", ".", ".."} {
		_, err := installedIOSExecutable([]installationproxy.AppInfo{{"CFBundleIdentifier": "bundle", "Path": "/Applications/StockCars.app", "CFBundleExecutable": value}}, "bundle")
		if err == nil {
			t.Fatalf("executable %q accepted", value)
		}
	}
}
func TestAppPIDModern_BasenameOnlyProbeNoLongerAuthorizesPID(t *testing.T) {
	adapter := NewIOSProcessProbeAdapter(IOSProcessProbe{InstalledFolder: func(string, string) (string, error) { return "StockCars.app", nil }, MajorVersion: func(string) (int, error) { return 27, nil }})
	if _, err := adapter.AppPIDContext(context.Background(), "dut", "bundle"); err == nil {
		t.Fatal("modern basename-only probe accepted")
	}
}

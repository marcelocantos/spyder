// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	goios_ios "github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/installationproxy"
)

// OSVersion is a device's OS release and build identifier.
type OSVersion struct {
	Version string // Android ro.build.version.release; iOS ProductVersion
	Build   string // Android ro.build.id; iOS BuildVersion
}

// AppVersion is what the installed package reports about itself.
type AppVersion struct {
	Name    string // iOS CFBundleName; Android has no cheap label lookup → ""
	Version string // Android versionName; iOS CFBundleShortVersionString
	Build   string // Android versionCode; iOS CFBundleVersion
}

// OSVersionProber is implemented by adapters that can read the device
// OS version (🎯T137 run provenance). Optional: callers type-assert.
type OSVersionProber interface {
	OSVersion(ctx context.Context, id string) (OSVersion, error)
}

// AppVersionProber is implemented by adapters that can read an
// installed app's version. Optional: callers type-assert.
type AppVersionProber interface {
	AppVersion(ctx context.Context, id, bundleID string) (AppVersion, error)
}

// ErrAppNotInstalled is returned by AppVersion when bundleID is absent.
var ErrAppNotInstalled = errors.New("app not installed")

func adbOut(ctx context.Context, serial string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "adb", append([]string{"-s", serial}, args...)...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// OSVersion reads ro.build.version.release and ro.build.id.
func (a *AndroidAdapter) OSVersion(ctx context.Context, id string) (OSVersion, error) {
	if id == "" {
		return OSVersion{}, errors.New("device identifier is empty")
	}
	rel, err := adbOut(ctx, id, "shell", "getprop", "ro.build.version.release")
	if err != nil {
		return OSVersion{}, err
	}
	build, _ := adbOut(ctx, id, "shell", "getprop", "ro.build.id")
	return OSVersion{Version: rel, Build: build}, nil
}

// AppVersion reads versionName/versionCode from `dumpsys package`.
func (a *AndroidAdapter) AppVersion(ctx context.Context, id, pkg string) (AppVersion, error) {
	if id == "" || pkg == "" {
		return AppVersion{}, errors.New("device id and package are required")
	}
	out, err := adbOut(ctx, id, "shell", "dumpsys", "package", pkg)
	if err != nil {
		return AppVersion{}, err
	}
	return parseDumpsysPackage(out, pkg)
}

// parseDumpsysPackage extracts versionName / versionCode for pkg from
// `dumpsys package <pkg>` output. Only the "Package [pkg]" section is
// considered so a shared-user or query section can't leak in.
func parseDumpsysPackage(out, pkg string) (AppVersion, error) {
	var v AppVersion
	in := false
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "Package [") {
			in = strings.HasPrefix(t, "Package ["+pkg+"]")
			continue
		}
		if !in {
			continue
		}
		for _, f := range strings.Fields(t) {
			if val, ok := strings.CutPrefix(f, "versionName="); ok && v.Version == "" {
				v.Version = val
			}
			if val, ok := strings.CutPrefix(f, "versionCode="); ok && v.Build == "" {
				v.Build = val
			}
		}
	}
	if v.Version == "" && v.Build == "" {
		return AppVersion{}, fmt.Errorf("%s: %w", pkg, ErrAppNotInstalled)
	}
	return v, nil
}

// OSVersion reads ProductVersion/BuildVersion over lockdown.
func (a *IOSAdapter) OSVersion(ctx context.Context, id string) (OSVersion, error) {
	if id == "" {
		return OSVersion{}, errors.New("device identifier is empty")
	}
	type res struct {
		v   OSVersion
		err error
	}
	ch := make(chan res, 1)
	go func() {
		dev, err := goios_ios.GetDevice(id)
		if err != nil {
			ch <- res{err: err}
			return
		}
		vals, err := goios_ios.GetValues(dev)
		if err != nil {
			ch <- res{err: err}
			return
		}
		ch <- res{v: OSVersion{Version: vals.Value.ProductVersion, Build: vals.Value.BuildVersion}}
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		return OSVersion{}, ctx.Err()
	}
}

// AppVersion reads CFBundleName / CFBundleShortVersionString /
// CFBundleVersion from the installation proxy.
func (a *IOSAdapter) AppVersion(ctx context.Context, id, bundleID string) (AppVersion, error) {
	if id == "" || bundleID == "" {
		return AppVersion{}, errors.New("device id and bundle_id are required")
	}
	lease, err := a.ipPool.AcquireLease(ctx, id)
	if err != nil {
		return AppVersion{}, fmt.Errorf("installation_proxy on %s: %w", id, err)
	}
	defer lease.Release()
	raw, err := lease.Conn.BrowseUserApps()
	if err != nil {
		if ctx.Err() == nil {
			lease.Invalidate()
		}
		return AppVersion{}, err
	}
	for _, app := range raw {
		if app.CFBundleIdentifier() != bundleID {
			continue
		}
		build, _ := app[installationproxy.CFBundleVersion].(string)
		return AppVersion{Name: app.CFBundleName(), Version: app.CFBundleShortVersionString(), Build: build}, nil
	}
	return AppVersion{}, fmt.Errorf("%s: %w", bundleID, ErrAppNotInstalled)
}

var (
	_ OSVersionProber  = (*AndroidAdapter)(nil)
	_ AppVersionProber = (*AndroidAdapter)(nil)
	_ OSVersionProber  = (*IOSAdapter)(nil)
	_ AppVersionProber = (*IOSAdapter)(nil)
)

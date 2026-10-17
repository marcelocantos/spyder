// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/danielpaulus/go-ios/ios/appservice"
	"github.com/danielpaulus/go-ios/ios/installationproxy"
	"github.com/marcelocantos/spyder/internal/goios"
)

// canonicalIOSExecutablePath accepts only an absolute local POSIX path or
// local file URL. /private/var and /var are the documented device root alias;
// no basename, executable-name, nested helper or substring is an identity.
func canonicalIOSExecutablePath(value string) (string, error) {
	if strings.HasPrefix(value, "file:") {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme != "file" || parsed.Host != "" && parsed.Host != "localhost" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
			return "", fmt.Errorf("installed executable is not a local file URL")
		}
		value = parsed.Path
	}
	if !path.IsAbs(value) || strings.ContainsAny(value, "\x00\\") {
		return "", fmt.Errorf("installed executable is not an absolute local path")
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." || part == "." {
			return "", fmt.Errorf("installed executable has ambiguous path components")
		}
	}
	value = path.Clean(value)
	if strings.HasPrefix(value, "/private/var/") {
		value = strings.TrimPrefix(value, "/private")
	}
	return value, nil
}

func installedIOSExecutable(apps []installationproxy.AppInfo, bundleID string) (string, error) {
	var found installationproxy.AppInfo
	for _, app := range apps {
		if app.CFBundleIdentifier() != bundleID {
			continue
		}
		if found != nil {
			return "", fmt.Errorf("installed bundle identity is duplicated")
		}
		found = app
	}
	if found == nil {
		return "", nil
	}
	directory, err := canonicalIOSExecutablePath(found.Path())
	if err != nil {
		return "", err
	}
	executable := found.CFBundleExecutable()
	if !strings.HasSuffix(directory, ".app") || executable == "" || executable == "." || executable == ".." || strings.ContainsAny(executable, "/\\\x00") {
		return "", fmt.Errorf("installed main executable metadata is unavailable/invalid")
	}
	return path.Join(directory, executable), nil
}

func exactIOSAppPID(processes []appservice.Process, executable string) (int, error) {
	expected, err := canonicalIOSExecutablePath(executable)
	if err != nil {
		return 0, err
	}
	pid := 0
	for _, process := range processes {
		observed, err := canonicalIOSExecutablePath(process.Path)
		if err != nil || observed != expected {
			continue
		}
		if process.Pid <= 0 || pid != 0 {
			return 0, fmt.Errorf("main executable process identity is invalid/ambiguous")
		}
		pid = process.Pid
	}
	return pid, nil
}

func (a *IOSAdapter) installedAppExecutableContext(ctx context.Context, id, bundleID string) (string, error) {
	lease, err := a.ipPool.AcquireLease(ctx, id)
	if err != nil {
		return "", fmt.Errorf("installation_proxy: %w", err)
	}
	defer lease.Release()
	ended := goios.LifecyclePhase(ctx, "installed.browse", "device_id", id)
	apps, err := lease.Conn.BrowseAllApps()
	ended(err)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		lease.Invalidate()
		return "", fmt.Errorf("browse apps: %w", err)
	}
	return installedIOSExecutable(apps, bundleID)
}

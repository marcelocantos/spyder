// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package devicectl

import (
	"context"
	"encoding/json"
	"fmt"
)

// LaunchArgs tunes a process launch. The zero value launches the app in the
// foreground (devicectl's default --activate) without disturbing an existing
// instance.
type LaunchArgs struct {
	// TerminateExisting kills any running instance before launching, so the
	// returned pid is guaranteed to be the freshly launched process.
	TerminateExisting bool
	// Args are passed to the app as command-line arguments.
	Args []string
}

// LaunchApp foregrounds an app by bundle id via CoreDevice and returns the
// launched process's pid. args may be nil.
func (c *Client) LaunchApp(ctx context.Context, udid, bundleID string, args *LaunchArgs) (int, error) {
	if udid == "" || bundleID == "" {
		return 0, fmt.Errorf("devicectl LaunchApp: udid and bundleID are required")
	}
	argv := []string{"device", "process", "launch", "--device", udid}
	if args != nil && args.TerminateExisting {
		argv = append(argv, "--terminate-existing")
	}
	argv = append(argv, bundleID)
	if args != nil {
		argv = append(argv, args.Args...)
	}
	data, err := c.run(ctx, "device process launch", argv)
	if err != nil {
		return 0, err
	}
	return parseLaunch(data)
}

func parseLaunch(data []byte) (int, error) {
	var doc struct {
		Result struct {
			Process struct {
				ProcessIdentifier int `json:"processIdentifier"`
			} `json:"process"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return 0, fmt.Errorf("devicectl process launch JSON: %w", err)
	}
	return doc.Result.Process.ProcessIdentifier, nil
}

// SignalProcess sends a signal (e.g. "SIGKILL", "SIGTERM") to a pid on the
// device.
func (c *Client) SignalProcess(ctx context.Context, udid string, pid int, signal string) error {
	if udid == "" || pid <= 0 || signal == "" {
		return fmt.Errorf("devicectl SignalProcess: udid, pid and signal are required")
	}
	_, err := c.run(ctx, "device process signal",
		[]string{"device", "process", "signal",
			"--device", udid,
			"--pid", fmt.Sprintf("%d", pid),
			"--signal", signal})
	return err
}

// InstallApp installs a .app or .ipa bundle onto the device via CoreDevice.
// devicectl handles signing/provisioning against the registered profile.
func (c *Client) InstallApp(ctx context.Context, udid, path string) error {
	if udid == "" || path == "" {
		return fmt.Errorf("devicectl InstallApp: udid and path are required")
	}
	_, err := c.run(ctx, "device install app",
		[]string{"device", "install", "app", "--device", udid, path})
	return err
}

// UninstallApp removes an app by bundle id via CoreDevice.
func (c *Client) UninstallApp(ctx context.Context, udid, bundleID string) error {
	if udid == "" || bundleID == "" {
		return fmt.Errorf("devicectl UninstallApp: udid and bundleID are required")
	}
	_, err := c.run(ctx, "device uninstall app",
		[]string{"device", "uninstall", "app", "--device", udid, bundleID})
	return err
}

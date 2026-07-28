// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// 🎯T111 — host↔device TCP port forward (adb forward parity).

package device

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// PortForward describes one adb TCP forward.
type PortForward struct {
	Serial     string `json:"serial,omitempty"`
	LocalPort  int    `json:"local_port"`
	DevicePort int    `json:"device_port"`
	Spec       string `json:"spec"` // e.g. "tcp:8080 tcp:8080"
}

// ForwardTCP installs `adb -s <id> forward tcp:<local> tcp:<device>`.
// localPort 0 asks adb to pick an ephemeral local port (adb prints it).
func (a *AndroidAdapter) ForwardTCP(id string, localPort, devicePort int) (PortForward, error) {
	if id == "" {
		return PortForward{}, errors.New("device identifier is empty")
	}
	if devicePort <= 0 || devicePort > 65535 {
		return PortForward{}, errors.New("device_port must be in 1..65535")
	}
	if localPort < 0 || localPort > 65535 {
		return PortForward{}, errors.New("local_port must be in 0..65535 (0 = ephemeral)")
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return PortForward{}, fmt.Errorf("adb not found in PATH: %w", err)
	}
	localSpec := "tcp:" + strconv.Itoa(localPort)
	deviceSpec := "tcp:" + strconv.Itoa(devicePort)
	out, stderr, err := androidAdb("-s", id, "forward", localSpec, deviceSpec)
	if err != nil {
		msg := string(stderr) + string(out)
		if isAndroidDeviceNotConnected(msg) {
			return PortForward{}, fmt.Errorf("device not connected: %s", id)
		}
		return PortForward{}, fmt.Errorf("adb forward: %v\n%s", err, truncate(msg, 200))
	}
	// When localPort is 0, adb prints the chosen port on stdout.
	chosen := localPort
	if localPort == 0 {
		s := strings.TrimSpace(string(out))
		if s != "" {
			if p, perr := strconv.Atoi(s); perr == nil {
				chosen = p
			}
		}
		if chosen == 0 {
			return PortForward{}, errors.New("adb forward: ephemeral local port not reported")
		}
	}
	return PortForward{
		Serial:     id,
		LocalPort:  chosen,
		DevicePort: devicePort,
		Spec:       fmt.Sprintf("tcp:%d tcp:%d", chosen, devicePort),
	}, nil
}

// UnforwardTCP removes a host-local forward: `adb forward --remove tcp:<local>`.
func (a *AndroidAdapter) UnforwardTCP(id string, localPort int) error {
	if id == "" {
		return errors.New("device identifier is empty")
	}
	if localPort <= 0 {
		return errors.New("local_port must be positive")
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return fmt.Errorf("adb not found in PATH: %w", err)
	}
	_, stderr, err := androidAdb("-s", id, "forward", "--remove", "tcp:"+strconv.Itoa(localPort))
	if err != nil {
		msg := string(stderr)
		if isAndroidDeviceNotConnected(msg) {
			return fmt.Errorf("device not connected: %s", id)
		}
		return fmt.Errorf("adb forward --remove: %v\n%s", err, truncate(msg, 200))
	}
	return nil
}

// ListForwards returns TCP forwards for this device (or all if id filter empty match).
// Parses `adb forward --list` lines: "<serial> tcp:<local> tcp:<remote>".
func (a *AndroidAdapter) ListForwards(id string) ([]PortForward, error) {
	if _, err := exec.LookPath("adb"); err != nil {
		return nil, fmt.Errorf("adb not found in PATH: %w", err)
	}
	out, stderr, err := androidAdb("forward", "--list")
	if err != nil {
		return nil, fmt.Errorf("adb forward --list: %v\n%s", err, truncate(string(stderr), 200))
	}
	return ParseForwardList(string(out), id)
}

// ParseForwardList parses adb forward --list output. If serialFilter is
// non-empty, only that serial's rows are returned.
func ParseForwardList(out, serialFilter string) ([]PortForward, error) {
	var result []PortForward
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		// serial local remote  — e.g. emulator-5554 tcp:8080 tcp:8080
		if len(fields) < 3 {
			continue
		}
		serial := fields[0]
		if serialFilter != "" && serial != serialFilter {
			continue
		}
		localPort, ok1 := parseTCPPort(fields[1])
		devicePort, ok2 := parseTCPPort(fields[2])
		if !ok1 || !ok2 {
			continue
		}
		result = append(result, PortForward{
			Serial:     serial,
			LocalPort:  localPort,
			DevicePort: devicePort,
			Spec:       fields[1] + " " + fields[2],
		})
	}
	return result, nil
}

func parseTCPPort(spec string) (int, bool) {
	spec = strings.TrimSpace(spec)
	if !strings.HasPrefix(spec, "tcp:") {
		return 0, false
	}
	p, err := strconv.Atoi(strings.TrimPrefix(spec, "tcp:"))
	if err != nil || p <= 0 {
		return 0, false
	}
	return p, true
}

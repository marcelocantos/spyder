// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package simlease

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Sim is one simulator as simctl reports it, plus its uptime when booted.
type Sim struct {
	UDID        string `json:"udid"`
	Name        string `json:"name"`
	State       string `json:"state"` // "Booted", "Shutdown", "Booting", ...
	DeviceType  string `json:"device_type"`
	Runtime     string `json:"runtime"`
	IsAvailable bool   `json:"is_available"`
	// Uptime is how long the simulator has been booted. Zero when it is
	// not booted or the boot time could not be read.
	Uptime time.Duration `json:"-"`
}

// Booted reports whether the simulator is up (or on its way up).
func (s Sim) Booted() bool { return s.State == stateBooted || s.State == stateBooting }

const (
	stateBooted  = "Booted"
	stateBooting = "Booting"
)

// DeviceType and Runtime are the catalogue entries simctl offers.
type DeviceType struct {
	ID   string
	Name string
}

type Runtime struct {
	ID          string
	Name        string // "iOS 18.6"
	Platform    string // "iOS"
	Version     string // "18.6"
	IsAvailable bool
}

// Simctl is the slice of `xcrun simctl` the lease manager uses. Tests
// substitute a fake; production uses XcrunSimctl.
type Simctl interface {
	List(ctx context.Context) ([]Sim, error)
	DeviceTypes(ctx context.Context) ([]DeviceType, error)
	Runtimes(ctx context.Context) ([]Runtime, error)
	Create(ctx context.Context, name, deviceType, runtime string) (string, error)
	// Boot boots the simulator and returns once it has finished booting.
	Boot(ctx context.Context, udid string) error
	Shutdown(ctx context.Context, udid string) error
	Delete(ctx context.Context, udid string) error
}

// XcrunSimctl runs the real `xcrun simctl`.
type XcrunSimctl struct{}

func simctl(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "xcrun", append([]string{"simctl"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("simctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (XcrunSimctl) List(ctx context.Context) ([]Sim, error) {
	out, err := simctl(ctx, "list", "devices", "--json")
	if err != nil {
		return nil, err
	}
	sims, err := ParseDevicesJSON(out)
	if err != nil {
		return nil, err
	}
	uptimes := simUptimes(ctx)
	for i := range sims {
		if sims[i].Booted() {
			sims[i].Uptime = uptimes[sims[i].UDID]
		}
	}
	return sims, nil
}

// ParseDevicesJSON parses `xcrun simctl list devices --json`.
func ParseDevicesJSON(data []byte) ([]Sim, error) {
	var raw struct {
		Devices map[string][]struct {
			UDID        string `json:"udid"`
			Name        string `json:"name"`
			State       string `json:"state"`
			DeviceType  string `json:"deviceTypeIdentifier"`
			IsAvailable bool   `json:"isAvailable"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse simctl devices: %w", err)
	}
	var sims []Sim
	for runtime, entries := range raw.Devices {
		for _, e := range entries {
			sims = append(sims, Sim{
				UDID:        e.UDID,
				Name:        e.Name,
				State:       e.State,
				DeviceType:  e.DeviceType,
				Runtime:     runtime,
				IsAvailable: e.IsAvailable,
			})
		}
	}
	return sims, nil
}

// simUptimes maps a booted simulator's UDID to the age of its
// launchd_sim process, whose argv names the device's data directory.
// Errors yield an empty map: uptime is informational.
func simUptimes(ctx context.Context) map[string]time.Duration {
	out, err := exec.CommandContext(ctx, "ps", "-A", "-o", "etime=,command=").Output()
	if err != nil {
		return nil
	}
	return parseLaunchdSimUptimes(string(out))
}

func parseLaunchdSimUptimes(ps string) map[string]time.Duration {
	const marker = "/CoreSimulator/Devices/"
	m := map[string]time.Duration{}
	for _, line := range strings.Split(ps, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || !strings.HasSuffix(fields[1], "/launchd_sim") && fields[1] != "launchd_sim" {
			continue
		}
		rest := strings.Join(fields[2:], " ")
		i := strings.Index(rest, marker)
		if i < 0 {
			continue
		}
		udid, _, _ := strings.Cut(rest[i+len(marker):], "/")
		if d, ok := parseEtime(fields[0]); ok {
			m[udid] = d
		}
	}
	return m
}

// parseEtime parses ps's elapsed-time format, [[dd-]hh:]mm:ss.
func parseEtime(s string) (time.Duration, bool) {
	var days int
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, false
		}
		days, s = n, rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	total := 0
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, false
		}
		total = total*60 + n
	}
	return time.Duration(days)*24*time.Hour + time.Duration(total)*time.Second, true
}

func (XcrunSimctl) DeviceTypes(ctx context.Context) ([]DeviceType, error) {
	out, err := simctl(ctx, "list", "devicetypes", "--json")
	if err != nil {
		return nil, err
	}
	var raw struct {
		DeviceTypes []struct {
			Identifier string `json:"identifier"`
			Name       string `json:"name"`
		} `json:"devicetypes"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse simctl devicetypes: %w", err)
	}
	types := make([]DeviceType, 0, len(raw.DeviceTypes))
	for _, t := range raw.DeviceTypes {
		types = append(types, DeviceType{ID: t.Identifier, Name: t.Name})
	}
	return types, nil
}

func (XcrunSimctl) Runtimes(ctx context.Context) ([]Runtime, error) {
	out, err := simctl(ctx, "list", "runtimes", "--json")
	if err != nil {
		return nil, err
	}
	var raw struct {
		Runtimes []struct {
			Identifier  string `json:"identifier"`
			Name        string `json:"name"`
			Platform    string `json:"platform"`
			Version     string `json:"version"`
			IsAvailable bool   `json:"isAvailable"`
		} `json:"runtimes"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse simctl runtimes: %w", err)
	}
	rts := make([]Runtime, 0, len(raw.Runtimes))
	for _, r := range raw.Runtimes {
		rts = append(rts, Runtime{ID: r.Identifier, Name: r.Name, Platform: r.Platform, Version: r.Version, IsAvailable: r.IsAvailable})
	}
	return rts, nil
}

func (XcrunSimctl) Create(ctx context.Context, name, deviceType, runtime string) (string, error) {
	out, err := simctl(ctx, "create", name, deviceType, runtime)
	if err != nil {
		return "", err
	}
	udid := strings.TrimSpace(string(out))
	if udid == "" {
		return "", fmt.Errorf("simctl create returned no UDID")
	}
	return udid, nil
}

// Boot boots and waits: `bootstatus -b` boots a shut-down device and
// returns when its boot has finished.
func (XcrunSimctl) Boot(ctx context.Context, udid string) error {
	_, err := simctl(ctx, "bootstatus", udid, "-b")
	return err
}

func (XcrunSimctl) Shutdown(ctx context.Context, udid string) error {
	_, err := simctl(ctx, "shutdown", udid)
	return err
}

func (XcrunSimctl) Delete(ctx context.Context, udid string) error {
	_, err := simctl(ctx, "delete", udid)
	return err
}

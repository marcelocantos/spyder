// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package devicectl

import (
	"context"
	"encoding/json"
	"fmt"
)

// Device is a CoreDevice-known device, flattened from the nested devicectl
// `list devices` document. UDID is the hardware identifier; Identifier is
// the CoreDevice UUID (used as a fallback when UDID is absent).
type Device struct {
	UDID          string
	Identifier    string
	Name          string
	Model         string // marketingName, falling back to productType
	OSVersion     string // e.g. "17.5"
	Platform      string // "iOS", "macOS", …
	TunnelState   string // "connected", "disconnected", "unavailable", "available (unpaired)"
	TransportType string // "wired", "localNetwork", …
}

// Connected reports whether the device is reachable over a wired tunnel —
// the condition spyder treats as "usable for install/launch/etc".
func (d Device) Connected() bool {
	return d.TunnelState == "connected" && d.TransportType == "wired"
}

// App is an installed application as devicectl reports it. AppFolder is the
// basename of the .app bundle (e.g. "MultiMaze.app"), derived from the
// bundle URL — devicectl does not expose CFBundleExecutable, but the bundle
// folder name is what appears in running-process paths and (minus ".app")
// matches the executable name in log streams for the overwhelming majority
// of apps.
type App struct {
	BundleID   string
	Name       string
	Version    string // CFBundleShortVersionString
	AppFolder  string // e.g. "MultiMaze.app"
	BundlePath string // host-style path to the .app bundle
	Removable  bool
	DefaultApp bool
}

// Process is a running process as devicectl reports it. Path is the
// decoded executable path (e.g. /private/var/.../MultiMaze.app/MultiMaze).
type Process struct {
	PID  int
	Path string
}

// Details is the subset of `device info details` spyder consumes. devicectl
// does not surface battery level or charging state, so those remain the
// province of the lockdown path; Details is the usbmuxd-free fallback for
// device identity and OS version.
type Details struct {
	UDID      string
	Name      string
	Model     string
	OSVersion string
	Platform  string
}

// ListDevices returns every device CoreDevice currently knows about,
// regardless of connection state. Callers filter on Device.Connected as
// needed.
func (c *Client) ListDevices(ctx context.Context) ([]Device, error) {
	data, err := c.run(ctx, "list devices", []string{"list", "devices", "--quiet"})
	if err != nil {
		return nil, err
	}
	return parseDevices(data)
}

func parseDevices(data []byte) ([]Device, error) {
	var doc struct {
		Result struct {
			Devices []struct {
				Identifier         string `json:"identifier"`
				HardwareProperties struct {
					UDID          string `json:"udid"`
					MarketingName string `json:"marketingName"`
					ProductType   string `json:"productType"`
					Platform      string `json:"platform"`
				} `json:"hardwareProperties"`
				DeviceProperties struct {
					Name            string `json:"name"`
					OSVersionNumber string `json:"osVersionNumber"`
				} `json:"deviceProperties"`
				ConnectionProperties struct {
					TunnelState   string `json:"tunnelState"`
					TransportType string `json:"transportType"`
				} `json:"connectionProperties"`
			} `json:"devices"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("devicectl list devices JSON: %w", err)
	}
	out := make([]Device, 0, len(doc.Result.Devices))
	for _, d := range doc.Result.Devices {
		model := d.HardwareProperties.MarketingName
		if model == "" {
			model = d.HardwareProperties.ProductType
		}
		out = append(out, Device{
			UDID:          d.HardwareProperties.UDID,
			Identifier:    d.Identifier,
			Name:          d.DeviceProperties.Name,
			Model:         model,
			OSVersion:     d.DeviceProperties.OSVersionNumber,
			Platform:      d.HardwareProperties.Platform,
			TunnelState:   d.ConnectionProperties.TunnelState,
			TransportType: d.ConnectionProperties.TransportType,
		})
	}
	return out, nil
}

// ListApps returns installed apps on the device. By default devicectl
// reports developer apps and removable (user-deletable) apps — the set
// spyder previously got from installation_proxy's BrowseUserApps. App
// clips, default system apps, hidden and internal apps are excluded.
func (c *Client) ListApps(ctx context.Context, udid string) ([]App, error) {
	if udid == "" {
		return nil, fmt.Errorf("devicectl ListApps: udid is empty")
	}
	data, err := c.run(ctx, "device info apps",
		[]string{"device", "info", "apps", "--device", udid})
	if err != nil {
		return nil, err
	}
	return parseApps(data)
}

// ResolveBundleApp returns the single installed app matching bundleID,
// searching across all app categories (so it finds apps ListApps filters
// out). The bool is false with a nil error when the bundle isn't installed.
func (c *Client) ResolveBundleApp(ctx context.Context, udid, bundleID string) (App, bool, error) {
	if udid == "" || bundleID == "" {
		return App{}, false, fmt.Errorf("devicectl ResolveBundleApp: udid and bundleID are required")
	}
	data, err := c.run(ctx, "device info apps",
		[]string{"device", "info", "apps",
			"--device", udid,
			"--include-all-apps",
			"--bundle-id", bundleID})
	if err != nil {
		return App{}, false, err
	}
	apps, err := parseApps(data)
	if err != nil {
		return App{}, false, err
	}
	for _, a := range apps {
		if a.BundleID == bundleID {
			return a, true, nil
		}
	}
	return App{}, false, nil
}

func parseApps(data []byte) ([]App, error) {
	var doc struct {
		Result struct {
			Apps []struct {
				BundleIdentifier string `json:"bundleIdentifier"`
				Name             string `json:"name"`
				Version          string `json:"version"`
				URL              string `json:"url"`
				Removable        bool   `json:"removable"`
				DefaultApp       bool   `json:"defaultApp"`
			} `json:"apps"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("devicectl info apps JSON: %w", err)
	}
	out := make([]App, 0, len(doc.Result.Apps))
	for _, a := range doc.Result.Apps {
		path := fileURLToPath(a.URL)
		out = append(out, App{
			BundleID:   a.BundleIdentifier,
			Name:       a.Name,
			Version:    a.Version,
			AppFolder:  baseName(path),
			BundlePath: path,
			Removable:  a.Removable,
			DefaultApp: a.DefaultApp,
		})
	}
	return out, nil
}

// ListProcesses returns the device's running processes. Used to resolve a
// bundle id to a live pid (by matching the .app folder in each process path).
func (c *Client) ListProcesses(ctx context.Context, udid string) ([]Process, error) {
	if udid == "" {
		return nil, fmt.Errorf("devicectl ListProcesses: udid is empty")
	}
	data, err := c.run(ctx, "device info processes",
		[]string{"device", "info", "processes", "--device", udid})
	if err != nil {
		return nil, err
	}
	return parseProcesses(data)
}

func parseProcesses(data []byte) ([]Process, error) {
	var doc struct {
		Result struct {
			RunningProcesses []struct {
				ProcessIdentifier int    `json:"processIdentifier"`
				Executable        string `json:"executable"`
			} `json:"runningProcesses"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("devicectl info processes JSON: %w", err)
	}
	out := make([]Process, 0, len(doc.Result.RunningProcesses))
	for _, p := range doc.Result.RunningProcesses {
		out = append(out, Process{
			PID:  p.ProcessIdentifier,
			Path: fileURLToPath(p.Executable),
		})
	}
	return out, nil
}

// DeviceDetails returns identity and OS information for the device. It is
// the usbmuxd-free fallback for the State tool; battery/charging are not
// exposed by devicectl and remain the lockdown path's responsibility.
func (c *Client) DeviceDetails(ctx context.Context, udid string) (Details, error) {
	if udid == "" {
		return Details{}, fmt.Errorf("devicectl DeviceDetails: udid is empty")
	}
	data, err := c.run(ctx, "device info details",
		[]string{"device", "info", "details", "--device", udid})
	if err != nil {
		return Details{}, err
	}
	return parseDetails(data)
}

func parseDetails(data []byte) (Details, error) {
	var doc struct {
		Result struct {
			DeviceProperties struct {
				Name            string `json:"name"`
				OSVersionNumber string `json:"osVersionNumber"`
			} `json:"deviceProperties"`
			HardwareProperties struct {
				UDID          string `json:"udid"`
				MarketingName string `json:"marketingName"`
				ProductType   string `json:"productType"`
				Platform      string `json:"platform"`
			} `json:"hardwareProperties"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return Details{}, fmt.Errorf("devicectl info details JSON: %w", err)
	}
	model := doc.Result.HardwareProperties.MarketingName
	if model == "" {
		model = doc.Result.HardwareProperties.ProductType
	}
	return Details{
		UDID:      doc.Result.HardwareProperties.UDID,
		Name:      doc.Result.DeviceProperties.Name,
		Model:     model,
		OSVersion: doc.Result.DeviceProperties.OSVersionNumber,
		Platform:  doc.Result.HardwareProperties.Platform,
	}, nil
}

// baseName returns the last path element without importing path/filepath
// semantics that would mangle a device-side POSIX path on a non-POSIX host
// (devicectl paths are always POSIX). It is a plain split on '/'.
func baseName(p string) string {
	if p == "" {
		return ""
	}
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

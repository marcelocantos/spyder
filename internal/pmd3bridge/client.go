// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package pmd3bridge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

const defaultClientTimeout = 30 * time.Second

// Client calls the pmd3-bridge HTTP API over a Unix-domain socket. It is safe
// for concurrent use after construction.
type Client struct {
	http       *http.Client
	socketPath string
}

// NewClient constructs a Client that dials the bridge over the given Unix
// socket path. The socket need not exist at construction time; each request
// dials fresh.
func NewClient(socketPath string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}
	return &Client{
		socketPath: socketPath,
		http: &http.Client{
			Transport: transport,
			Timeout:   defaultClientTimeout,
		},
	}
}

// post encodes reqBody as JSON, POSTs to the given path, and decodes the
// response into respBody. On 4xx/5xx the response is decoded as a BridgeError.
// The "host" portion of the URL is irrelevant for Unix-socket transports — we
// use "localhost" as a conventional placeholder.
func (c *Client) post(ctx context.Context, path string, reqBody, respBody any) error {
	var body io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("pmd3bridge: marshal request: %w", err)
		}
		body = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://localhost"+path, body)
	if err != nil {
		return fmt.Errorf("pmd3bridge: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("pmd3bridge: %s: %w", path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("pmd3bridge: read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var errBody bridgeErrorBody
		if jerr := json.Unmarshal(raw, &errBody); jerr != nil {
			return &BridgeError{
				Code:    "unknown",
				Message: string(raw),
				Status:  resp.StatusCode,
			}
		}
		return &BridgeError{
			Code:    errBody.Error,
			Message: errBody.Message,
			Status:  resp.StatusCode,
		}
	}

	if respBody != nil {
		if jerr := json.Unmarshal(raw, respBody); jerr != nil {
			return fmt.Errorf("pmd3bridge: decode response from %s: %w", path, jerr)
		}
	}
	return nil
}

// ListDevices returns the connected iOS devices visible to the bridge.
func (c *Client) ListDevices(ctx context.Context) ([]DeviceInfo, error) {
	var resp listDevicesResponse
	if err := c.post(ctx, "/v1/list_devices", listDevicesRequest{}, &resp); err != nil {
		return nil, err
	}
	return resp.Devices, nil
}

// ListApps returns the apps installed on the device identified by udid.
func (c *Client) ListApps(ctx context.Context, udid string) ([]AppInfo, error) {
	var resp listAppsResponse
	if err := c.post(ctx, "/v1/list_apps", listAppsRequest{UDID: udid}, &resp); err != nil {
		return nil, err
	}
	return resp.Apps, nil
}

// LaunchApp launches the app with bundleID on the device identified by udid
// and returns its PID.
func (c *Client) LaunchApp(ctx context.Context, udid, bundleID string) (int, error) {
	var resp launchAppResponse
	if err := c.post(ctx, "/v1/launch_app",
		launchAppRequest{UDID: udid, BundleID: bundleID}, &resp); err != nil {
		return 0, err
	}
	return resp.PID, nil
}

// KillApp stops the app with bundleID on the device identified by udid.
func (c *Client) KillApp(ctx context.Context, udid, bundleID string) error {
	return c.post(ctx, "/v1/kill_app",
		killAppRequest{UDID: udid, BundleID: bundleID}, nil)
}

// PIDForBundle returns the PID of the running app with bundleID on the device
// identified by udid, or nil if the app is not running.
func (c *Client) PIDForBundle(ctx context.Context, udid, bundleID string) (*int, error) {
	var resp pidForBundleResponse
	if err := c.post(ctx, "/v1/pid_for_bundle",
		pidForBundleRequest{UDID: udid, BundleID: bundleID}, &resp); err != nil {
		return nil, err
	}
	return resp.PID, nil
}

// Battery returns the battery state for the device identified by udid.
func (c *Client) Battery(ctx context.Context, udid string) (Battery, error) {
	var resp Battery
	if err := c.post(ctx, "/v1/battery", batteryRequest{UDID: udid}, &resp); err != nil {
		return Battery{}, err
	}
	return resp, nil
}

// Screenshot captures a PNG screenshot from the device identified by udid and
// returns the raw PNG bytes (decoded from the bridge's base64 response).
func (c *Client) Screenshot(ctx context.Context, udid string) ([]byte, error) {
	var resp screenshotResponse
	if err := c.post(ctx, "/v1/screenshot", screenshotRequest{UDID: udid}, &resp); err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(resp.PNGBase64)
	if err != nil {
		return nil, fmt.Errorf("pmd3bridge: decode screenshot base64: %w", err)
	}
	return data, nil
}

// CrashReportsList lists crash reports on the device. since is optional
// (zero time = no lower bound). process is optional (empty = all processes).
func (c *Client) CrashReportsList(ctx context.Context, udid string, since time.Time, process string) ([]CrashReport, error) {
	req := crashReportsListRequest{UDID: udid}
	if !since.IsZero() {
		s := since.UTC().Format(time.RFC3339)
		req.SinceISO = &s
	}
	if process != "" {
		req.Process = &process
	}
	var resp crashReportsListResponse
	if err := c.post(ctx, "/v1/crash_reports_list", req, &resp); err != nil {
		return nil, err
	}
	return resp.Reports, nil
}

// CrashReportsPull returns the raw text content of the named crash report.
func (c *Client) CrashReportsPull(ctx context.Context, udid, name string) (string, error) {
	var resp crashReportsPullResponse
	if err := c.post(ctx, "/v1/crash_reports_pull",
		crashReportsPullRequest{UDID: udid, Name: name}, &resp); err != nil {
		return "", err
	}
	return resp.Content, nil
}

// AcquirePowerAssertion acquires a power assertion on the device and returns
// a handle ID for subsequent refresh and release calls.
// type_ is the pmd3 assertion type (e.g. "NoIdleSleep").
// details is optional (pass "" to omit).
func (c *Client) AcquirePowerAssertion(ctx context.Context, udid, type_, name string, timeoutSec int, details string) (string, error) {
	req := acquirePowerAssertionRequest{
		UDID:       udid,
		Type:       type_,
		Name:       name,
		TimeoutSec: timeoutSec,
	}
	if details != "" {
		req.Details = &details
	}
	var resp acquirePowerAssertionResponse
	if err := c.post(ctx, "/v1/acquire_power_assertion", req, &resp); err != nil {
		return "", err
	}
	return resp.HandleID, nil
}

// RefreshPowerAssertion extends the lifetime of an existing power assertion
// identified by handleID.
func (c *Client) RefreshPowerAssertion(ctx context.Context, handleID string, timeoutSec int) error {
	return c.post(ctx, "/v1/refresh_power_assertion",
		refreshPowerAssertionRequest{HandleID: handleID, TimeoutSec: timeoutSec}, nil)
}

// ReleasePowerAssertion releases a power assertion identified by handleID.
// Releasing an unknown handle is a no-op (idempotent by bridge design).
func (c *Client) ReleasePowerAssertion(ctx context.Context, handleID string) error {
	return c.post(ctx, "/v1/release_power_assertion",
		releasePowerAssertionRequest{HandleID: handleID}, nil)
}

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package pmd3bridge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Client calls the pmd3-bridge HTTP API over a loopback TCP connection with
// a bearer-token handshake (🎯T26.1). It is safe for concurrent use after
// construction.
//
// Error model (🎯T26.2): client methods return nil on success or a
// *BridgeError for structured responses from the bridge (device_not_paired,
// bundle_not_installed, tunneld_unavailable, pmd3_error, not_found). Any
// other failure mode — transport error, deadline exceeded, decode failure —
// is treated as a bug and panics via the client's fatal hook. The daemon
// process exits with the stack trace; the external process supervisor
// restarts it cleanly.
//
// context.Canceled is the one exception: it indicates the caller (typically
// daemon shutdown) has requested cancellation and is returned unchanged.
type Client struct {
	http *http.Client

	// Exactly one of (sup) or (baseURL, token) is populated. When sup is
	// non-nil, baseURL+token are read dynamically from it on every request
	// so a Client constructed before Start still works once Start completes.
	sup     *Supervisor
	baseURL string
	token   string

	// fatal is called when a bridge call encounters a bug condition.
	// Default: panic with the supplied error, crashing the daemon.
	// Tests may replace this to capture fatals without terminating the
	// test process.
	fatal func(error)
}

// NewClient constructs a Client that sends requests to baseURL (e.g.
// "http://127.0.0.1:12345") carrying `Authorization: Bearer <token>` on
// every request. Used by tests and by callers that already know the
// bridge's endpoint; production code prefers Supervisor.Client, which
// reads the endpoint live from the supervisor.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{},
		fatal:   func(err error) { panic(err) },
	}
}

// endpoint returns the live baseURL+token for this client. For
// supervisor-backed clients the values are read on every request; for
// static clients (tests) they are fixed at construction.
func (c *Client) endpoint() (string, string) {
	if c.sup != nil {
		return c.sup.BaseURL(), c.sup.Token()
	}
	return c.baseURL, c.token
}

// post encodes reqBody as JSON, POSTs to the given path with the supplied
// per-endpoint timeout applied as a context deadline, and decodes the response
// into respBody. Returns nil or a *BridgeError; any other failure panics via
// c.fatal.
//
// Logging (🎯T26.5): every call emits a DEBUG-level structured log on entry
// and on completion; calls taking longer than half the endpoint threshold
// also emit a WARN; fatal-path failures emit an ERROR before the panic so
// the breadcrumb is persisted even if the stack-trace stream is truncated.
func (c *Client) post(ctx context.Context, path string, timeout time.Duration,
	reqBody, respBody any,
) error {
	started := time.Now()
	slog.Debug("bridge call", "endpoint", path, "timeout_ms", timeout.Milliseconds())

	err := c.doPost(ctx, path, timeout, reqBody, respBody)

	elapsed := time.Since(started)
	c.logOutcome(path, elapsed, timeout, err)
	return err
}

// doPost implements the request/response pipeline proper. post() wraps it
// to own the timing/logging envelope.
func (c *Client) doPost(ctx context.Context, path string, timeout time.Duration,
	reqBody, respBody any,
) error {
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var body io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			// Static request shape; marshal failure = programmer bug.
			c.fire(fmt.Errorf("pmd3bridge: %s: marshal request: %w", path, err))
			return err
		}
		body = bytes.NewReader(b)
	}

	baseURL, token := c.endpoint()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost,
		baseURL+path, body)
	if err != nil {
		c.fire(fmt.Errorf("pmd3bridge: %s: build request: %w", path, err))
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// context.Canceled is caller-initiated shutdown, not a bug.
		// Anything else — dial refused, EOF, EPIPE, deadline exceeded — is.
		if errors.Is(err, context.Canceled) && ctx.Err() == context.Canceled {
			return err
		}
		c.fire(fmt.Errorf("pmd3bridge: %s: transport error after %s: %w",
			path, timeout, err))
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() == context.Canceled {
			return err
		}
		c.fire(fmt.Errorf("pmd3bridge: %s: read response: %w", path, err))
		return err
	}

	if resp.StatusCode >= 400 {
		var errBody bridgeErrorBody
		if jerr := json.Unmarshal(raw, &errBody); jerr != nil {
			// 4xx/5xx with non-JSON body = bridge protocol bug.
			c.fire(fmt.Errorf("pmd3bridge: %s: unstructured error response (%d): %s",
				path, resp.StatusCode, raw))
			return &BridgeError{Code: "unknown", Message: string(raw), Status: resp.StatusCode}
		}
		return &BridgeError{
			Code:    errBody.Error,
			Message: errBody.Message,
			Status:  resp.StatusCode,
		}
	}

	if respBody != nil {
		if jerr := json.Unmarshal(raw, respBody); jerr != nil {
			c.fire(fmt.Errorf("pmd3bridge: %s: decode response: %w", path, jerr))
			return jerr
		}
	}
	return nil
}

// logOutcome emits the per-call completion log, at the level appropriate to
// the outcome: DEBUG for ok, DEBUG for structured BridgeError, WARN if the
// call took more than half its threshold, and an additional WARN (at
// whatever level) if outcome is slow.
func (c *Client) logOutcome(path string, elapsed, timeout time.Duration, err error) {
	durMs := elapsed.Milliseconds()
	thresholdMs := timeout.Milliseconds()
	slow := elapsed*2 > timeout

	switch {
	case err == nil:
		slog.Debug("bridge call ok",
			"endpoint", path,
			"duration_ms", durMs,
			"threshold_ms", thresholdMs)
	default:
		var be *BridgeError
		if errors.As(err, &be) {
			slog.Debug("bridge call bridge_error",
				"endpoint", path,
				"duration_ms", durMs,
				"threshold_ms", thresholdMs,
				"code", be.Code,
				"status", be.Status)
		} else if errors.Is(err, context.Canceled) {
			slog.Debug("bridge call cancelled",
				"endpoint", path,
				"duration_ms", durMs)
		}
		// Fatal cases have already logged at ERROR inside fire().
	}

	if slow {
		slog.Warn("bridge call slow",
			"endpoint", path,
			"duration_ms", durMs,
			"threshold_ms", thresholdMs)
	}
}

// fire classifies a bridge-call failure and either swallows it (transport
// errors and shutdown-window errors) or panics (genuine protocol/programmer
// bugs). The split exists because the bridge supervisor manages a child
// subprocess that can hang or restart for environmental reasons; panicking
// the daemon on every transport blip would short-circuit the supervisor's
// own recovery path, and that's exactly what 🎯T41 found in production.
//
// Cases:
//
//  1. Supervisor in shutdown (Stopped() true) — in-flight transport errors
//     are expected (bridge got SIGTERM, listener torn down). WARN + swallow.
//     The daemon is exiting anyway; panicking would mask the SIGINT/SIGTERM
//     handler's clean-shutdown logging.
//
//  2. Recoverable transport error (deadline exceeded, dial refused, EOF/EPIPE
//     mid-call, network unreachable) on a steady-state call — the bridge
//     subprocess is wedged or briefly unavailable. WARN + return; the caller
//     gets the error and the supervisor's liveness probe will restart the
//     subprocess transparently. Panicking here would kick every connected
//     MCP client across the daemon (including a Claude Code session held
//     open via /mcp's GET stream), which 🎯T41 documents was happening every
//     ~30 minutes in production.
//
//  3. Genuine bug — marshal failure on a static request shape, decode
//     failure on the response, unstructured non-JSON 4xx/5xx, anything else
//     fire() is invoked with. ERROR + panic. These are programmer or
//     protocol bugs that should surface immediately, not be silently
//     swallowed.
func (c *Client) fire(err error) {
	if c.sup != nil && c.sup.Stopped() {
		slog.Warn("bridge call failed during shutdown — swallowed",
			"error", err.Error())
		return
	}
	if isRecoverableTransportError(err) {
		slog.Warn("bridge call transport error — daemon stays up, supervisor will recover",
			"error", err.Error())
		return
	}
	slog.Error("bridge call FATAL — about to panic", "error", err.Error())
	c.fatal(err)
}

// isRecoverableTransportError returns true when err represents a transport
// or transient subprocess fault rather than a protocol/programmer bug.
// These are recoverable in the sense that retrying the call (after the
// supervisor's liveness probe restarts the bridge subprocess if needed)
// will succeed.
func isRecoverableTransportError(err error) bool {
	if err == nil {
		return false
	}
	// Per-endpoint deadline expiry — bridge is wedged or genuinely slow.
	// (context.Canceled is not in this set: it's caller-initiated and
	// already short-circuited before fire() is called.)
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// Dial refused / connection reset / EPIPE / EOF mid-call / network
	// unreachable — anything net.OpError surfaces. These cover the case
	// where the bridge subprocess crashed or its listener went away
	// between calls.
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return true
	}
	// EOF mid-body during io.ReadAll(resp.Body) doesn't always wrap a
	// net.OpError on macOS/Linux — match by sentinel as a safety net.
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	return false
}

// ListDevices returns the connected iOS devices visible to the bridge.
func (c *Client) ListDevices(ctx context.Context) ([]DeviceInfo, error) {
	var resp listDevicesResponse
	if err := c.post(ctx, "/v1/list_devices", timeoutListDevices,
		listDevicesRequest{}, &resp); err != nil {
		return nil, err
	}
	return resp.Devices, nil
}

// ListApps returns the apps installed on the device identified by udid.
func (c *Client) ListApps(ctx context.Context, udid string) ([]AppInfo, error) {
	var resp listAppsResponse
	if err := c.post(ctx, "/v1/list_apps", timeoutListApps,
		listAppsRequest{UDID: udid}, &resp); err != nil {
		return nil, err
	}
	return resp.Apps, nil
}

// LaunchApp launches the app with bundleID on the device identified by udid
// and returns its PID.
func (c *Client) LaunchApp(ctx context.Context, udid, bundleID string) (int, error) {
	var resp launchAppResponse
	if err := c.post(ctx, "/v1/launch_app", timeoutLaunchKillApp,
		launchAppRequest{UDID: udid, BundleID: bundleID}, &resp); err != nil {
		return 0, err
	}
	return resp.PID, nil
}

// KillApp stops the app with bundleID on the device identified by udid.
func (c *Client) KillApp(ctx context.Context, udid, bundleID string) error {
	return c.post(ctx, "/v1/kill_app", timeoutLaunchKillApp,
		killAppRequest{UDID: udid, BundleID: bundleID}, nil)
}

// PIDForBundle returns the PID of the running app with bundleID on the device
// identified by udid, or nil if the app is not running.
func (c *Client) PIDForBundle(ctx context.Context, udid, bundleID string) (*int, error) {
	var resp pidForBundleResponse
	if err := c.post(ctx, "/v1/pid_for_bundle", timeoutPidForBundle,
		pidForBundleRequest{UDID: udid, BundleID: bundleID}, &resp); err != nil {
		return nil, err
	}
	return resp.PID, nil
}

// AppState constants returned by AppState. The bridge collapses iOS's
// fine-grained BackBoard taxonomy (Running, Suspended, Background-Running,
// Background-Task-Suspended, ...) onto three values that capture the only
// distinction autoawake cares about: is the app foregrounded, is it
// somewhere in the background, or has iOS reaped it entirely.
const (
	AppStateRunning      = "running"
	AppStateBackgrounded = "backgrounded"
	AppStateTerminated   = "terminated"
)

// AppState reports the lifecycle state of one app on the device — used
// by autoawake to detect a user-initiated swipe-away from KeepAwake
// (Running → backgrounded, observed across two ticks) and treat it as
// an opt-out signal. See bridge/src/pmd3_bridge/services.py::app_state
// for the BackBoard mapping and the detection caveats.
func (c *Client) AppState(ctx context.Context, udid, bundleID string) (string, string, error) {
	var resp appStateResponse
	if err := c.post(ctx, "/v1/app_state", timeoutAppState,
		appStateRequest{UDID: udid, BundleID: bundleID}, &resp); err != nil {
		return "", "", err
	}
	return resp.State, resp.Description, nil
}

// Battery returns the battery state for the device identified by udid.
func (c *Client) Battery(ctx context.Context, udid string) (Battery, error) {
	var resp Battery
	if err := c.post(ctx, "/v1/battery", timeoutBattery,
		batteryRequest{UDID: udid}, &resp); err != nil {
		return Battery{}, err
	}
	return resp, nil
}

// Screenshot captures a PNG screenshot from the device identified by udid and
// returns the raw PNG bytes (decoded from the bridge's base64 response).
func (c *Client) Screenshot(ctx context.Context, udid string) ([]byte, error) {
	var resp screenshotResponse
	if err := c.post(ctx, "/v1/screenshot", timeoutScreenshot,
		screenshotRequest{UDID: udid}, &resp); err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(resp.PNGBase64)
	if err != nil {
		c.fatal(fmt.Errorf("pmd3bridge: screenshot: decode base64: %w", err))
		return nil, err
	}
	return data, nil
}

// DevicePowerState queries the power/display state of the device without
// resetting its idle timer (🎯T29). The bridge uses the DVT Screenshot
// instrument to probe the framebuffer — a GPU/display read that does not
// write to IOPMrootDomain user-activity registers.
//
// State values: "awake", "display_off", "asleep", "unknown".
// See DevicePowerState doc and docs/papers/t29-device-state-detection.md.
func (c *Client) DevicePowerState(ctx context.Context, udid string) (DevicePowerState, error) {
	var resp DevicePowerState
	if err := c.post(ctx, "/v1/device_power_state", timeoutDeviceState,
		devicePowerStateRequest{UDID: udid}, &resp); err != nil {
		return DevicePowerState{}, err
	}
	return resp, nil
}

// CrashReportsList lists crash reports on the device. since is optional
// (zero time = no lower bound). process is optional (empty = all processes).
//
// Transport (🎯T26.3): the bridge streams the index as NDJSON over chunked
// HTTP. The client accumulates entries into a slice for backward-compatible
// return shape. The inter-packet deadline protects against device-side
// stalls; any stall during read panics via the fatal hook.
func (c *Client) CrashReportsList(ctx context.Context, udid string, since time.Time, process string) ([]CrashReport, error) {
	req := crashReportsListRequest{UDID: udid}
	if !since.IsZero() {
		s := since.UTC().Format(time.RFC3339)
		req.SinceISO = &s
	}
	if process != "" {
		req.Process = &process
	}

	const endpoint = "/v1/crash_reports_list"
	_, body, err := c.postStream(ctx, endpoint, timeoutStreamEndToEnd, req)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	var reports []CrashReport
	scanErr := scanNDJSON[CrashReport](body, func(r CrashReport) bool {
		reports = append(reports, r)
		return true
	})
	if err := c.drainErr(endpoint, scanErr); err != nil {
		return nil, err
	}
	return reports, nil
}

// CrashReportsPull returns the raw text content of the named crash report.
//
// Transport (🎯T26.3): the bridge streams the report as application/
// octet-stream over chunked HTTP. The client drains the body into a string
// for backward-compatible return shape. Inter-chunk stalls panic via the
// fatal hook.
func (c *Client) CrashReportsPull(ctx context.Context, udid, name string) (string, error) {
	const endpoint = "/v1/crash_reports_pull"
	_, body, err := c.postStream(ctx, endpoint, timeoutStreamEndToEnd,
		crashReportsPullRequest{UDID: udid, Name: name})
	if err != nil {
		return "", err
	}
	defer body.Close()

	buf, readErr := io.ReadAll(body)
	if err := c.drainErr(endpoint, readErr); err != nil {
		return "", err
	}
	return string(buf), nil
}

// Syslog streams syslog entries from the device, invoking onEntry for each
// parsed line. The stream runs until ctx is cancelled, the bridge closes
// the connection, or onEntry returns false. PID -1 means all processes;
// empty filter fields impose no filter (🎯T46).
//
// Transport: NDJSON over chunked HTTP, mirroring crash_reports_list. No
// outer end-to-end deadline is imposed — the caller's ctx is the only
// stop signal — but the inter-packet stall watchdog still applies.
func (c *Client) Syslog(ctx context.Context, udid string, filter SyslogFilter,
	onEntry func(SyslogEntry) bool,
) error {
	pid := filter.PID
	if pid == 0 {
		pid = -1
	}
	req := syslogRequest{
		UDID:        udid,
		PID:         pid,
		ProcessName: filter.ProcessName,
		Subsystem:   filter.Subsystem,
	}
	const endpoint = "/v1/syslog"
	_, body, err := c.postStream(ctx, endpoint, 0, req)
	if err != nil {
		return err
	}
	defer body.Close()

	scanErr := scanNDJSON(body, onEntry)
	return c.drainErr(endpoint, scanErr)
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
	if err := c.post(ctx, "/v1/acquire_power_assertion", timeoutPowerAssertion,
		req, &resp); err != nil {
		return "", err
	}
	return resp.HandleID, nil
}

// RefreshPowerAssertion extends the lifetime of an existing power assertion
// identified by handleID.
func (c *Client) RefreshPowerAssertion(ctx context.Context, handleID string, timeoutSec int) error {
	return c.post(ctx, "/v1/refresh_power_assertion", timeoutPowerAssertion,
		refreshPowerAssertionRequest{HandleID: handleID, TimeoutSec: timeoutSec}, nil)
}

// ReleasePowerAssertion releases a power assertion identified by handleID.
// Releasing an unknown handle is a no-op (idempotent by bridge design).
func (c *Client) ReleasePowerAssertion(ctx context.Context, handleID string) error {
	return c.post(ctx, "/v1/release_power_assertion", timeoutPowerAssertion,
		releasePowerAssertionRequest{HandleID: handleID}, nil)
}

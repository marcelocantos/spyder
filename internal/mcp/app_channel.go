// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/appchannel"
)

// lanHosts returns the LAN IPv4 candidates an app should dial to
// reach this spyder.
var lanHosts = func() ([]string, error) {
	return appchannelLANHosts()
}

// handleAppChannelStop closes a listener and tears down its sessions.
// Manual-GC entry point — the sweeper handles routine reaping after
// the 24h idle TTL.
func (h *Handler) handleAppChannelStop(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.appChannel == nil {
		return toolErr("app channel not configured")
	}
	id, err := requireString(args, "listener_id")
	if err != nil {
		return nil, err
	}
	l := h.findAppChannelListener(id)
	if l == nil {
		return toolErr("app_channel_stop: no such listener: %s", id)
	}
	l.Stop()
	return toolText(fmt.Sprintf("listener %s stopped", id))
}

// handleAppChannelList returns metadata for every keyed listener
// (and the sessions accepted on it).
func (h *Handler) handleAppChannelList(_ map[string]any) (*mcpgo.CallToolResult, error) {
	if h.appChannel == nil {
		return toolErr("app channel not configured")
	}
	listeners := h.appChannel.KeyedListeners()
	out := make([]listenerInfo, 0, len(listeners))
	for _, l := range listeners {
		out = append(out, listenerInfoFrom(l))
	}
	return toolJSON(map[string]any{"listeners": out})
}

type listenerInfo struct {
	ListenerID string        `json:"listener_id"`
	DeviceID   string        `json:"device_id"`
	BundleID   string        `json:"bundle_id"`
	Port       int           `json:"port"`
	Owner      string        `json:"owner,omitempty"`
	IdleSince  string        `json:"idle_since,omitempty"`
	Sessions   []sessionInfo `json:"sessions"`
}

type sessionInfo struct {
	SessionID  string   `json:"session_id"`
	Port       int      `json:"port"`
	Owner      string   `json:"owner,omitempty"`
	StartedAt  string   `json:"started_at"`
	AppName    string   `json:"app_name,omitempty"`
	AppVersion string   `json:"app_version,omitempty"`
	Methods    []string `json:"methods,omitempty"`
}

func listenerInfoFrom(l *appchannel.Listener) listenerInfo {
	sessions := l.Sessions()
	infos := make([]sessionInfo, 0, len(sessions))
	for _, s := range sessions {
		infos = append(infos, sessionInfoFrom(s))
	}
	info := listenerInfo{
		ListenerID: l.ID,
		DeviceID:   l.Key.DeviceID,
		BundleID:   l.Key.BundleID,
		Port:       l.Port,
		Owner:      l.Owner,
		Sessions:   infos,
	}
	if len(sessions) == 0 {
		info.IdleSince = l.LastTouched().Format(time.RFC3339)
	}
	return info
}

func sessionInfoFrom(s *appchannel.Session) sessionInfo {
	info := sessionInfo{
		SessionID: s.ID,
		Port:      s.Port,
		Owner:     s.Owner,
		StartedAt: s.StartedAt.Format(time.RFC3339),
	}
	if h := s.HelloInfo(); h != nil {
		info.AppName = h.AppName
		info.AppVersion = h.AppVersion
		info.Methods = h.Methods
	}
	return info
}

// requireSession resolves the target session for an app_* tool call.
//
// Resolution precedence:
//  1. session_id (explicit; backwards-compatible)
//  2. (device, bundle_id) — looks up the keyed listener and picks
//     its unique live session
//  3. unique-live-session fallback (when only one is connected)
//
// The keyed listener is Touch()ed whenever it's involved in
// resolution, so an active agent loop keeps the 24h idle timer
// from firing.
func (h *Handler) requireSession(args map[string]any) (*appchannel.Session, *mcpgo.CallToolResult) {
	if h.appChannel == nil {
		res, _ := toolErr("app channel not configured")
		return nil, res
	}
	if id := optString(args, "session_id"); id != "" {
		s, ok := h.appChannel.GetSession(id)
		if !ok {
			res, _ := toolErr("no such session: %s", id)
			return nil, res
		}
		if s.Listener() != nil {
			s.Listener().Touch()
		}
		return s, nil
	}

	dev := optString(args, "device")
	bundleID := optString(args, "bundle_id")
	if dev != "" && bundleID != "" {
		_, _, deviceID, err := h.resolveAdapter(dev)
		if err != nil {
			res, _ := toolErr("resolve %q: %v", dev, err)
			return nil, res
		}
		key := appchannel.AppKey{DeviceID: deviceID, BundleID: bundleID}
		l, ok := h.appChannel.LookupKeyed(key)
		if !ok {
			res, _ := toolErr("no app channel listener for device=%s bundle_id=%s (launch the app first?)", dev, bundleID)
			return nil, res
		}
		l.Touch()
		sessions := l.Sessions()
		switch len(sessions) {
		case 0:
			res, _ := toolErr("no live app channel session for device=%s bundle_id=%s (waiting for app to connect?)", dev, bundleID)
			return nil, res
		case 1:
			return sessions[0], nil
		default:
			res, _ := toolErr("multiple live sessions for device=%s bundle_id=%s — pass session_id", dev, bundleID)
			return nil, res
		}
	}

	sessions := h.appChannel.Sessions()
	if len(sessions) == 1 {
		if sessions[0].Listener() != nil {
			sessions[0].Listener().Touch()
		}
		return sessions[0], nil
	}
	res, _ := toolErr("session_id (or device+bundle_id) is required (have %d active sessions)", len(sessions))
	return nil, res
}

// findAppChannelListener finds a keyed listener by listener_id.
// Returns nil if no keyed listener has that id.
func (h *Handler) findAppChannelListener(id string) *appchannel.Listener {
	if h.appChannel == nil {
		return nil
	}
	for _, l := range h.appChannel.KeyedListeners() {
		if l.ID == id {
			return l
		}
	}
	return nil
}

// --- single-method handlers ----------------------------------------------

// callSimple is a helper for methods with no params and no significant
// result — just sends the call, waits for ack.
func callSimple(s *appchannel.Session, method string, timeout time.Duration) (*mcpgo.CallToolResult, error) {
	_, err := s.Call(context.Background(), method, nil, timeout)
	if err != nil {
		return toolErr("%s: %v", method, err)
	}
	return toolText(fmt.Sprintf("%s acknowledged", method))
}

func (h *Handler) handleAppPing(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	res, err := s.Call(context.Background(), appchannel.MethodPing, nil, 5*time.Second)
	if err != nil {
		return toolErr("ping: %v", err)
	}
	var pong map[string]any
	if err := appchannel.UnpackParams(res, &pong); err != nil {
		return toolErr("ping: decode pong: %v", err)
	}
	return toolJSON(pong)
}

func (h *Handler) handleAppQuit(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	timeoutMs, _ := args["timeout_ms"].(float64)
	if timeoutMs <= 0 {
		timeoutMs = 5000
	}
	return callSimple(s, appchannel.MethodQuit, time.Duration(timeoutMs)*time.Millisecond)
}

func (h *Handler) handleAppFlush(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	return callSimple(s, appchannel.MethodFlush, 5*time.Second)
}

func (h *Handler) handleAppBackground(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	return callSimple(s, appchannel.MethodBackgrounded, 5*time.Second)
}

func (h *Handler) handleAppForeground(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	return callSimple(s, appchannel.MethodForegrounded, 5*time.Second)
}

func (h *Handler) handleAppLowMemory(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	return callSimple(s, appchannel.MethodLowMemoryWarning, 5*time.Second)
}

func (h *Handler) handleAppPause(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	return callSimple(s, appchannel.MethodPause, 5*time.Second)
}

func (h *Handler) handleAppResume(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	return callSimple(s, appchannel.MethodResume, 5*time.Second)
}

func (h *Handler) handleAppStep(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	frames, _ := args["frames"].(float64)
	if frames <= 0 {
		frames = 1
	}
	_, err := s.Call(context.Background(), appchannel.MethodStep, map[string]int{"frames": int(frames)}, 5*time.Second)
	if err != nil {
		return toolErr("step: %v", err)
	}
	return toolText(fmt.Sprintf("stepped %d frames", int(frames)))
}

func (h *Handler) handleAppSpeed(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	mult, ok := args["multiplier"].(float64)
	if !ok || mult <= 0 {
		return toolErr("speed: multiplier (positive number) is required")
	}
	_, err := s.Call(context.Background(), appchannel.MethodSpeed, map[string]float64{"multiplier": mult}, 5*time.Second)
	if err != nil {
		return toolErr("speed: %v", err)
	}
	return toolText(fmt.Sprintf("speed set to %vx", mult))
}

func (h *Handler) handleAppInput(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	inputType, err := requireString(args, "type")
	if err != nil {
		return nil, err
	}
	// Pass through every arg except session_id and type as params.
	params := map[string]any{"type": inputType}
	for k, v := range args {
		if k == "session_id" || k == "type" {
			continue
		}
		params[k] = v
	}
	_, err = s.Call(context.Background(), appchannel.MethodInputInject, params, 5*time.Second)
	if err != nil {
		return toolErr("input: %v", err)
	}
	return toolText(fmt.Sprintf("injected %s event", inputType))
}

// handleAppStateSlices returns the slice catalogue the app advertised
// in its `hello` (the `slices` field). Lets an agent discover what a
// connected game exposes without prior knowledge. Pre-T80 apps that
// omit the `slices` field return an empty list — same shape, agent
// can fall back to known-slice probing.
func (h *Handler) handleAppStateSlices(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	hello := s.HelloInfo()
	slices := []appchannel.SliceDescriptor{}
	if hello != nil && hello.Slices != nil {
		slices = hello.Slices
	}
	return toolJSON(map[string]any{
		"session_id": s.ID,
		"slices":     slices,
	})
}

func (h *Handler) handleAppStateCaptureStart(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	slice, err := requireString(args, "slice")
	if err != nil {
		return nil, err
	}
	var interval time.Duration
	if v, ok := args["interval_ms"].(float64); ok && v > 0 {
		interval = time.Duration(v) * time.Millisecond
	}
	selectExpr := optString(args, "select")
	c, err := s.StartStateCaptureWithSelect(slice, interval, selectExpr)
	if err != nil {
		if jqErr, ok := err.(*appchannel.JQError); ok {
			return toolJSON(map[string]any{"select_error": jqErr})
		}
		return toolErr("state_capture_start: %v", err)
	}
	return toolJSON(map[string]any{
		"session_id":  s.ID,
		"capture_id":  c.ID,
		"slice":       c.Slice,
		"interval_ms": int(c.Interval / time.Millisecond),
		"started_at":  c.Started,
		"select":      c.SelectExpr,
	})
}

func (h *Handler) handleAppStateCaptureGet(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	captureID, err := requireString(args, "capture_id")
	if err != nil {
		return nil, err
	}
	r, err := s.GetStateCapture(captureID)
	if err != nil {
		return toolErr("state_capture_get: %v", err)
	}
	selectExpr := optString(args, "select")
	if selectExpr == "" {
		return toolJSON(r)
	}
	filtered, jqErr := filterCaptureSamples(r.Samples, selectExpr)
	if jqErr != nil {
		return toolJSON(map[string]any{"select_error": jqErr})
	}
	return toolJSON(map[string]any{
		"capture_id":      r.CaptureID,
		"slice":           r.Slice,
		"samples":         filtered,
		"dropped_samples": r.Dropped,
		"errors":          r.Errors,
		"last_error":      r.LastError,
	})
}

// filterCaptureSamples runs each sample's payload through the jq expr
// and returns the matched results paired with their timestamps. A
// parse/compile error is returned upfront (the expression is invalid
// regardless of which sample it'd be applied to); per-sample eval
// errors collapse the sample's result to nil.
func filterCaptureSamples(samples []appchannel.StateCaptureSample, expr string) ([]map[string]any, *appchannel.JQError) {
	// Pre-parse once via ApplyJQ on a dummy empty input — gojq.Parse
	// is cheap but we want the JQError shape consistent with the
	// other call sites.
	out := make([]map[string]any, 0, len(samples))
	for _, sample := range samples {
		v, err := appchannel.ApplyJQ(expr, sample.Data)
		if err != nil {
			if jqErr, ok := err.(*appchannel.JQError); ok {
				return nil, jqErr
			}
			v = nil
		}
		out = append(out, map[string]any{
			"timestamp": sample.Timestamp,
			"data":      v,
		})
	}
	return out, nil
}

func (h *Handler) handleAppStateCaptureStop(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	captureID, err := requireString(args, "capture_id")
	if err != nil {
		return nil, err
	}
	r, err := s.StopStateCapture(captureID)
	if err != nil {
		return toolErr("state_capture_stop: %v", err)
	}
	return toolJSON(r)
}

func (h *Handler) handleAppStateCaptureList(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	return toolJSON(map[string]any{
		"session_id": s.ID,
		"captures":   s.ListStateCaptures(),
	})
}

func (h *Handler) handleAppState(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	slice, err := requireString(args, "slice")
	if err != nil {
		return nil, err
	}
	selectExpr := optString(args, "select")
	res, err := s.Call(context.Background(), appchannel.MethodStateQuery, map[string]string{"slice": slice}, 10*time.Second)
	if err != nil {
		return toolErr("state_query: %v", err)
	}
	out, err := appchannel.ApplyJQ(selectExpr, res)
	if err != nil {
		if jqErr, ok := err.(*appchannel.JQError); ok {
			return toolJSON(map[string]any{"select_error": jqErr})
		}
		return toolErr("state_query: %v", err)
	}
	return toolJSON(out)
}

// handleAppTweakList / Get / Set / Reset expose the app's tweak plane over
// the app-channel (🎯T91.2) — ged's tweak_* control, ported so a direct-mode
// app is tunable from spyder without ged. The app answers with the shared
// tweak:: library's serialisation, so the shapes match ged's.
func (h *Handler) handleAppTweakList(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	res, err := s.Call(context.Background(), appchannel.MethodTweakList, nil, 10*time.Second)
	if err != nil {
		return toolErr("tweak_list: %v", err)
	}
	out, err := appchannel.ApplyJQ("", res)
	if err != nil {
		return toolErr("tweak_list: %v", err)
	}
	return toolJSON(out)
}

func (h *Handler) handleAppTweakGet(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	name, err := requireString(args, "name")
	if err != nil {
		return nil, err
	}
	res, err := s.Call(context.Background(), appchannel.MethodTweakGet, map[string]string{"name": name}, 10*time.Second)
	if err != nil {
		return toolErr("tweak_get: %v", err)
	}
	out, err := appchannel.ApplyJQ("", res)
	if err != nil {
		return toolErr("tweak_get: %v", err)
	}
	return toolJSON(out)
}

func (h *Handler) handleAppTweakSet(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	name, err := requireString(args, "name")
	if err != nil {
		return nil, err
	}
	value, ok := args["value"]
	if !ok {
		return toolErr("tweak_set: 'value' is required")
	}
	res, err := s.Call(context.Background(), appchannel.MethodTweakSet, map[string]any{"name": name, "value": value}, 10*time.Second)
	if err != nil {
		return toolErr("tweak_set: %v", err)
	}
	out, err := appchannel.ApplyJQ("", res)
	if err != nil {
		return toolErr("tweak_set: %v", err)
	}
	return toolJSON(out)
}

func (h *Handler) handleAppTweakReset(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	// name resets one tweak; its absence resets all — mirrors ged's payload.
	params := map[string]any{}
	if name := optString(args, "name"); name != "" {
		params["name"] = name
	} else {
		params["all"] = true
	}
	res, err := s.Call(context.Background(), appchannel.MethodTweakReset, params, 10*time.Second)
	if err != nil {
		return toolErr("tweak_reset: %v", err)
	}
	out, err := appchannel.ApplyJQ("", res)
	if err != nil {
		return toolErr("tweak_reset: %v", err)
	}
	return toolJSON(out)
}

// handleAppStateDescribe runs `state_query{slice}` once and walks the
// response into a types-only sketch — enough for the agent to write
// jq filters without first paying the full-payload cost.
func (h *Handler) handleAppStateDescribe(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	slice, err := requireString(args, "slice")
	if err != nil {
		return nil, err
	}
	res, err := s.Call(context.Background(), appchannel.MethodStateQuery, map[string]string{"slice": slice}, 10*time.Second)
	if err != nil {
		return toolErr("state_describe: %v", err)
	}
	shape, err := appchannel.DescribeShape(res)
	if err != nil {
		return toolErr("state_describe: %v", err)
	}
	return toolJSON(map[string]any{
		"slice": slice,
		"shape": shape,
	})
}

func (h *Handler) handleAppSaveState(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	res, err := s.Call(context.Background(), appchannel.MethodSaveState, nil, 30*time.Second)
	if err != nil {
		return toolErr("save_state: %v", err)
	}
	// Result is {state: <bin>}; return it base64-encoded so MCP/JSON
	// carries it cleanly.
	var resp struct {
		State []byte `msgpack:"state"`
	}
	if err := appchannel.UnpackParams(res, &resp); err != nil {
		return toolErr("save_state: decode result: %v", err)
	}
	return toolJSON(map[string]any{
		"state_b64": base64.StdEncoding.EncodeToString(resp.State),
		"size":      len(resp.State),
	})
}

func (h *Handler) handleAppRestoreState(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	b64, err := requireString(args, "state_b64")
	if err != nil {
		return nil, err
	}
	state, decErr := base64.StdEncoding.DecodeString(b64)
	if decErr != nil {
		return toolErr("restore_state: invalid base64: %v", decErr)
	}
	_, err = s.Call(context.Background(), appchannel.MethodRestoreState, map[string][]byte{"state": state}, 30*time.Second)
	if err != nil {
		return toolErr("restore_state: %v", err)
	}
	return toolText(fmt.Sprintf("restored %d bytes of state", len(state)))
}

func (h *Handler) handleAppScreenshot(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	res, err := s.Call(context.Background(), appchannel.MethodScreenshotApp, nil, 10*time.Second)
	if err != nil {
		return toolErr("screenshot_app: %v", err)
	}
	var resp struct {
		Format string `msgpack:"format"`
		Width  int    `msgpack:"width"`
		Height int    `msgpack:"height"`
		Data   []byte `msgpack:"data"`
	}
	if err := appchannel.UnpackParams(res, &resp); err != nil {
		return toolErr("screenshot_app: decode: %v", err)
	}
	if outPath := optString(args, "path"); outPath != "" {
		abs, err := resolveOutputPath(outPath)
		if err != nil {
			return toolErr("%v", err)
		}
		if err := writeOutputFile(abs, resp.Data); err != nil {
			return toolErr("saving app screenshot: %v", err)
		}
		return toolText(fmt.Sprintf(
			"app screenshot %dx%d saved to %s (%d bytes)",
			resp.Width, resp.Height, abs, len(resp.Data)))
	}
	return mcpgo.NewToolResultImage(
		fmt.Sprintf("app screenshot %dx%d (%d bytes)", resp.Width, resp.Height, len(resp.Data)),
		base64.StdEncoding.EncodeToString(resp.Data),
		"image/"+resp.Format,
	), nil
}

func (h *Handler) handleAppLogGet(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	lines, dropped := s.DrainLogs()
	selectExpr := optString(args, "select")
	out, jqErr := applyJQToValue(lines, selectExpr)
	if jqErr != nil {
		return toolJSON(map[string]any{"select_error": jqErr})
	}
	return toolJSON(map[string]any{
		"session_id":    s.ID,
		"lines":         out,
		"dropped_lines": dropped,
	})
}

func (h *Handler) handleAppPerfGet(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	samples, dropped := s.DrainPerf()
	selectExpr := optString(args, "select")
	out, jqErr := applyJQToValue(samples, selectExpr)
	if jqErr != nil {
		return toolJSON(map[string]any{"select_error": jqErr})
	}
	return toolJSON(map[string]any{
		"session_id":      s.ID,
		"samples":         out,
		"dropped_samples": dropped,
	})
}

// applyJQToValue runs a jq expression over an arbitrary Go value by
// round-tripping through MessagePack. Lets the log/perf handlers
// (which start from in-memory structs) share the same filter
// machinery the state handlers use on raw msgpack bytes.
func applyJQToValue(v any, expr string) (any, *appchannel.JQError) {
	if expr == "" {
		return v, nil
	}
	raw, err := appchannel.PackParams(v)
	if err != nil {
		return nil, &appchannel.JQError{Expression: expr, Stage: "marshal", Detail: err.Error()}
	}
	out, err := appchannel.ApplyJQ(expr, raw)
	if err != nil {
		if jqErr, ok := err.(*appchannel.JQError); ok {
			return nil, jqErr
		}
		return nil, &appchannel.JQError{Expression: expr, Stage: "eval", Detail: err.Error()}
	}
	return out, nil
}

// appChannelDefinitions returns the MCP tool surface.
func appChannelDefinitions() []mcpgo.Tool {
	return []mcpgo.Tool{
		mcpgo.NewTool("app_channel_stop",
			mcpgo.WithDescription("Manually stop a per-(device, bundle_id) appchannel listener and tear down its sessions. Routine cleanup happens automatically — listeners with no live session and no recent activity are reaped after 24h. Use this when you want to force a teardown sooner (e.g. before swapping the app's binary)."),
			mcpgo.WithString("listener_id", mcpgo.Required(), mcpgo.Description("listener_id as reported by app_channel_list")),
		),
		mcpgo.NewTool("app_channel_list",
			mcpgo.WithDescription("List active per-(device, bundle_id) appchannel listeners and the sessions accepted on each. Each entry has listener_id, device_id, bundle_id, port, owner, idle_since (only when no session is currently connected), and a sessions array (session_id, started_at, app_name, app_version, methods advertised in hello). Listeners are created automatically by `launch_app` and `deploy_app`."),
		),

		mcpgo.NewTool("app_ping", mcpgo.WithDescription("Ping the app (round-trip liveness check). Returns the timestamp the app saw."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
		),
		mcpgo.NewTool("app_quit", mcpgo.WithDescription("Ask the app to shut itself down cleanly (SDL_QUIT path → exit 0; no macOS crash notification). Falls back to terminate_app on timeout."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithNumber("timeout_ms", mcpgo.Description("How long to wait for the app to acknowledge the shutdown. Default 5000.")),
		),
		mcpgo.NewTool("app_flush", mcpgo.WithDescription("Ask the app to drain pending output (log queue, persistence) and acknowledge when done. Useful as a precondition for app_quit."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
		),
		mcpgo.NewTool("app_background", mcpgo.WithDescription("Fire the platform background-transition notification in the app without touching device focus."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
		),
		mcpgo.NewTool("app_foreground", mcpgo.WithDescription("Fire the platform foreground-transition notification in the app without touching device focus."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
		),
		mcpgo.NewTool("app_low_memory", mcpgo.WithDescription("Fire the synthetic memory-pressure notification in the app (iOS: UIApplicationDidReceiveMemoryWarningNotification analog; Android: onTrimMemory)."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
		),

		mcpgo.NewTool("app_pause", mcpgo.WithDescription("Pause the app's main loop (dt becomes 0; input/render continue so the app stays responsive)."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
		),
		mcpgo.NewTool("app_resume", mcpgo.WithDescription("Resume normal pacing after app_pause/app_speed."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
		),
		mcpgo.NewTool("app_step", mcpgo.WithDescription("Advance N frames while paused, then re-pause."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithNumber("frames", mcpgo.Description("Number of frames to advance (default 1)")),
		),
		mcpgo.NewTool("app_speed", mcpgo.WithDescription("Set a dt multiplier. 0.1 for slow-mo, 10.0 for soak. Persists until next app_speed or app_resume."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithNumber("multiplier", mcpgo.Required(), mcpgo.Description("Positive dt multiplier")),
		),

		mcpgo.NewTool("app_input", mcpgo.WithDescription("Inject a synthetic input event into the app's event loop. The `type` field determines the event shape; remaining args are passed through as params (e.g. {type: \"finger_down\", x: 0.5, y: 0.5}, {type: \"key_down\", key: \"a\"}, {type: \"accel\", x: 0.0, y: 1.0, z: 0.0})."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("type", mcpgo.Required(), mcpgo.Description("Event type: finger_down, finger_up, finger_motion, key_down, key_up, accel")),
		),

		mcpgo.NewTool("app_state", mcpgo.WithDescription("Query a named slice of the app's state. The app's hello advertises which slices it supports."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("slice", mcpgo.Required(), mcpgo.Description("State slice name (e.g. \"scene\", \"physics\", \"hud\")")),
		),
		mcpgo.NewTool("app_tweak_list", mcpgo.WithDescription("List the app's tweaks — name, current value, default, and metadata — over the app-channel (🎯T91.2: ged's tweak_list, ported so a direct-mode app is tunable without ged)."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
		),
		mcpgo.NewTool("app_tweak_get", mcpgo.WithDescription("Get one tweak's current value/default/metadata by name over the app-channel (🎯T91.2)."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("name", mcpgo.Required(), mcpgo.Description("Tweak name (e.g. \"camera.fov_deg\").")),
		),
		mcpgo.NewTool("app_tweak_set", mcpgo.WithDescription("Set a tweak's value over the app-channel; the app applies it and persists via its tweak DB (🎯T91.2)."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("name", mcpgo.Required(), mcpgo.Description("Tweak name.")),
			mcpgo.WithAny("value", mcpgo.Required(), mcpgo.Description("New value — any JSON type the tweak accepts (number, array, bool, …).")),
		),
		mcpgo.NewTool("app_tweak_reset", mcpgo.WithDescription("Reset one tweak (by name) or all tweaks (name omitted) to their defaults over the app-channel (🎯T91.2)."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("name", mcpgo.Description("Tweak name to reset; omit to reset all tweaks.")),
		),
		mcpgo.NewTool("app_save_state", mcpgo.WithDescription("Ask the app to serialize its state. Returns {state_b64, size}; pass the b64 blob back via app_restore_state."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
		),
		mcpgo.NewTool("app_restore_state", mcpgo.WithDescription("Load a previously-captured state blob (from app_save_state) back into the app."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("state_b64", mcpgo.Required(), mcpgo.Description("base64-encoded state blob")),
		),
		mcpgo.NewTool("app_screenshot", mcpgo.WithDescription("Request a screenshot from the app's own framebuffer (sibling to spyder's DTX-based `screenshot`; useful when DTX is wedged or you need state-correlated capture). By default returns the image inline; pass path to instead save it to that file and return a text confirmation."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("path", mcpgo.Description("Optional output file path. When set, the image is written here (a leading ~ is expanded; parent directories are created) and the tool returns a text confirmation instead of the inline image.")),
		),

		mcpgo.NewTool("app_state_slices", mcpgo.WithDescription("Return the slice catalogue the app advertised in its hello. Each entry has a `name` and an optional `example` payload — agents that get an example can write jq filters immediately; agents that don't can call app_state_describe to learn the shape without paying the full-payload cost."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
		),
		mcpgo.NewTool("app_state_describe", mcpgo.WithDescription("Call state_query{slice} once and return a recursive types-only sketch (`{\"marble\": {\"position\": {\"x\": \"float\", ...}}, ...}`). Lets the agent infer jq expressions without first ingesting the full payload. Works for any app supporting state_query — no protocol changes needed app-side."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("slice", mcpgo.Required(), mcpgo.Description("Slice name to describe.")),
		),
		mcpgo.NewTool("app_state_capture_start", mcpgo.WithDescription("Start a background poller that samples `state_query{slice}` at a fixed interval, accumulating timestamped samples until app_state_capture_stop is called. Mirrors the log_collect / app_perf_get pattern — lets an agent run an `app_input` sequence and observe state evolve frame-by-frame without a hand-rolled poll loop. Drain accumulated samples with app_state_capture_get.\n\nOptional `select` jq expression is applied at insert time — samples whose filter result is empty don't enter the ring buffer. Saves agent context budget and capture-buffer memory when only a small subset of a large slice is interesting."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("slice", mcpgo.Required(), mcpgo.Description("Slice name (e.g. \"scene\", \"physics\"). Must be one the app advertised in hello.")),
			mcpgo.WithNumber("interval_ms", mcpgo.Description("Sample interval in milliseconds. Default 100 (~10 Hz). Minimum 10.")),
			mcpgo.WithString("select", mcpgo.Description("Optional jq expression applied to each sample before insertion. Samples whose filter result is empty are skipped. Bad expressions are caught at start time and returned as `select_error`.")),
		),
		mcpgo.NewTool("app_state_capture_get", mcpgo.WithDescription("Drain the buffered samples for a state capture without stopping it. Optional `select` filters the drained samples (applies in addition to any insert-time filter set at start)."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("capture_id", mcpgo.Required(), mcpgo.Description("capture_id returned by app_state_capture_start")),
			mcpgo.WithString("select", mcpgo.Description("Optional jq expression applied to each drained sample's data. Bad expressions return `select_error`.")),
		),
		mcpgo.NewTool("app_state_capture_stop", mcpgo.WithDescription("Stop a state capture poller and return the remaining samples. The capture is gone after this call."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("capture_id", mcpgo.Required(), mcpgo.Description("capture_id returned by app_state_capture_start")),
		),
		mcpgo.NewTool("app_state_capture_list", mcpgo.WithDescription("List the active state captures on a session. Returns capture_id, slice, interval_ms, started_at, sample/dropped/error counts."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
		),

		mcpgo.NewTool("app_log_get", mcpgo.WithDescription("Drain structured log lines the app has pushed since the last call. Capture continues. Optional `select` filters the drained lines server-side."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("select", mcpgo.Description("Optional jq expression applied to the lines array. Example: `map(select(.level == \"error\"))`.")),
		),
		mcpgo.NewTool("app_perf_get", mcpgo.WithDescription("Drain perf-counter samples the app has pushed since the last call. Capture continues. Optional `select` filters the drained samples server-side."),
			mcpgo.WithString("session_id", mcpgo.Description("Target session id. Alternatively pass device+bundle_id; omit all three when only one session is connected.")),
			mcpgo.WithString("device", mcpgo.Description("Device alias or UUID — used with bundle_id to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("bundle_id", mcpgo.Description("App bundle id — used with device to resolve the keyed listener when session_id is omitted.")),
			mcpgo.WithString("select", mcpgo.Description("Optional jq expression applied to the samples array. Example: `[.[].samples.frame_ms] | max`.")),
		),
	}
}

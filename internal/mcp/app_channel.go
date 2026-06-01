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

// handleAppChannelStart opens a fresh appchannel TCP listener and
// returns the host:port plus the listener_id callers use to address
// it. Mirrors log_collect_start; apps connect, send a hello, and
// service subsequent RPC calls.
func (h *Handler) handleAppChannelStart(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.appChannel == nil {
		return toolErr("app channel not configured")
	}
	l, err := h.appChannel.Start(appchannel.StartParams{Owner: optString(args, "owner")})
	if err != nil {
		return toolErr("app_channel_start: %v", err)
	}
	h.appChannelListenerMu.Lock()
	h.appChannelListeners[l.ID] = l
	h.appChannelListenerMu.Unlock()

	hosts, _ := lanHosts()
	return toolJSON(struct {
		ListenerID string   `json:"listener_id"`
		Port       int      `json:"port"`
		Hosts      []string `json:"hosts"`
		Owner      string   `json:"owner,omitempty"`
	}{
		ListenerID: l.ID,
		Port:       l.Port,
		Hosts:      hosts,
		Owner:      l.Owner,
	})
}

// lanHosts wraps logcollect.LANHosts so this file doesn't import the
// logcollect package directly (keeps the responsibilities clean —
// app_channel is independent of log_collect).
var lanHosts = func() ([]string, error) {
	// Lazy import via type alias to avoid pulling logcollect in;
	// LANHosts is duplicated trivially here from logcollect to keep
	// the packages decoupled at the import graph.
	return appchannelLANHosts()
}

// handleAppChannelStop closes a listener and tears down its sessions.
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
	h.appChannelListenerMu.Lock()
	delete(h.appChannelListeners, id)
	h.appChannelListenerMu.Unlock()
	return toolText(fmt.Sprintf("listener %s stopped", id))
}

// handleAppChannelList returns metadata for every live session.
func (h *Handler) handleAppChannelList(_ map[string]any) (*mcpgo.CallToolResult, error) {
	if h.appChannel == nil {
		return toolErr("app channel not configured")
	}
	out := []sessionInfo{}
	for _, s := range h.appChannel.Sessions() {
		info := sessionInfoFrom(s)
		out = append(out, info)
	}
	return toolJSON(out)
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

// requireSession resolves session_id (or, if a single session exists,
// uses that as a default).
func (h *Handler) requireSession(args map[string]any) (*appchannel.Session, *mcpgo.CallToolResult) {
	if h.appChannel == nil {
		res, _ := toolErr("app channel not configured")
		return nil, res
	}
	id := optString(args, "session_id")
	if id == "" {
		sessions := h.appChannel.Sessions()
		if len(sessions) == 1 {
			return sessions[0], nil
		}
		res, _ := toolErr("session_id is required (have %d active sessions)", len(sessions))
		return nil, res
	}
	s, ok := h.appChannel.GetSession(id)
	if !ok {
		res, _ := toolErr("no such session: %s", id)
		return nil, res
	}
	return s, nil
}

func (h *Handler) findAppChannelListener(id string) *appchannel.Listener {
	if h.appChannel == nil {
		return nil
	}
	h.appChannelListenerMu.Lock()
	defer h.appChannelListenerMu.Unlock()
	return h.appChannelListeners[id]
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
	_ = appchannel.UnpackParams(res, &pong)
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

func (h *Handler) handleAppState(args map[string]any) (*mcpgo.CallToolResult, error) {
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
		return toolErr("state_query: %v", err)
	}
	// Decode generically and re-serialize as JSON for the agent.
	var generic any
	if err := appchannel.UnpackParams(res, &generic); err != nil {
		return toolErr("state_query: decode result: %v", err)
	}
	return toolJSON(generic)
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
	return toolJSON(map[string]any{
		"session_id":    s.ID,
		"lines":         lines,
		"dropped_lines": dropped,
	})
}

func (h *Handler) handleAppPerfGet(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	samples, dropped := s.DrainPerf()
	return toolJSON(map[string]any{
		"session_id":      s.ID,
		"samples":         samples,
		"dropped_samples": dropped,
	})
}

// appChannelDefinitions returns the MCP tool surface.
func appChannelDefinitions() []mcpgo.Tool {
	return []mcpgo.Tool{
		mcpgo.NewTool("app_channel_start",
			mcpgo.WithDescription("Open a fresh TCP listener for the bidirectional MessagePack RPC channel (🎯T75). Returns {listener_id, port, hosts}. Apps configured with the matching host:port (e.g. via spyder's launch_app env=LOG_TARGET=… or app-specific equivalent) connect, perform a `hello` handshake advertising their app_name/version/supported methods, and become addressable via the app_* tools below."),
			mcpgo.WithString("owner", mcpgo.Description("Free-form owner string for visibility in app_channel_list")),
		),
		mcpgo.NewTool("app_channel_stop",
			mcpgo.WithDescription("Close an appchannel listener and tear down all sessions accepted on it."),
			mcpgo.WithString("listener_id", mcpgo.Required(), mcpgo.Description("listener_id returned by app_channel_start")),
		),
		mcpgo.NewTool("app_channel_list",
			mcpgo.WithDescription("List active appchannel sessions across all listeners. Returns session_id, port, owner, app_name, app_version, methods advertised in hello."),
		),

		mcpgo.NewTool("app_ping", mcpgo.WithDescription("Ping the app (round-trip liveness check). Returns the timestamp the app saw."),
			mcpgo.WithString("session_id", mcpgo.Description("Defaults to the only live session if exactly one exists.")),
		),
		mcpgo.NewTool("app_quit", mcpgo.WithDescription("Ask the app to shut itself down cleanly (SDL_QUIT path → exit 0; no macOS crash notification). Falls back to terminate_app on timeout."),
			mcpgo.WithString("session_id"),
			mcpgo.WithNumber("timeout_ms", mcpgo.Description("How long to wait for the app to acknowledge the shutdown. Default 5000.")),
		),
		mcpgo.NewTool("app_flush", mcpgo.WithDescription("Ask the app to drain pending output (log queue, persistence) and acknowledge when done. Useful as a precondition for app_quit."),
			mcpgo.WithString("session_id"),
		),
		mcpgo.NewTool("app_background", mcpgo.WithDescription("Fire the platform background-transition notification in the app without touching device focus."),
			mcpgo.WithString("session_id"),
		),
		mcpgo.NewTool("app_foreground", mcpgo.WithDescription("Fire the platform foreground-transition notification in the app without touching device focus."),
			mcpgo.WithString("session_id"),
		),
		mcpgo.NewTool("app_low_memory", mcpgo.WithDescription("Fire the synthetic memory-pressure notification in the app (iOS: UIApplicationDidReceiveMemoryWarningNotification analog; Android: onTrimMemory)."),
			mcpgo.WithString("session_id"),
		),

		mcpgo.NewTool("app_pause", mcpgo.WithDescription("Pause the app's main loop (dt becomes 0; input/render continue so the app stays responsive)."),
			mcpgo.WithString("session_id"),
		),
		mcpgo.NewTool("app_resume", mcpgo.WithDescription("Resume normal pacing after app_pause/app_speed."),
			mcpgo.WithString("session_id"),
		),
		mcpgo.NewTool("app_step", mcpgo.WithDescription("Advance N frames while paused, then re-pause."),
			mcpgo.WithString("session_id"),
			mcpgo.WithNumber("frames", mcpgo.Description("Number of frames to advance (default 1)")),
		),
		mcpgo.NewTool("app_speed", mcpgo.WithDescription("Set a dt multiplier. 0.1 for slow-mo, 10.0 for soak. Persists until next app_speed or app_resume."),
			mcpgo.WithString("session_id"),
			mcpgo.WithNumber("multiplier", mcpgo.Required(), mcpgo.Description("Positive dt multiplier")),
		),

		mcpgo.NewTool("app_input", mcpgo.WithDescription("Inject a synthetic input event into the app's event loop. The `type` field determines the event shape; remaining args are passed through as params (e.g. {type: \"finger_down\", x: 0.5, y: 0.5}, {type: \"key_down\", key: \"a\"}, {type: \"accel\", x: 0.0, y: 1.0, z: 0.0})."),
			mcpgo.WithString("session_id"),
			mcpgo.WithString("type", mcpgo.Required(), mcpgo.Description("Event type: finger_down, finger_up, finger_motion, key_down, key_up, accel")),
		),

		mcpgo.NewTool("app_state", mcpgo.WithDescription("Query a named slice of the app's state. The app's hello advertises which slices it supports."),
			mcpgo.WithString("session_id"),
			mcpgo.WithString("slice", mcpgo.Required(), mcpgo.Description("State slice name (e.g. \"scene\", \"physics\", \"hud\")")),
		),
		mcpgo.NewTool("app_save_state", mcpgo.WithDescription("Ask the app to serialize its state. Returns {state_b64, size}; pass the b64 blob back via app_restore_state."),
			mcpgo.WithString("session_id"),
		),
		mcpgo.NewTool("app_restore_state", mcpgo.WithDescription("Load a previously-captured state blob (from app_save_state) back into the app."),
			mcpgo.WithString("session_id"),
			mcpgo.WithString("state_b64", mcpgo.Required(), mcpgo.Description("base64-encoded state blob")),
		),
		mcpgo.NewTool("app_screenshot", mcpgo.WithDescription("Request a screenshot from the app's own framebuffer (sibling to spyder's DTX-based `screenshot`; useful when DTX is wedged or you need state-correlated capture)."),
			mcpgo.WithString("session_id"),
		),

		mcpgo.NewTool("app_log_get", mcpgo.WithDescription("Drain structured log lines the app has pushed since the last call. Capture continues."),
			mcpgo.WithString("session_id"),
		),
		mcpgo.NewTool("app_perf_get", mcpgo.WithDescription("Drain perf-counter samples the app has pushed since the last call. Capture continues."),
			mcpgo.WithString("session_id"),
		),
	}
}

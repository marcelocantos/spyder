// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Tool-class deadlines (🎯T99.1). Tunable vars so tests can shorten them.
// Zero means no extra timeout beyond the caller's context.
var (
	// DeadlineFastRead covers catalogue/read tools (devices, resolve, status, …).
	DeadlineFastRead = 15 * time.Second
	// DeadlineDeviceOp covers typical device mutations (launch, screenshot, …).
	DeadlineDeviceOp = 60 * time.Second
	// DeadlineInstall covers install/deploy (zipconduit + cold tunnel).
	DeadlineInstall = 5 * time.Minute
	// DeadlinePerfFPS covers perf_fps: max window_sec is 120, plus margin
	// for gfxinfo reset/dump and adb RTT (🎯T111). Must stay ≥ max window
	// + DeadlinePerfFPSMargin or long windows time out mid-measure.
	DeadlinePerfFPS       = 150 * time.Second
	DeadlinePerfFPSMargin = 30 * time.Second
	// DeadlineWaitState covers wait_state: max timeout_ms is 120s plus
	// a poll/fetch margin so a last attempt is not killed by dispatch.
	DeadlineWaitState       = 150 * time.Second
	DeadlineWaitStateMargin = 30 * time.Second
)

// toolDeadlineByName lists explicit deadline classes. Unlisted verbs use
// DeadlineDeviceOp via toolDeadlineClass. Keys must stay ⊆ toolHandlers()
// (TestDeadlineNamesSubsetOfHandlers, ENT-005).
var toolDeadlineByName = func() map[string]func() time.Duration {
	m := map[string]func() time.Duration{}
	add := func(d func() time.Duration, names ...string) {
		for _, n := range names {
			m[n] = d
		}
	}
	// ensure_session / launch_player may install on the deploy path
	// (🎯T103: a cold emulator adb install must fit under this class).
	add(func() time.Duration { return DeadlineInstall }, "install_app", "deploy_app", "ensure_session", "launch_player")
	// Explicit class: window_sec max 120 must fit under this bound.
	add(func() time.Duration { return DeadlinePerfFPS }, "perf_fps")
	add(func() time.Duration { return DeadlineWaitState }, "wait_state", "wait_app_session")
	// Owner gates and long shell graphs; no extra wall-clock cap.
	add(func() time.Duration { return 0 }, "verify")
	// Optional wait for the alert, then the XCUITest runner (🎯T154).
	// agent_ensure may install and start the resident runner (🎯T163).
	add(func() time.Duration { return 5 * time.Minute }, "system_alert_tap", "agent_ensure")
	add(func() time.Duration { return DeadlineDeviceOp }, "system_alert")
	add(func() time.Duration { return DeadlineFastRead },
		"verify_status", "verify_answer", "verify_abort", "verify_dismiss", "verify_detail",
		"verify_review", "verify_report", "verify_now")
	// Outer dispatch must not undercut max_duration_ms (default 30s,
	// ceiling maxExecDuration). FastRead (15s) was killing multi-step
	// device recipes that correctly request 60–120s budgets.
	add(func() time.Duration { return maxExecDuration + 5*time.Second }, "app_exec", "run_script", "list_scripts")
	add(func() time.Duration { return DeadlineFastRead },
		"devices", "resolve", "device_state", "battery_history", "list_apps", "is_running",
		"reservations", "runs_list", "runs_show",
		"app_channel_list", "app_channel_diagnose", "agent_status")
	add(func() time.Duration { return DeadlineDeviceOp },
		"screenshot", "launch_app", "terminate_app", "uninstall_app",
		"rotate", "network", "record_start", "record_stop",
		"port_forward_start", "port_forward_stop", "input_tap", "input_swipe",
		"input_key", "notification_shade", "notification_tap_first",
		"crashes", "logs", "baseline_update", "diff",
		"reserve", "release", "renew",
		"sim_list", "sim_create", "sim_boot", "sim_shutdown",
		"app_channel_stop",
		"app_screenshot", "app_input", "app_pause", "app_step",
		"app_tweak_list", "app_tweak_get", "app_tweak_set", "app_tweak_reset",
		"app_log_get", "app_spawn", "app_quit", "app_ping", "app_flush",
		"app_background", "app_foreground", "app_state",
		"agent_idle")
	return m
}()

// toolDeadlineExplicitNames returns every tool name with a non-default entry
// in toolDeadlineByName.
func toolDeadlineExplicitNames() []string {
	names := make([]string, 0, len(toolDeadlineByName))
	for n := range toolDeadlineByName {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// toolDeadlineClass returns the wall-clock bound for a tool name.
func toolDeadlineClass(name string) time.Duration {
	if get, ok := toolDeadlineByName[name]; ok {
		return get()
	}
	return DeadlineDeviceOp
}

// InFlightOp is one tool call still running (🎯T99.5).
type InFlightOp struct {
	Tool      string    `json:"tool"`
	Device    string    `json:"device,omitempty"`
	Started   time.Time `json:"started"`
	ElapsedMs int64     `json:"elapsed_ms"` // filled on snapshot
}

// opRegistry tracks in-flight dispatches for spyder status / health.
type opRegistry struct {
	mu   sync.Mutex
	next uint64
	ops  map[uint64]inFlightEntry
}

type inFlightEntry struct {
	tool    string
	device  string
	started time.Time
}

func newOpRegistry() *opRegistry {
	return &opRegistry{ops: map[uint64]inFlightEntry{}}
}

func (r *opRegistry) begin(tool, device string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	id := r.next
	r.ops[id] = inFlightEntry{tool: tool, device: device, started: time.Now()}
	return id
}

func (r *opRegistry) end(id uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.ops, id)
}

func (r *opRegistry) snapshot() []InFlightOp {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	out := make([]InFlightOp, 0, len(r.ops))
	for _, e := range r.ops {
		out = append(out, InFlightOp{
			Tool:      e.tool,
			Device:    e.device,
			Started:   e.started,
			ElapsedMs: now.Sub(e.started).Milliseconds(),
		})
	}
	return out
}

// withToolDeadline derives a child context with the tool-class timeout, or
// returns ctx unchanged when the class deadline is zero.
func withToolDeadline(ctx context.Context, name string) (context.Context, context.CancelFunc) {
	d := toolDeadlineClass(name)
	if d <= 0 {
		return context.WithCancel(ctx)
	}
	// Honour a tighter parent deadline if already set.
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < d {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}

// formatTimeoutError builds the structured agent-visible timeout message.
func formatTimeoutError(tool, device string, elapsed time.Duration) string {
	if device != "" {
		return fmt.Sprintf("tool timeout: %s on %s after %s", tool, device, elapsed.Round(time.Millisecond))
	}
	return fmt.Sprintf("tool timeout: %s after %s", tool, elapsed.Round(time.Millisecond))
}

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"fmt"
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
)

// toolDeadlineClass returns the wall-clock bound for a tool name.
func toolDeadlineClass(name string) time.Duration {
	switch name {
	case "install_app", "deploy_app":
		return DeadlineInstall
	case "devices", "resolve", "device_state", "list_apps", "is_running",
		"reservations", "runs_list", "runs_show", "runs_artefacts",
		"app_channel_list", "health", "app_exec", "list_scripts", "run_script":
		// app_exec/run_script have their own max_duration; still bound outer dispatch.
		return DeadlineFastRead
	case "screenshot", "launch_app", "terminate_app", "uninstall_app",
		"rotate", "network", "record_start", "record_stop",
		"crashes", "logs", "log_stream", "baseline_update", "diff",
		"reserve", "release", "renew",
		"sim_list", "sim_create", "sim_boot", "sim_shutdown", "sim_erase",
		"app_channel_start", "app_channel_stop",
		"app_screenshot", "app_input", "app_pause", "app_step",
		"app_tweak_list", "app_tweak_get", "app_tweak_set", "app_tweak_reset",
		"app_log_get", "app_spawn", "app_quit", "app_ping", "app_flush",
		"app_background", "app_foreground", "app_state",
		"launch_player":
		return DeadlineDeviceOp
	default:
		return DeadlineDeviceOp
	}
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

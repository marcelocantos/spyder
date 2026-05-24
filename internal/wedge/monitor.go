// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package wedge

import (
	"bufio"
	"context"
	"log/slog"
	"os/exec"
	"sync"
	"time"
)

// Tunables for the wedge monitor. Declared as vars (not consts) so
// tests can shorten them for fast lifecycle assertions.
var (
	// pollInterval bounds detection latency in the polling-only path
	// (when log-tail is unavailable or its predicate breaks across
	// macOS versions). 30s is a useful upper bound — wedges have
	// historically been undetected for hours; 30s is a dramatic
	// improvement and adds no meaningful load (two cheap process/
	// library calls per cycle).
	pollInterval = 30 * time.Second

	// debounceDelay coalesces concurrent `MuxReceivedUSBData device
	// disconnected` events into a single parity check. When multiple
	// devices renegotiate on a display-wake transition, log-tail can
	// see several disconnect lines within a few hundred ms; doing one
	// parity check at the end is sufficient and avoids racing the
	// kernel's USB re-enumeration.
	debounceDelay = 5 * time.Second
)

// Injectable seams for testing reconcile's episode logic without shelling
// out (ioreg / go-ios / sudo). Production wires them to the real functions.
var (
	isWedgedFn = IsWedged
	captureFn  = Capture
	recoverFn  = AttemptRecovery
)

// wedgeState tracks the monitor's view of the current wedge episode so
// auto-recovery fires at most once per episode (🎯T72.5).
type wedgeState struct {
	// inEpisode is true while the system is continuously wedged. It resets
	// when a parity check comes back healthy, opening a fresh episode for
	// the next wedge.
	inEpisode bool
	// attempted is true once auto-recovery has fired in the current episode.
	attempted bool
}

// RunMonitor blocks until ctx is cancelled, running the wedge
// detection loop. Two trigger sources funnel into a single
// reconciliation step:
//
//   - 30s polling timer (catches wedges from any path)
//   - log-stream tail of usbmuxd for `MuxReceivedUSBData device
//     disconnected` (catches the known phantom-disconnect path
//     within a few seconds of trigger)
//
// On wedge detection the monitor always writes a snapshot via Capture.
// Auto-recovery (killusbmuxd) fires at most ONCE per wedge episode — the
// first detection of a continuous wedge — and then gives up until the
// system recovers and wedges again. The old behaviour (one kill every 2
// minutes for as long as the wedge persisted) was actively harmful when
// the wedge is device-side: the kills did nothing but churn usbmuxd. A
// deliberate retry is now an explicit operator action (`spyder doctor
// --fix`). (🎯T72.5.)
//
// The function performs an immediate parity check at startup before
// the first timer tick — handles the case where the daemon starts
// into an already-wedged system.
//
// (🎯T68.2.)
func RunMonitor(ctx context.Context) {
	triggers := make(chan string, 8)

	go pollTrigger(ctx, triggers)
	go logTailTrigger(ctx, triggers)

	var st wedgeState

	// Immediate startup check.
	reconcile(ctx, "startup", &st)

	for {
		select {
		case <-ctx.Done():
			return
		case src := <-triggers:
			reconcile(ctx, src, &st)
		}
	}
}

// reconcile runs one parity check and, on a wedge, always writes a
// snapshot; it fires auto-recovery only on the rising edge of a wedge
// episode (st tracks that). Failures of the parity check itself (ioreg /
// ListDevices error) are logged but do not stop the monitor.
func reconcile(ctx context.Context, source string, st *wedgeState) {
	wedged, iousb, usbmux, err := isWedgedFn()
	if err != nil {
		slog.Error("wedge: parity check failed",
			"source", source, "error", err.Error())
		return
	}
	if !wedged {
		if st.inEpisode {
			slog.Info("wedge: cleared", "source", source, "iousb", iousb, "usbmux", usbmux)
		} else {
			slog.Debug("wedge: parity check ok",
				"source", source, "iousb", iousb, "usbmux", usbmux)
		}
		st.inEpisode = false
		st.attempted = false
		return
	}

	newEpisode := !st.inEpisode
	st.inEpisode = true
	slog.Warn("wedge: detected by monitor",
		"source", source, "iousb", iousb, "usbmux", usbmux, "new_episode", newEpisode)

	// Detection + snapshot always run.
	captureFn("", "wedge.monitor."+source)

	if newEpisode && !st.attempted {
		st.attempted = true
		_ = recoverFn(ctx)
		return
	}
	slog.Info("wedge: auto-recovery already attempted this episode; not retrying "+
		"(kill-usbmuxd churn disabled — run `spyder doctor --fix` to retry, or unplug+replug the device)",
		"source", source)
}

// pollTrigger fires a "poll" trigger every pollInterval until ctx
// cancels. Drops triggers silently if the reconciler is already
// backlogged (the buffer is enough to absorb bursts; sustained
// backlog means the reconciler itself is wedged, in which case
// adding more work won't help).
func pollTrigger(ctx context.Context, triggers chan<- string) {
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			select {
			case triggers <- "poll":
			default:
			}
		}
	}
}

// logTailTrigger runs `log stream --process usbmuxd` filtered for
// MuxReceivedUSBData device disconnected events. Each line schedules
// a debounced "log-tail" trigger; concurrent disconnects within
// debounceDelay collapse to one trigger.
//
// Best-effort: if the subprocess fails to start or exits unexpectedly,
// the function returns silently. The polling timer is the safety net.
func logTailTrigger(ctx context.Context, triggers chan<- string) {
	cmd := exec.CommandContext(ctx, "log", "stream",
		"--process", "usbmuxd",
		"--info",
		"--predicate", `eventMessage CONTAINS "MuxReceivedUSBData device disconnected"`,
		"--style", "compact",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		slog.Error("wedge: log-tail stdout pipe failed; falling back to polling only",
			"error", err.Error())
		return
	}
	if err := cmd.Start(); err != nil {
		slog.Error("wedge: log-tail subprocess start failed; falling back to polling only",
			"error", err.Error())
		return
	}
	slog.Info("wedge: log-tail started", "pid", cmd.Process.Pid)
	defer func() {
		_ = cmd.Wait()
		slog.Info("wedge: log-tail exited")
	}()

	var debounceMu sync.Mutex
	var debounceTimer *time.Timer
	fire := func() {
		select {
		case triggers <- "log-tail":
		default:
		}
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		debounceMu.Lock()
		if debounceTimer != nil {
			debounceTimer.Stop()
		}
		debounceTimer = time.AfterFunc(debounceDelay, fire)
		debounceMu.Unlock()
	}
	if err := scanner.Err(); err != nil {
		slog.Error("wedge: log-tail read failed", "error", err.Error())
	}
}

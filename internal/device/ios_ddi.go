// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Developer-services self-heal (🎯T191).
//
// On iOS 17+ the app service (com.apple.coredevice.appservice, behind the
// resident agent's process list, AppPID and launch) only runs once the
// personalized developer disk image is mounted. A device that rebooted
// comes back without it. Apple's tools mount it on demand; go-ios does
// not, so every app-service connect is reset by the device — "could not
// read frame … connection reset by peer" — and automation never starts.
// The instruments services keep working, which hides the cause.
//
// Any devicectl developer operation mounts the image as a side effect;
// `devicectl device info processes` is read-only. The healer runs it once
// per device per ddiHealEvery and the caller retries.

// ddiHealEvery bounds how often one device is healed, so a device that
// genuinely cannot mount (locked before first unlock, untrusted) is not
// hammered every tick.
const ddiHealEvery = 2 * time.Minute

// ddiHealTimeout bounds one devicectl call. It must stay well under the
// dispatch deadline of the verbs that open the app service.
const ddiHealTimeout = 30 * time.Second

// isDDIUnavailable reports whether err is the device refusing the app
// service, as it does while the developer disk image is not mounted.
func isDDIUnavailable(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	if !strings.Contains(s, "ConnectToXpcServiceTunnelIface") {
		return false
	}
	return strings.Contains(s, "connection reset") || strings.Contains(s, "could not read frame") ||
		strings.Contains(s, "EOF")
}

type ddiHealer struct {
	mu   sync.Mutex
	last map[string]time.Time
	now  func() time.Time
	run  func(ctx context.Context, udid string) error
}

func newDDIHealer() *ddiHealer {
	return &ddiHealer{last: map[string]time.Time{}, now: time.Now, run: devicectlMountDDI}
}

// heal mounts udid's developer services unless it tried recently. It
// reports whether it ran and succeeded, i.e. whether a retry is worthwhile.
func (h *ddiHealer) heal(udid string) bool {
	if h == nil || udid == "" {
		return false
	}
	h.mu.Lock()
	if t, ok := h.last[udid]; ok && h.now().Sub(t) < ddiHealEvery {
		h.mu.Unlock()
		return false
	}
	h.last[udid] = h.now()
	h.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), ddiHealTimeout)
	defer cancel()
	started := time.Now()
	if err := h.run(ctx, udid); err != nil {
		slog.Warn("ios: developer services unavailable and devicectl could not mount them",
			"udid", udid, "error", err, "duration_ms", time.Since(started).Milliseconds())
		return false
	}
	slog.Info("ios: mounted developer services via devicectl (app service was refused)",
		"udid", udid, "duration_ms", time.Since(started).Milliseconds())
	return true
}

func devicectlMountDDI(ctx context.Context, udid string) error {
	out, err := exec.CommandContext(ctx, "xcrun", "devicectl", "device", "info", "processes",
		"--device", udid, "--quiet", "--timeout", "25").CombinedOutput()
	if err != nil {
		return &execError{err: err, out: truncate(strings.TrimSpace(string(out)), 240)}
	}
	return nil
}

type execError struct {
	err error
	out string
}

func (e *execError) Error() string { return e.err.Error() + ": " + e.out }
func (e *execError) Unwrap() error { return e.err }

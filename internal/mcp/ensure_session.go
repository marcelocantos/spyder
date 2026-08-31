// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// ensure_session (🎯T118): one verb from device+bundle to a ready
// app-channel session. Deploys if the artifact isn't installed, launches
// with SPYDER_APP_CHANNEL wired, waits for the app's hello handshake,
// and returns the session id. Idempotent when a healthy session already
// exists — that fast path mutates nothing, so it is not reservation-gated;
// only the deploy/launch path goes through authorize().
package mcp

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/appchannel"
)

// postLaunchSessionWait bounds how long launch_app / deploy_app wait for
// the launched app to dial back and complete the app-channel handshake
// before returning without a session_id (🎯T119/🎯T121). Non-channel apps
// never connect, so this stays short; ensure_session takes the longer,
// explicit wait. Var so tests can shorten it.
var postLaunchSessionWait = 1500 * time.Millisecond

const (
	// defaultEnsureSessionWait / maxEnsureSessionWait bound ensure_session's
	// handshake wait (timeout_ms).
	defaultEnsureSessionWait = 15 * time.Second
	maxEnsureSessionWait     = 90 * time.Second
	// sessionPollInterval paces the handshake polls.
	sessionPollInterval = 25 * time.Millisecond
	// defaultReadyWait is the absolute outer bound for the post-hello
	// state_query readiness wait (ctx). Idle silence for the call itself
	// is progress-aware (appchannel.DefaultIdleSilence + progress beats).
	defaultReadyWait = 3 * time.Minute
)

// ensureSessionResult is the JSON payload returned by ensure_session.
// Deployed/Launched report what this call actually did — both false on
// the idempotent fast path.
type ensureSessionResult struct {
	Device      string `json:"device"`
	BundleID    string `json:"bundle_id"`
	SessionID   string `json:"session_id"`
	ChannelPort int    `json:"channel_port"`
	PID         int    `json:"pid,omitempty"`
	Deployed    bool   `json:"deployed"`
	Launched    bool   `json:"launched"`
}

func (h *Handler) handleEnsureSession(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.appChannel == nil {
		return toolErr("app channel not configured")
	}
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	bundleID := optString(args, "bundle_id")
	path := optString(args, "path")
	if bundleID == "" && path == "" {
		return toolErr("ensure_session: bundle_id or path is required")
	}
	owner := optString(args, "owner")
	env := optStringMap(args, "env")
	wait := defaultEnsureSessionWait
	if ms, ok := args["timeout_ms"].(float64); ok && ms > 0 {
		wait = min(time.Duration(ms)*time.Millisecond, maxEnsureSessionWait)
	}

	h.mu.Lock()
	adapter, platform, id, err := h.resolveAdapter(dev)
	if err != nil {
		h.mu.Unlock()
		return toolErr("%v", err)
	}
	if bundleID == "" {
		bundleID, err = deriveBundleID(platform, path)
		if err != nil {
			h.mu.Unlock()
			return toolErr("ensure_session: cannot derive bundle_id from %q: %v — pass bundle_id explicitly", path, err)
		}
	}
	h.mu.Unlock()

	key := appchannel.AppKey{DeviceID: id, BundleID: bundleID}

	// Fast path: a healthy session already exists — return it untouched.
	if s, ok := h.waitForChannelSession(key, 0); ok {
		if sessionHealthy(s) {
			return toolJSON(ensureSessionResult{
				Device:      dev,
				BundleID:    bundleID,
				SessionID:   s.ID,
				ChannelPort: s.Port,
			})
		}
		// Wedged session (open TCP conn, unresponsive app) — drop it so
		// the relaunch below forms a fresh one.
		_ = s.Close()
	}

	deployed, launched := false, false
	var pid int
	errRes := func() *mcpgo.CallToolResult {
		h.mu.Lock()
		defer h.mu.Unlock()
		if res := h.authorize(dev, owner); res != nil {
			return res
		}
		if path != "" {
			vpath, verr := validateAppPath(path)
			if verr != nil {
				res, _ := toolErr("ensure_session: %v", verr)
				return res
			}
			installed := false
			if apps, listErr := adapter.ListApps(id); listErr == nil {
				for _, app := range apps {
					if app.BundleID == bundleID {
						installed = true
						break
					}
				}
			}
			if !installed {
				if ierr := h.installOn(adapter, id, vpath); ierr != nil {
					res, _ := toolErr("ensure_session: install %s on %s: %v", filepath.Base(vpath), dev, ierr)
					return res
				}
				deployed = true
			}
		}
		// A running instance can't adopt the channel env after the fact —
		// soft-quit then hard-terminate so the relaunch picks up SPYDER_APP_CHANNEL.
		if p, perr := adapter.AppPID(id, bundleID); perr == nil && p > 0 {
			h.softQuitRunningApp(id, bundleID)
			h.waitUntilNotRunning(adapter, id, bundleID, 2*time.Second)
			if terr := adapter.TerminateApp(id, bundleID); terr != nil && !isNotRunningError(terr) {
				res, _ := toolErr("ensure_session: terminate %s on %s: %v", bundleID, dev, terr)
				return res
			}
		}
		var eerr error
		env, eerr = h.ensureAppChannelEnv(env, platform, id, bundleID)
		if eerr != nil {
			res, _ := toolErr("ensure_session: %v", eerr)
			return res
		}
		if lerr := adapter.LaunchApp(id, bundleID, env); lerr != nil {
			res, _ := toolErr("ensure_session: launch %s on %s: %v", bundleID, dev, lerr)
			return res
		}
		launched = true
		h.launchTimes[launchKey{deviceID: id, bundleID: bundleID}] = time.Now()
		var perr error
		pid, perr = waitForAppPID(adapter, id, bundleID, 3*time.Second)
		if perr != nil {
			res, _ := toolErr("ensure_session: verify pid for %s on %s: %v", bundleID, dev, perr)
			return res
		}
		return nil
	}()
	if errRes != nil {
		return errRes, nil
	}

	s, ok := h.waitForChannelSession(key, wait)
	if !ok {
		return toolErr("ensure_session: %s launched on %s (pid %d) but no app-channel session formed within %s — the app may not support SPYDER_APP_CHANNEL; check app_channel_list()", bundleID, dev, pid, wait)
	}
	// After hello, wait until the app can answer a main-thread slice.
	// Channel connect ≠ main pump free (Unity cold load).
	if !waitForSessionReady(s, defaultReadyWait) {
		return toolErr("ensure_session: session %s formed but app never answered state_query within %s (main thread stalled?)", s.ID, defaultReadyWait)
	}
	return toolJSON(ensureSessionResult{
		Device:      dev,
		BundleID:    bundleID,
		SessionID:   s.ID,
		ChannelPort: s.Port,
		PID:         pid,
		Deployed:    deployed,
		Launched:    launched,
	})
}

// waitForChannelSession polls the keyed listener for (device, bundle)
// until a handshaken session appears or timeout elapses. timeout 0 means
// a single check. Returns immediately when no keyed listener exists — no
// listener means the launch never wired SPYDER_APP_CHANNEL, so nothing
// can ever connect.
func (h *Handler) waitForChannelSession(key appchannel.AppKey, timeout time.Duration) (*appchannel.Session, bool) {
	if h.appChannel == nil {
		return nil, false
	}
	l, ok := h.appChannel.LookupKeyed(key)
	if !ok {
		return nil, false
	}
	deadline := time.Now().Add(timeout)
	for {
		if sessions := l.Sessions(); len(sessions) > 0 {
			// Most recent session — matters after a relaunch when a dying
			// predecessor may still be draining.
			return sessions[len(sessions)-1], true
		}
		if !time.Now().Before(deadline) {
			return nil, false
		}
		time.Sleep(sessionPollInterval)
	}
}

// sessionHealthy verifies the app still answers on the channel. The
// point is to detect a dead peer behind an open TCP connection, not
// capability — an app that doesn't advertise ping counts as healthy.
func sessionHealthy(s *appchannel.Session) bool {
	_, err := s.Call(context.Background(), appchannel.MethodPing, nil, 2*time.Second)
	if err == nil {
		return true
	}
	var rpcErr *appchannel.RPCError
	if errors.As(err, &rpcErr) && rpcErr.Code == appchannel.ErrCodeUnsupported {
		return true
	}
	return false
}

// waitForSessionReady waits for one successful state_query after hello.
// Uses progress-aware Call (idle silence, not wall-clock from t0) with an
// absolute outer bound on ctx. Kit progress heartbeats keep the call alive
// while main work is queued; true silence fails after DefaultIdleSilence.
func waitForSessionReady(s *appchannel.Session, absolute time.Duration) bool {
	if s == nil || absolute <= 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), absolute)
	defer cancel()
	_, err := s.Call(ctx, appchannel.MethodStateQuery,
		map[string]string{"slice": "session"}, 0)
	if err == nil {
		return true
	}
	var rpcErr *appchannel.RPCError
	if errors.As(err, &rpcErr) && rpcErr.Code == appchannel.ErrCodeUnsupported {
		return true
	}
	return false
}

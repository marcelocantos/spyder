// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/marcelocantos/spyder/internal/devpolicy"
	"github.com/marcelocantos/spyder/internal/paths"
	"github.com/marcelocantos/spyder/internal/usbspeed"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/vmihailenco/msgpack/v5"
)

func (h *Handler) developmentPolicy() *devpolicy.Controller {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.devPolicy != nil {
		return h.devPolicy
	}
	h.devPolicy = &devpolicy.Controller{
		Store: &devpolicy.Store{Path: filepath.Join(paths.Base(), "dev-policy.json")},
		Peers: func() []devpolicy.Peer {
			if h.appChannel == nil {
				return nil
			}
			var out []devpolicy.Peer
			for _, s := range h.appChannel.Sessions() {
				l := s.Listener()
				if l == nil || l.Key.BundleID == "" || l.Key.DeviceID == "" {
					continue
				}
				out = append(out, devpolicy.Peer{ID: s.ID, DeviceID: l.Key.DeviceID, BundleID: l.Key.BundleID, Capable: s.Supports("dev_session_configure") || s.Supports("dev_session_configure_v2"), AdsCapable: s.Supports("dev_session_configure_v2")})
			}
			return out
		},
		USB: readDevelopmentUSB,
		Authorize: func(id, owner string) error {
			if h.reservations == nil {
				return nil
			}
			return h.reservations.Authorize(id, owner)
		},
		AuthorizeSession: func(id, owner string) error {
			if h.reservations == nil {
				return errors.New("session overrides require an active reservation")
			}
			hold, ok := h.reservations.Get(id)
			if !ok || hold.Owner != owner {
				return errors.New("session override owner does not hold this device")
			}
			return nil
		},
		Call: func(ctx context.Context, p devpolicy.Peer, f devpolicy.Flags, lease time.Duration) (any, error) {
			s, ok := h.appChannel.GetSession(p.ID)
			if !ok {
				return nil, errors.New("session disconnected")
			}
			method := "dev_session_configure"
			if p.AdsCapable {
				method = "dev_session_configure_v2"
			}
			raw, err := s.Call(ctx, method, map[string]any{"flags": f, "lease_ms": lease.Milliseconds()}, devpolicy.CallTimeout)
			if err != nil {
				return nil, err
			}
			var ack map[string]any
			if err = msgpack.Unmarshal(raw, &ack); err != nil {
				return nil, fmt.Errorf("invalid configuration acknowledgement: %w", err)
			}
			if applied, ok := ack["applied"].(bool); !ok || !applied {
				return ack, fmt.Errorf("app did not acknowledge applied=true: %v", ack)
			}
			if p.AdsCapable {
				actual, ok := ack["flags"].(map[string]any)
				if !ok || len(actual) != len(f) {
					return ack, errors.New("app acknowledged different flags")
				}
				for k, v := range f {
					if actual[k] != v {
						return ack, errors.New("app acknowledged different flags")
					}
				}
				suppressed, validSuppression := ack["ads_suppressed"].(bool)
				if !validSuppression || suppressed != (f["ads"] == "suppressed") {
					return ack, errors.New("invalid effective ads acknowledgement")
				}
				active, ok := ack["active"].(bool)
				remaining, valid := developmentPolicyInteger(ack["remaining_lease_ms"])
				if !ok || active != (len(f) > 0) || !valid || (active && (remaining <= 0 || remaining > lease.Milliseconds())) || (!active && remaining != 0) {
					return ack, errors.New("invalid active lease acknowledgement")
				}
			}
			return ack, nil
		},
	}
	return h.devPolicy
}

// StartDevelopmentPolicy is tied to the daemon lifetime; lease expiry restores
// the app even if the daemon exits without delivering a final revoke.
func (h *Handler) StartDevelopmentPolicy(ctx context.Context) { h.developmentPolicy().Run(ctx) }

func (h *Handler) handleDevPolicy(args map[string]any) (*mcpgo.CallToolResult, error) {
	c := h.developmentPolicy()
	op := optString(args, "op")
	if op == "" {
		op = "get"
	}
	switch op {
	case "session_set", "session_clear":
		allowed := map[string]bool{"op": true, "owner": true, "run_token": true, "device_id": true, "bundle_id": true}
		if op == "session_set" {
			allowed["flags"] = true
			allowed["lease_ms"] = true
		}
		for k := range args {
			if !allowed[k] {
				return toolErr("dev_policy: unknown session parameter %s", k)
			}
		}
		s := devpolicy.SessionOverride{Owner: optString(args, "owner"), RunToken: optString(args, "run_token"), DeviceID: optString(args, "device_id"), BundleID: optString(args, "bundle_id")}
		if op == "session_set" {
			b, err := json.Marshal(args["flags"])
			if err != nil {
				return toolErr("dev_policy: %v", err)
			}
			if err = json.Unmarshal(b, &s.Flags); err != nil {
				return toolErr("dev_policy: flags must be a string map")
			}
			n, ok := developmentPolicyInteger(args["lease_ms"])
			if !ok || n < 1 || n > devpolicy.MaxSessionLease.Milliseconds() {
				return toolErr("dev_policy: lease_ms must be between 1 and 30000")
			}
			if err = c.SetSession(s, time.Duration(n)*time.Millisecond); err != nil {
				return toolErr("dev_policy: %v", err)
			}
		} else if err := c.ClearSession(s); err != nil {
			return toolErr("dev_policy: %v", err)
		}
	case "clear":
		owner := optString(args, "owner")
		if owner == "" {
			return toolErr("dev_policy clear requires owner")
		}
		cfg := devpolicy.Empty()
		cfg.Owner = owner
		if err := c.Store.Save(cfg); err != nil {
			return toolErr("dev_policy: %v", err)
		}
	case "set":
		raw, ok := args["config"]
		if !ok {
			return toolErr("dev_policy set requires config")
		}
		b, err := json.Marshal(raw)
		if err != nil {
			return toolErr("dev_policy: %v", err)
		}
		var cfg devpolicy.Config
		d := json.NewDecoder(strings.NewReader(string(b)))
		d.DisallowUnknownFields()
		if err = d.Decode(&cfg); err != nil {
			return toolErr("dev_policy: %v", err)
		}
		owner := optString(args, "owner")
		if owner == "" {
			return toolErr("dev_policy set requires owner")
		}
		cfg.Owner = owner
		if err = c.Store.Save(cfg); err != nil {
			return toolErr("dev_policy: %v", err)
		}
	case "get":
	default:
		return toolErr("dev_policy op must be get, set, clear, session_set or session_clear")
	}
	cfg, err := c.Store.Load()
	if err != nil {
		return toolErr("dev_policy: %v", err)
	}
	out := map[string]any{"path": c.Store.Path, "config": cfg, "sessions": c.Statuses(), "session_overrides": c.SessionOverrides(), "lease_ms": devpolicy.Lease.Milliseconds(), "poll_ms": devpolicy.PollInterval.Milliseconds()}
	if bundle := optString(args, "bundle_id"); bundle != "" {
		out["resolved"] = cfg.Resolve(bundle, nil)
	}
	return toolJSON(out)
}

// A USB mux connection alone is insufficient (it can be Wi-Fi). IOKit proves
// physical iOS attachment; adb's usb: transport token proves physical Android.
// Every sweep executes fresh, bounded enumerations. Any error fails closed.
func readDevelopmentUSB(ctx context.Context) (map[string]bool, error) {
	out, err := exec.CommandContext(ctx, "ioreg", "-p", "IOUSB", "-w0", "-c", "IOUSBHostDevice").Output()
	if err != nil {
		return nil, fmt.Errorf("IOKit census: %w", err)
	}
	if _, err = exec.LookPath("adb"); errors.Is(err, exec.ErrNotFound) {
		return developmentUSBFromCensuses(out, nil), nil
	} else if err != nil {
		return nil, err
	}
	adb, err := exec.CommandContext(ctx, "adb", "devices", "-l").Output()
	if err != nil {
		return nil, fmt.Errorf("ADB census: %w", err)
	}
	return developmentUSBFromCensuses(out, adb), nil
}

func developmentUSBFromCensuses(out, adb []byte) map[string]bool {
	attached := map[string]bool{}
	census := usbspeed.ParseCensus(out)
	for serial := range census {
		attached[serial] = true
	}
	for _, line := range strings.Split(string(adb), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[1] != "device" {
			continue
		}
		for _, field := range fields[2:] {
			if strings.HasPrefix(field, "usb:") && len(field) > 4 {
				attached[fields[0]] = true
			}
		}
	}
	return attached
}

// Only exact bounded integers cross the lease wire; fractional JSON numbers are
// never truncated into an apparently valid renewal.
func developmentPolicyInteger(v any) (int64, bool) {
	var n int64
	switch x := v.(type) {
	case int:
		n = int64(x)
	case int64:
		n = x
	case int32:
		n = int64(x)
	case uint:
		if x > 30000 {
			return 0, false
		}
		n = int64(x)
	case uint64:
		if x > 30000 {
			return 0, false
		}
		n = int64(x)
	case uint32:
		n = int64(x)
	case uint16:
		n = int64(x)
	case uint8:
		n = int64(x)
	case int16:
		n = int64(x)
	case int8:
		n = int64(x)
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) || x < 0 || x > 30000 || math.Trunc(x) != x {
			return 0, false
		}
		n = int64(x)
	default:
		return 0, false
	}
	return n, n >= 0 && n <= 30000
}

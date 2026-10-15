// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
				out = append(out, devpolicy.Peer{ID: s.ID, DeviceID: l.Key.DeviceID, BundleID: l.Key.BundleID, Capable: s.Supports("dev_session_configure")})
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
		Call: func(ctx context.Context, p devpolicy.Peer, f devpolicy.Flags, lease time.Duration) (any, error) {
			s, ok := h.appChannel.GetSession(p.ID)
			if !ok {
				return nil, errors.New("session disconnected")
			}
			raw, err := s.Call(ctx, "dev_session_configure", map[string]any{"flags": f, "lease_ms": lease.Milliseconds()}, devpolicy.CallTimeout)
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
		return toolErr("dev_policy op must be get, set, or clear")
	}
	cfg, err := c.Store.Load()
	if err != nil {
		return toolErr("dev_policy: %v", err)
	}
	out := map[string]any{"path": c.Store.Path, "config": cfg, "sessions": c.Statuses(), "lease_ms": devpolicy.Lease.Milliseconds(), "poll_ms": devpolicy.PollInterval.Milliseconds()}
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

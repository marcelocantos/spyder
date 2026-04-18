// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package tunneld is a client for pymobiledevice3's remote tunneld HTTP
// surface. Tunneld is load-bearing for iOS 17+ DVT operations
// (screenshot, app launch/terminate, sysmon). Spyder probes it at startup
// and lets DVT-dependent tool handlers gate on Ready() before shelling
// out to developer commands that would otherwise hang.
//
// Tunneld is typically externally managed (launchd / brew services /
// manual sudo invocation). This package does not spawn it — that remains
// opt-in via a separate supervisor (see 🎯T7 follow-up).
package tunneld

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultAddr is pymobiledevice3's tunneld default listen address.
const DefaultAddr = "127.0.0.1:49151"

// Client probes a tunneld HTTP endpoint.
type Client struct {
	addr string
	http *http.Client
}

// New returns a Client that probes tunneld at addr (e.g. "127.0.0.1:49151").
func New(addr string) *Client {
	return &Client{
		addr: addr,
		http: &http.Client{Timeout: 2 * time.Second},
	}
}

// Addr returns the host:port this client probes.
func (c *Client) Addr() string { return c.addr }

// Probe hits tunneld's root endpoint and returns the set of UDIDs it has
// paired tunnels for. Returns an error if tunneld is not reachable, not
// responding, or returning an unexpected body.
func (c *Client) Probe() (udids []string, err error) {
	resp, err := c.http.Get("http://" + c.addr + "/")
	if err != nil {
		return nil, fmt.Errorf("tunneld unreachable at %s: %w", c.addr, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tunneld returned %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading tunneld response: %w", err)
	}
	var devs map[string]json.RawMessage
	if err := json.Unmarshal(body, &devs); err != nil {
		return nil, fmt.Errorf("parsing tunneld response: %w", err)
	}
	udids = make([]string, 0, len(devs))
	for k := range devs {
		udids = append(udids, k)
	}
	return udids, nil
}

// ErrUnavailable is returned by Require when tunneld is not reachable.
// DVT-dependent tool handlers should wrap this into a structured
// user-facing error that suggests starting tunneld.
var ErrUnavailable = errors.New("tunneld unavailable — start `sudo pymobiledevice3 remote tunneld` or pass --tunneld-addr")

// Require probes tunneld and returns ErrUnavailable if it isn't ready.
// Cheap enough (HTTP GET with 2s timeout) to call before each DVT
// operation; callers that want caching can layer their own.
func (c *Client) Require() error {
	if _, err := c.Probe(); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

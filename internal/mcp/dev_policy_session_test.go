// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"fmt"
	"math"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/appchannel"
	"github.com/marcelocantos/spyder/internal/devpolicy"
)

func TestDevPolicySessionRESTAtomicTokenScopeAndHeldOwnership(t *testing.T) {
	h, holds := newHandlerWithReservations(t, nil, nil)
	c := h.developmentPolicy()
	c.Store = &devpolicy.Store{Path: filepath.Join(t.TempDir(), "policy.json")}
	a := map[string]any{"op": "session_set", "owner": "run", "run_token": "token", "device_id": "D1", "bundle_id": "app", "flags": map[string]any{"ads": "suppressed"}, "lease_ms": 10000.0}
	if r := dispatchJSON(t, h, "dev_policy", a); !r.IsError {
		t.Fatal("accepted unheld device")
	}
	if _, err := holds.Acquire("D1", "run", time.Minute, ""); err != nil {
		t.Fatal(err)
	}
	if r := dispatchJSON(t, h, "dev_policy", a); r.IsError {
		t.Fatal(resultText(t, &r))
	}
	before := c.SessionOverrides()[0]
	for _, mutation := range []map[string]any{{"run_token": "different"}, {"flags": map[string]any{"ads": true}}, {"flags": map[string]any{"ads": "no"}}, {"lease_ms": 100.5}, {"lease_ms": true}, {"lease_ms": 30001}, {"unknown": true}, {"owner": "foreign"}} {
		bad := map[string]any{}
		for k, v := range a {
			bad[k] = v
		}
		for k, v := range mutation {
			bad[k] = v
		}
		if r := dispatchJSON(t, h, "dev_policy", bad); !r.IsError {
			t.Fatal("accepted invalid request", bad)
		}
		got := c.SessionOverrides()[0]
		if got.ExpiresAt != before.ExpiresAt || got.Flags["ads"] != "suppressed" {
			t.Fatal("partial mutation", got)
		}
	}
	cfg, err := c.Store.Load()
	if err != nil || len(cfg.Global) != 0 || len(cfg.Apps) != 0 {
		t.Fatal("session persisted", cfg, err)
	}
	clear := map[string]any{"op": "session_clear", "owner": "run", "run_token": "different", "device_id": "D1", "bundle_id": "app"}
	if r := dispatchJSON(t, h, "dev_policy", clear); !r.IsError {
		t.Fatal("wrong-token clear accepted")
	}
	clear["run_token"] = "token"
	if r := dispatchJSON(t, h, "dev_policy", clear); r.IsError {
		t.Fatal(resultText(t, &r))
	}
	if len(c.SessionOverrides()) != 0 {
		t.Fatal("clear failed")
	}
}

func TestDevPolicyV2ActualHelloDispatchAndStrictAck(t *testing.T) {
	for _, kind := range []string{"exact", "missing", "wrong", "inactive", "expired", "suppression_mismatch", "excessive"} {
		t.Run(kind, func(t *testing.T) {
			h := startAppChannelHandler(t)
			_, port := openListener(t, h)
			conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			hello, _ := appchannel.PackParams(appchannel.Hello{AppName: "ads-contract", Methods: appchannel.MethodDescriptors("dev_session_configure", "dev_session_configure_v2")})
			if err = appchannel.WriteFrame(conn, &appchannel.Envelope{ID: 1, Method: appchannel.MethodHello, Params: hello}); err != nil {
				t.Fatal(err)
			}
			if _, err = appchannel.ReadFrame(conn); err != nil {
				t.Fatal(err)
			}
			received := make(chan *appchannel.Envelope, 1)
			go func() {
				env, e := appchannel.ReadFrame(conn)
				if e != nil {
					return
				}
				received <- env
				ack := map[string]any{"applied": true, "active": true, "ads_suppressed": true, "flags": map[string]any{"ads": "suppressed"}, "remaining_lease_ms": 10000}
				switch kind {
				case "missing":
					delete(ack, "flags")
				case "wrong":
					ack["flags"] = map[string]any{"ads": "normal"}
				case "inactive":
					ack["active"] = false
				case "expired":
					ack["remaining_lease_ms"] = 0
				case "excessive":
					ack["remaining_lease_ms"] = 10001
				case "suppression_mismatch":
					ack["ads_suppressed"] = false
				}
				raw, _ := appchannel.PackParams(ack)
				_ = appchannel.WriteFrame(conn, &appchannel.Envelope{ID: env.ID, Result: raw})
			}()
			sid := waitForAppSession(t, h)
			c := h.developmentPolicy()
			_, err = c.Call(context.Background(), devpolicy.Peer{ID: sid, AdsCapable: true}, devpolicy.Flags{"ads": "suppressed"}, 10*time.Second)
			if (err == nil) != (kind == "exact") {
				t.Fatal("incorrect acknowledgement acceptance", kind, err)
			}
			select {
			case env := <-received:
				if env.Method != "dev_session_configure_v2" {
					t.Fatal("used old SDK contract", env.Method)
				}
				var p map[string]any
				if appchannel.UnpackParams(env.Params, &p) != nil {
					t.Fatal("invalid params")
				}
				if p["flags"].(map[string]any)["ads"] != "suppressed" {
					t.Fatal(p)
				}
			case <-time.After(time.Second):
				t.Fatal("no dispatch")
			}
		})
	}
}

func TestDevPolicyExactLeaseIntegerRejectsFractionNonfiniteAndBool(t *testing.T) {
	for _, v := range []any{0.1, true, "10", math.NaN(), math.Inf(1), uint64(math.MaxUint64), int64(-1)} {
		if _, ok := developmentPolicyInteger(v); ok {
			t.Fatal("accepted invalid integer", v)
		}
	}
	for _, v := range []any{10000.0, int64(10000), uint16(10000)} {
		if n, ok := developmentPolicyInteger(v); !ok || n != 10000 {
			t.Fatal("rejected wire integer", v)
		}
	}
}

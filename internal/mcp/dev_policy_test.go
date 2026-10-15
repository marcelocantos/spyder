// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"fmt"
	"github.com/marcelocantos/spyder/internal/appchannel"
	"github.com/marcelocantos/spyder/internal/paths"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/devpolicy"
)

func TestDevPolicyRESTSurfaceSetGetClear(t *testing.T) {
	h := NewHandler()
	c := h.developmentPolicy()
	c.Store = &devpolicy.Store{Path: filepath.Join(t.TempDir(), "policy.json")}
	cfg := map[string]any{"version": 1, "global": map[string]any{}, "groups": []any{map[string]any{"name": "minicades", "bundle_prefixes": []any{"com.minicades."}, "flags": map[string]any{"orientation": "landscapeRight"}}}, "apps": map[string]any{}}
	r := dispatchJSON(t, h, "dev_policy", map[string]any{"op": "set", "config": cfg, "owner": "office", "bundle_id": "com.minicades.stockcars"})
	if r.IsError {
		t.Fatal(resultText(t, &r))
	}
	got := dispatchJSONMap(t, h, "dev_policy", map[string]any{"bundle_id": "com.minicades.stockcars"})
	resolved := got["resolved"].(map[string]any)
	if resolved["flags"].(map[string]any)["orientation"] != "landscapeRight" {
		t.Fatal(got)
	}
	cfg["global"] = map[string]any{"orientation": true}
	bad := dispatchJSON(t, h, "dev_policy", map[string]any{"op": "set", "config": cfg, "owner": "office"})
	if !bad.IsError {
		t.Fatal("accepted non-string flag")
	}
	loaded, err := c.Store.Load()
	if err != nil || loaded.Resolve("com.minicades.stockcars", nil).Flags["orientation"] != "landscapeRight" {
		t.Fatal("partial write", loaded, err)
	}
	clear := dispatchJSON(t, h, "dev_policy", map[string]any{"op": "clear", "owner": "office"})
	if clear.IsError {
		t.Fatal(resultText(t, &clear))
	}
	loaded, err = c.Store.Load()
	if err != nil || len(loaded.Resolve("com.minicades.stockcars", nil).Flags) != 0 {
		t.Fatal(loaded, err)
	}
}

func TestDevelopmentUSBProofRejectsWirelessAndOffline(t *testing.T) {
	iokit := []byte(`+-o iPhone <class IOUSBHostDevice, id 0x1000001, registered, matched, active, busy 0, retain 10>
 {"USB Serial Number" = "00008110000CFF123456789A"
 "Device Speed" = 2
 "bDeviceClass" = 0
 }`)
	adb := []byte("List of devices attached\nUSB123 device usb:1-2 product:samsung\n192.168.1.2:5555 device product:samsung\noffline123 offline usb:1-3\nemulator-5554 device product:sdk\n")
	got := developmentUSBFromCensuses(iokit, adb)
	if !got["00008110000CFF123456789A"] || !got["USB123"] {
		t.Fatal("missing physical USB", got)
	}
	for _, id := range []string{"192.168.1.2:5555", "offline123", "emulator-5554", "unseen-wifi-ios"} {
		if got[id] {
			t.Fatal("accepted non-physical transport", id)
		}
	}
}

func TestDevPolicyRealChannelRequiresExplicitApplicationAck(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		t.Run(fmt.Sprint(accepted), func(t *testing.T) {
			h := startAppChannelHandler(t)
			_, port := openListener(t, h)
			conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			hello, _ := appchannel.PackParams(appchannel.Hello{AppName: "dev-policy-contract", AppVersion: "1", Methods: appchannel.MethodDescriptors("dev_session_configure")})
			if err = appchannel.WriteFrame(conn, &appchannel.Envelope{ID: 1, Method: appchannel.MethodHello, Params: hello}); err != nil {
				t.Fatal(err)
			}
			if _, err = appchannel.ReadFrame(conn); err != nil {
				t.Fatal(err)
			}
			received := make(chan map[string]any, 1)
			go func() {
				env, readErr := appchannel.ReadFrame(conn)
				if readErr != nil {
					return
				}
				var params map[string]any
				if appchannel.UnpackParams(env.Params, &params) != nil {
					return
				}
				received <- params
				raw, _ := appchannel.PackParams(map[string]any{"applied": accepted})
				_ = appchannel.WriteFrame(conn, &appchannel.Envelope{ID: env.ID, Result: raw})
			}()
			sid := waitForAppSession(t, h)
			c := h.developmentPolicy()
			c.Store = &devpolicy.Store{Path: filepath.Join(t.TempDir(), "policy.json")}
			cfg := devpolicy.Empty()
			cfg.Global = devpolicy.Flags{"orientation": "landscapeRight"}
			if err = c.Store.Save(cfg); err != nil {
				t.Fatal(err)
			}
			c.USB = func(context.Context) (map[string]bool, error) { return map[string]bool{"test-device": true}, nil }
			c.Reconcile(context.Background())
			statuses := c.Statuses()
			if len(statuses) != 1 || statuses[0].SessionID != sid {
				t.Fatal(statuses)
			}
			if accepted {
				if statuses[0].Applied["orientation"] != "landscapeRight" || statuses[0].Error != "" {
					t.Fatal(statuses)
				}
			} else {
				if len(statuses[0].Applied) != 0 || statuses[0].Error == "" {
					t.Fatal("false applied status", statuses)
				}
			}
			select {
			case p := <-received:
				if n, ok := anyToInt(p["lease_ms"]); !ok || n != 10000 {
					t.Fatal(p)
				}
			case <-time.After(time.Second):
				t.Fatal("no lease request")
			}
		})
	}
}

func TestDevPolicyUsesIsolatedDataDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv(paths.DataDirEnv, root)
	h := NewHandler()
	controller := h.developmentPolicy()
	if got, want := controller.Store.Path, filepath.Join(root, "dev-policy.json"); got != want {
		t.Fatalf("policy path %q want %q", got, want)
	}
	r := dispatchJSON(t, h, "dev_policy", map[string]any{"op": "clear", "owner": "office"})
	if r.IsError {
		t.Fatal(resultText(t, &r))
	}
	if _, err := os.Stat(filepath.Join(root, "dev-policy.json")); err != nil {
		t.Fatal("isolated policy not persisted", err)
	}
}

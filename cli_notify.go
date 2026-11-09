// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/marcelocantos/spyder/internal/cliexit"
	"github.com/marcelocantos/spyder/internal/clitimeout"
	"github.com/marcelocantos/spyder/internal/notify"
)

func runNotify(args []string) {
	pf, ctx, cancel := setupCommand("notify", args, []string{"--webhook-url", "--platform", "--ttl", "--match", "--data", "--device"}, []string{"--json", "--recurring"}, clitimeout.DefaultRead)
	defer cancel()
	verb := ""
	a := map[string]any{}
	if len(pf.positional) > 0 {
		switch pf.positional[0] {
		case "connected":
			if len(pf.positional) == 2 && pf.flags["--webhook-url"] != "" {
				verb = "notify_when_connected"
				a["device"] = pf.positional[1]
			}
		case "subscribe":
			if len(pf.positional) == 2 && pf.flags["--webhook-url"] != "" {
				verb = "notification_subscribe"
				a["events"] = strings.Split(pf.positional[1], ",")
				a["recurring"] = pf.bools["--recurring"]
				match := map[string]any{}
				for _, s := range pf.repeats["--match"] {
					k, v, ok := strings.Cut(s, "=")
					if !ok || k == "" {
						fatalUsage("notify", fmt.Errorf("--match requires FIELD=VALUE"))
					}
					match[k] = v
				}
				a["match"] = match
			}
		case "events":
			if len(pf.positional) == 1 {
				verb = "notification_events"
			}
		case "publish":
			if len(pf.positional) == 2 {
				verb = "event_publish"
				a["event"] = pf.positional[1]
				data := map[string]any{}
				for _, s := range pf.repeats["--data"] {
					k, v, ok := strings.Cut(s, "=")
					if !ok || k == "" {
						fatalUsage("notify", fmt.Errorf("--data requires FIELD=VALUE"))
					}
					data[k] = v
				}
				a["data"] = data
				if d := pf.flags["--device"]; d != "" {
					a["device"] = d
				}
			}
		case "list":
			if len(pf.positional) == 1 {
				verb = "notifications"
			}
		case "cancel":
			if len(pf.positional) == 2 {
				verb = "notification_cancel"
				a["id"] = pf.positional[1]
			}
		}
	}
	if verb == "" {
		fatalUsage("notify", fmt.Errorf("expected subscribe EVENTS --webhook-url URL|-, connected DEVICE --webhook-url URL|-, events, publish custom.NAME, list, or cancel ID"))
	}
	if verb == "notify_when_connected" || verb == "notification_subscribe" {
		webhook := pf.flags["--webhook-url"]
		if webhook == "-" {
			b, err := io.ReadAll(io.LimitReader(os.Stdin, notify.MaxWebhookURLBytes+1))
			if err != nil {
				cliexit.Errorf(1, "read webhook URL: %v", err)
			}
			if len(b) > notify.MaxWebhookURLBytes {
				cliexit.Errorf(1, "webhook URL too long")
			}
			webhook = strings.TrimSpace(string(b))
		}
		a["webhook_url"] = webhook
		for _, key := range []string{"platform", "ttl"} {
			if v := pf.flags["--"+key]; v != "" {
				a[key] = v
			}
		}
	}
	dispatchAndExit(ctx, verb, a, true, false)
}

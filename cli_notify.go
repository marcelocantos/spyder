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
	pf, ctx, cancel := setupCommand("notify", args, []string{"--webhook-url", "--platform", "--ttl"}, []string{"--json"}, clitimeout.DefaultRead)
	defer cancel()
	verb := ""
	a := map[string]any{}
	if len(pf.positional) > 0 {
		switch pf.positional[0] {
		case "connected":
			if len(pf.positional) == 2 && pf.flags["--webhook-url"] != "" {
				verb = "notify_when_connected"
				a["device"] = pf.positional[1]
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
		fatalUsage("notify", fmt.Errorf("expected connected DEVICE --webhook-url URL|-, list, or cancel ID"))
	}
	dispatchAndExit(ctx, verb, a, true, false)
}

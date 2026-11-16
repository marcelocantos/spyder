// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/marcelocantos/spyder/internal/adbserver"
	"github.com/marcelocantos/spyder/internal/notify"
)

// An adb server restart drops every `adb reverse` mapping, so a running
// Android app loses its app channel. spyder remembers the mappings it
// made and puts them back when the server returns (🎯T194.5).

// reverseRestoreWait is how long a device has to reconnect after the
// server restarts before its mapping is given up.
const reverseRestoreWait = 30 * time.Second

type adbReverse struct {
	deviceID string
	port     int
}

func (h *Handler) noteReverse(deviceID string, port int, live bool) {
	h.reverseMu.Lock()
	defer h.reverseMu.Unlock()
	if h.reverses == nil {
		h.reverses = map[adbReverse]bool{}
	}
	if live {
		h.reverses[adbReverse{deviceID, port}] = true
	} else {
		delete(h.reverses, adbReverse{deviceID, port})
	}
}

// OnADBServerRestart restores spyder's reverse mappings and publishes
// adb.server_restarted. It returns at once; restoring runs in the
// background while devices reconnect.
func (h *Handler) OnADBServerRestart(ev adbserver.Event) {
	h.reverseMu.Lock()
	var pending []adbReverse
	for r := range h.reverses {
		pending = append(pending, r)
	}
	h.reverseMu.Unlock()
	go h.restoreReverses(ev, pending)
}

func (h *Handler) restoreReverses(ev adbserver.Event, pending []adbReverse) {
	var failed []string
	restored := 0
	deadline := time.Now().Add(reverseRestoreWait)
	for _, r := range pending {
		var err error
		for {
			if err = reverseAppChannel(r.deviceID, r.port); err == nil || time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Second)
		}
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s:%d: %v", r.deviceID, r.port, err))
			continue
		}
		restored++
	}
	data := map[string]string{
		"reverses": fmt.Sprintf("restored %d of %d", restored, len(pending)),
		"evidence": ev.Evidence,
	}
	if !ev.Up.IsZero() {
		data["down_ms"] = strconv.FormatInt(ev.Up.Sub(ev.Down).Milliseconds(), 10)
	}
	var errs []string
	if ev.Err != nil {
		errs = append(errs, ev.Err.Error())
	}
	errs = append(errs, failed...)
	if len(errs) > 0 {
		data["error"] = strings.Join(errs, "; ")
	}
	slog.Info("adb server restart handled", "reverses_restored", restored, "reverses", len(pending), "errors", errs)
	h.publishNotification(notify.Event{Event: "adb.server_restarted", Data: data})
}

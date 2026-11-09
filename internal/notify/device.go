// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package notify

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marcelocantos/spyder/internal/goios"
)

// WaitDevice uses provider event streams, never the slow device census. usbmux
// replays attached devices on subscribe; adb's wait also handles existing
// connections and does not admit offline/unauthorized Android devices. An iOS
// attach means connected, not unlocked or automation/tunnel ready.
func WaitDevice(ctx context.Context, platform, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch platform {
	case "android":
		if err := exec.CommandContext(ctx, "adb", "-s", id, "wait-for-device").Run(); err != nil {
			return fmt.Errorf("adb device wait: %w", err)
		}
		return nil
	case "ios":
		src, err := goios.NewUsbmuxEventSource()
		if err != nil {
			return err
		}
		defer src.Close()
		stop := context.AfterFunc(ctx, func() { _ = src.Close() })
		defer stop()
		for {
			event, err := src.Next()
			if err != nil {
				return err
			}
			if event.Attached && event.UDID == id {
				return nil
			}
		}
	default:
		return errors.New("unsupported device platform")
	}
}

// WatchDevices shares one native stream per platform for generic fleet events.
// Initial sightings are connected events; disconnection needs an observed
// presence. It cannot reconstruct hardware events while Spyder was stopped.
func WatchDevices(ctx context.Context, emit func(Event)) {
	var wg sync.WaitGroup
	for _, source := range []func(context.Context, func(Event)) error{watchIOS, watchAndroid} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				if err := source(ctx, emit); err != nil && ctx.Err() == nil {
					slog.Warn("notification device event source unavailable", "error", err)
				}
				if !pause(ctx, time.Second) {
					return
				}
			}
		}()
	}
	wg.Wait()
}

// RunDeviceEvents opens provider streams only while a generic subscription
// needs device events; other event families do not start adb or USB listeners.
func (m *Manager) RunDeviceEvents(ctx context.Context, emit func(Event)) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var cancel context.CancelFunc
	var done chan struct{}
	stop := func() {
		if cancel != nil {
			cancel()
			<-done
			cancel = nil
			done = nil
		}
	}
	defer stop()
	for ctx.Err() == nil {
		m.mu.Lock()
		needed := false
		for _, r := range m.records {
			if (r.State == "waiting" || r.State == "ready") && time.Now().Before(r.ExpiresAt) && slices.ContainsFunc(r.Events, func(p string) bool {
				return eventMatches(p, "device.connected") || eventMatches(p, "device.disconnected")
			}) {
				needed = true
				break
			}
		}
		m.mu.Unlock()
		if needed && cancel == nil {
			cancel, done = startDeviceWatcher(ctx, emit)
		} else if !needed {
			stop()
		}
		select {
		case <-ctx.Done():
			return
		case <-m.sourceWake:
		case <-tick.C:
		}
	}
}

// The caller owns cancellation and joins done before dropping the watcher.
func startDeviceWatcher(ctx context.Context, emit func(Event)) (context.CancelFunc, chan struct{}) {
	watchCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); WatchDevices(watchCtx, emit) }()
	return cancel, done
}

func watchIOS(ctx context.Context, emit func(Event)) error {
	src, err := goios.NewUsbmuxEventSource()
	if err != nil {
		return err
	}
	defer src.Close()
	stop := context.AfterFunc(ctx, func() { _ = src.Close() })
	defer stop()
	byID := map[int]string{}
	for {
		e, err := src.Next()
		if err != nil {
			return err
		}
		if e.Attached {
			present := false
			for _, id := range byID {
				if id == e.UDID {
					present = true
					break
				}
			}
			byID[e.DeviceID] = e.UDID
			if !present {
				emit(Event{Event: "device.connected", DeviceID: e.UDID, Platform: "ios"})
			}
		} else if id, ok := byID[e.DeviceID]; ok {
			delete(byID, e.DeviceID)
			present := false
			for _, other := range byID {
				if other == id {
					present = true
					break
				}
			}
			if !present {
				emit(Event{Event: "device.disconnected", DeviceID: id, Platform: "ios"})
			}
		}
	}
}

func watchAndroid(ctx context.Context, emit func(Event)) error {
	cmd := exec.CommandContext(ctx, "adb", "track-devices")
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	return readADBEvents(bufio.NewReader(pipe), emit)
}

// adb track-devices writes hexadecimal length-prefixed snapshots, not lines.
func readADBEvents(reader io.Reader, emit func(Event)) error {
	const lengthBytes = 4
	const lengthBits = 16
	previous := map[string]bool{}
	for {
		header := make([]byte, lengthBytes)
		if _, err := io.ReadFull(reader, header); err != nil {
			return err
		}
		n, err := strconv.ParseUint(string(header), lengthBits, lengthBits)
		if err != nil {
			return errors.New("invalid adb tracker frame")
		}
		body := make([]byte, int(n))
		if _, err := io.ReadFull(reader, body); err != nil {
			return err
		}
		current := map[string]bool{}
		for line := range strings.SplitSeq(string(body), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[1] == "device" {
				current[fields[0]] = true
			}
		}
		for id := range current {
			if !previous[id] {
				emit(Event{Event: "device.connected", DeviceID: id, Platform: "android"})
			}
		}
		for id := range previous {
			if !current[id] {
				emit(Event{Event: "device.disconnected", DeviceID: id, Platform: "android"})
			}
		}
		previous = current
	}
}

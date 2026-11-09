// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package notify

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

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

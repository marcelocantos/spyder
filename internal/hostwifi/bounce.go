// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package hostwifi

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

const bounceSettleDelay = 2 * time.Second

// Bounce turns the Wi-Fi hardware port off and back on via networksetup.
func Bounce(ctx context.Context, device string) error {
	if device == "" {
		st, err := ReadStatus()
		if err != nil {
			return err
		}
		device = st.Device
	}
	for _, power := range []string{"off", "on"} {
		cmd := exec.CommandContext(ctx, "networksetup", "-setairportpower", device, power)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("networksetup -setairportpower %s %s: %w: %s",
				device, power, err, string(out))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(bounceSettleDelay):
		}
	}
	return nil
}

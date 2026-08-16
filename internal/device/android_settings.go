// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// 🎯T130 — allowlisted Android system settings (refresh rate). Restore
// deletes the pin rather than leaving a value. Unknown keys never reach adb.

package device

import (
	"fmt"
	"os/exec"
	"strings"
)

// SetSystemSetting writes an allowlisted key (refresh_rate → peak+min).
func (a *AndroidAdapter) SetSystemSetting(id, key, value string) (SettingResult, error) {
	return a.applySystemSetting(id, key, value, false)
}

// RestoreSystemSetting deletes the allowlisted Android settings so the
// device returns to OEM default (not a leftover pin).
func (a *AndroidAdapter) RestoreSystemSetting(id, key string) (SettingResult, error) {
	return a.applySystemSetting(id, key, "", true)
}

// GetSystemSetting reads the current Android values for an allowlisted key.
func (a *AndroidAdapter) GetSystemSetting(id, key string) (SettingResult, error) {
	if id == "" {
		return SettingResult{}, fmt.Errorf("device identifier is empty")
	}
	names, err := SettingAndroidNames(key)
	if err != nil {
		return SettingResult{}, err
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return SettingResult{}, fmt.Errorf("adb not found in PATH: %w", err)
	}
	curr := map[string]string{}
	for _, n := range names {
		out, stderr, err := androidAdb("-s", id, "shell", "settings", "get", n.Namespace, n.Name)
		if err != nil {
			return SettingResult{}, settingsAdbErr(id, "get", err, stderr)
		}
		curr[n.Name] = strings.TrimSpace(string(out))
	}
	return SettingResult{
		Key:     key,
		Action:  "get",
		Names:   settingDisplayNames(names),
		Current: curr,
	}, nil
}

func (a *AndroidAdapter) applySystemSetting(id, key, value string, restore bool) (SettingResult, error) {
	if id == "" {
		return SettingResult{}, fmt.Errorf("device identifier is empty")
	}
	names, err := SettingAndroidNames(key)
	if err != nil {
		return SettingResult{}, err
	}
	if !restore && value == "" {
		return SettingResult{}, fmt.Errorf("value is required to set %s", key)
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return SettingResult{}, fmt.Errorf("adb not found in PATH: %w", err)
	}

	prev := map[string]string{}
	curr := map[string]string{}
	for _, n := range names {
		before, _, _ := androidAdb("-s", id, "shell", "settings", "get", n.Namespace, n.Name)
		prev[n.Name] = strings.TrimSpace(string(before))

		var stderr []byte
		if restore {
			_, stderr, err = androidAdb("-s", id, "shell", "settings", "delete", n.Namespace, n.Name)
		} else {
			_, stderr, err = androidAdb("-s", id, "shell", "settings", "put", n.Namespace, n.Name, value)
		}
		if err != nil {
			op := "put"
			if restore {
				op = "delete"
			}
			return SettingResult{}, settingsAdbErr(id, op, err, stderr)
		}

		after, _, _ := androidAdb("-s", id, "shell", "settings", "get", n.Namespace, n.Name)
		curr[n.Name] = strings.TrimSpace(string(after))
	}

	action := "set"
	if restore {
		action = "restore"
	}
	return SettingResult{
		Key:      key,
		Action:   action,
		Value:    value,
		Names:    settingDisplayNames(names),
		Previous: prev,
		Current:  curr,
	}, nil
}

func settingsAdbErr(id, op string, err error, stderr []byte) error {
	msg := string(stderr)
	if isAndroidDeviceNotConnected(msg) {
		return fmt.Errorf("device not connected: %s", id)
	}
	return fmt.Errorf("adb settings %s: %v\n%s", op, err, truncate(msg, 200))
}

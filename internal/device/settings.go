// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"fmt"
	"sort"
	"strings"
)

// Allowlisted device-setting keys. Arbitrary adb/lockdown keys are rejected.
const (
	SettingRefreshRate = "refresh_rate"
)

// androidSettingName is one Android `settings` namespace/name pair that
// an allowlisted key expands to. Refresh rate writes both peak and min
// so OEMs (Samsung) actually pin the panel.
type androidSettingName struct {
	Namespace string
	Name      string
}

var settingAllowlist = map[string][]androidSettingName{
	SettingRefreshRate: {
		{Namespace: "system", Name: "peak_refresh_rate"},
		{Namespace: "system", Name: "min_refresh_rate"},
	},
}

// AllowedSettingKeys returns the sorted allowlist.
func AllowedSettingKeys() []string {
	keys := make([]string, 0, len(settingAllowlist))
	for k := range settingAllowlist {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// SettingAndroidNames expands an allowlisted key. Unknown keys error
// without any platform I/O.
func SettingAndroidNames(key string) ([]androidSettingName, error) {
	names, ok := settingAllowlist[key]
	if !ok {
		return nil, UnknownSettingError(key)
	}
	return names, nil
}

// UnknownSettingError rejects a key that is not on the allowlist.
func UnknownSettingError(key string) error {
	return fmt.Errorf("unknown device setting %q — allowlist: %s (not arbitrary adb shell)",
		key, strings.Join(AllowedSettingKeys(), ", "))
}

// SettingResult is the structured set/restore/get outcome.
type SettingResult struct {
	Key      string            `json:"key"`
	Action   string            `json:"action"` // set | restore | get
	Value    string            `json:"value,omitempty"`
	Names    []string          `json:"android_names,omitempty"`
	Previous map[string]string `json:"previous,omitempty"`
	Current  map[string]string `json:"current,omitempty"`
}

func settingDisplayNames(names []androidSettingName) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = n.Namespace + "/" + n.Name
	}
	return out
}

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"fmt"
	"strings"
)

// Android power, lock and network state (🎯T194.1).

// androidPowerScript reads screen wakefulness, stay-awake, keyguard, the
// current user's lock, whether a credential exists, airplane mode, Wi-Fi
// and the default network, in one adb round trip. Plain grep (not
// -m/head): closing the pipe early makes dumpsys print "Broken pipe"
// errors.
const androidPowerScript = `dumpsys power | grep -E "mWakefulness=|mStayOn="; ` +
	`dumpsys window | grep -E "isKeyguardShowing="; ` +
	`dumpsys trust | grep "(current)"; ` +
	`dumpsys lock_settings | grep "CredentialType:"; ` +
	`echo "airplane_mode_on=$(settings get global airplane_mode_on)"; ` +
	`echo "wifi_on=$(settings get global wifi_on)"; ` +
	`dumpsys connectivity | grep -E "Active default network:|NetworkAgentInfo\\{"`

// androidChargeStatus maps BatteryManager.BATTERY_STATUS_* to our names.
var androidChargeStatus = map[int]string{
	2: ChargeCharging,
	3: ChargeDischarging,
	4: ChargeNotCharging,
	5: ChargeFull,
}

// androidPowerFromBattery reads source, status and temperature from the
// parsed `dumpsys battery` keys.
func androidPowerFromBattery(extra map[string]any) Power {
	var p Power
	p.Source = PowerSourceNone
	for _, src := range []struct{ key, name string }{
		{"AC powered", PowerSourceAC},
		{"USB powered", PowerSourceUSB},
		{"Wireless powered", PowerSourceWireless},
		{"Dock powered", PowerSourceDock},
	} {
		if extra[src.key] == true {
			p.Source = src.name
			break
		}
	}
	if _, ok := extra["AC powered"]; !ok {
		p.Source = "" // not a battery dump we understand
	}
	if n, ok := extra["status"].(int); ok {
		p.Status = androidChargeStatus[n]
	}
	if n, ok := extra["temperature"].(int); ok && n > 0 {
		c := float64(n) / 10 // tenths of a degree
		p.TemperatureC = &c
	}
	return p
}

// parseAndroidPowerLock fills wakefulness, stay-awake and lock fields from
// androidPowerScript's output.
func parseAndroidPowerLock(out string, p *Power) {
	credentialSeen := false
	activeNetwork := ""
	var agents []string
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "mWakefulness=") && p.Wakefulness == "":
			// Android 14+ adds a numeric per-display-group line after
			// the named one; keep the name.
			p.Wakefulness = strings.TrimPrefix(line, "mWakefulness=")
		case strings.HasPrefix(line, "mStayOn="):
			p.StayOn = boolPtr(strings.TrimPrefix(line, "mStayOn=") == "true")
		case strings.HasPrefix(line, "isKeyguardShowing="):
			p.KeyguardShowing = boolPtr(strings.TrimPrefix(line, "isKeyguardShowing=") == "true")
		case strings.Contains(line, "(current)") && strings.Contains(line, "deviceLocked="):
			_, rest, _ := strings.Cut(line, "deviceLocked=")
			v, _, _ := strings.Cut(rest, ",")
			p.DeviceLocked = boolPtr(strings.TrimSpace(v) == "1")
		case strings.HasPrefix(line, "CredentialType:") && !credentialSeen:
			// The first block is user 0, the lab's only user.
			credentialSeen = true
			v := strings.TrimSpace(strings.TrimPrefix(line, "CredentialType:"))
			p.CredentialSet = boolPtr(v != "" && v != "NONE")
		case strings.HasPrefix(line, "airplane_mode_on="):
			if v := strings.TrimPrefix(line, "airplane_mode_on="); v == "0" || v == "1" {
				p.AirplaneMode = boolPtr(v == "1")
			}
		case strings.HasPrefix(line, "wifi_on="):
			// 0 off; 1 on; 2 on while airplane mode is on; 3 off in
			// airplane mode but restores when it ends.
			if v := strings.TrimPrefix(line, "wifi_on="); len(v) == 1 && v >= "0" && v <= "3" {
				p.WifiOn = boolPtr(v == "1" || v == "2")
			}
		case strings.HasPrefix(line, "Active default network:"):
			activeNetwork = strings.TrimSpace(strings.TrimPrefix(line, "Active default network:"))
		case strings.HasPrefix(line, "NetworkAgentInfo{"):
			agents = append(agents, line)
		}
	}
	switch activeNetwork {
	case "":
	case "none":
		p.DefaultNetwork = NetworkNone
		p.NetworkValidated = boolPtr(false)
	default:
		p.DefaultNetwork = "unknown"
		for _, a := range agents {
			if !strings.HasPrefix(a, "NetworkAgentInfo{network{"+activeNetwork+"}") {
				continue
			}
			if _, rest, ok := strings.Cut(a, "ni{"); ok {
				if f := strings.Fields(rest); len(f) > 0 {
					p.DefaultNetwork = strings.ToLower(f[0])
				}
			}
			p.NetworkValidated = boolPtr(strings.Contains(a, "IS_VALIDATED"))
		}
	}
}

func boolPtr(b bool) *bool { return &b }

// ThirdPartyForeground returns the foreground package when it is a
// user-installed app, or "" when the foreground is the launcher, a
// system app or nothing (🎯T194.3 step-in).
func (a *AndroidAdapter) ThirdPartyForeground(id string) (string, error) {
	pkg, err := androidForegroundApp(id)
	if err != nil || pkg == "" {
		return "", err
	}
	out, stderr, err := androidAdbBG("-s", id, "shell", "pm", "list", "packages", "-3", pkg)
	if err != nil {
		return "", fmt.Errorf("pm list packages: %s", truncate(string(stderr), 120))
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.TrimSpace(line) == "package:"+pkg {
			return pkg, nil
		}
	}
	return "", nil
}

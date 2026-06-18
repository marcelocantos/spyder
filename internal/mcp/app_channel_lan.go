// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"fmt"
	"net"
	"strings"

	"github.com/marcelocantos/spyder/internal/device"
)

// pickAppChannelHost picks the host portion of SPYDER_APP_CHANNEL for
// the launched app to dial back to spyder. Choice depends on where
// the app runs:
//
//   - iOS simulator       → "127.0.0.1" (simulator shares the host loopback)
//   - Android emulator    → "10.0.2.2" (the emulator's alias for the host)
//   - Physical iOS/Android → first LAN IPv4 advertised by the host
//
// Returns an error when the device is physical and no LAN address is
// available — spyder can't tell the device where to dial.
func pickAppChannelHost(platform, deviceID string) (string, error) {
	switch strings.ToLower(platform) {
	case "ios":
		if device.IsSimulatorID(deviceID) {
			return "127.0.0.1", nil
		}
	case "android":
		if strings.HasPrefix(deviceID, "emulator-") {
			return "10.0.2.2", nil
		}
	}
	hosts, err := lanHosts()
	if err != nil {
		return "", fmt.Errorf("appchannel: enumerate LAN hosts: %w", err)
	}
	if len(hosts) == 0 {
		return "", fmt.Errorf("appchannel: no LAN IPv4 address available for physical device")
	}
	return hosts[0], nil
}

// appchannelLANHosts returns the host candidates an app should dial to
// reach this spyder.
func appchannelLANHosts() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("net.Interfaces: %w", err)
	}
	var out []string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			ip4 := ip.To4()
			if ip4 == nil {
				continue
			}
			out = append(out, ip4.String())
		}
	}
	return out, nil
}

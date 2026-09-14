// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package hostwifi

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"
)

const gatewayProbeTimeout = 1500 * time.Millisecond

// DefaultGateway returns the IPv4 default gateway from the routing table.
func DefaultGateway() (string, error) {
	out, err := exec.Command("route", "-n", "get", "default").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("route -n get default: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "gateway:") {
			gw := strings.TrimSpace(strings.TrimPrefix(line, "gateway:"))
			if gw != "" {
				return gw, nil
			}
		}
	}
	return "", fmt.Errorf("no default gateway in route output")
}

// GatewayReachable probes the default gateway with a short TCP dial. ICMP
// ping is avoided — many gateways drop ping but still forward traffic.
func GatewayReachable() bool {
	gw, err := DefaultGateway()
	if err != nil {
		return false
	}
	// Port 53 is commonly open on home routers; fall back through a small set.
	for _, port := range []string{"53", "80", "443"} {
		addr := net.JoinHostPort(gw, port)
		conn, err := net.DialTimeout("tcp", addr, gatewayProbeTimeout)
		if err == nil {
			_ = conn.Close()
			return true
		}
	}
	return false
}

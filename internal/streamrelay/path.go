// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package streamrelay

import (
	"net"
	"strings"
)

// PathClass classifies a peer address for stream observability (🎯T96).
// Lets operators see at a glance whether a hop is loopback, LAN, or public
// without comparing raw IPs by eye.
type PathClass string

const (
	PathLoopback PathClass = "loopback"
	PathLAN      PathClass = "lan"
	PathPublic   PathClass = "public"
	PathUnknown  PathClass = "unknown"
)

// ClassifyRemote maps an http.Request.RemoteAddr-style "host:port" (or bare
// host) to a PathClass. Non-IP hosts (e.g. unresolved names) are unknown.
func ClassifyRemote(remote string) PathClass {
	host := remote
	if h, _, err := net.SplitHostPort(remote); err == nil {
		host = h
	}
	// Strip IPv6 brackets if present without a port (rare).
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	if ip == nil {
		return PathUnknown
	}
	if ip.IsLoopback() {
		return PathLoopback
	}
	// Private RFC1918 / ULA, link-local, and unique-local — all "same site"
	// for our purposes (not hairpinned through the public internet).
	if ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return PathLAN
	}
	return PathPublic
}

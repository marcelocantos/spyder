// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package listenaddr persists the daemon's HTTP bind and classifies
// loopback-only addresses (🎯T103).
package listenaddr

import (
	"net"
	"os"
	"path/filepath"
	"strings"
)

// Resolve picks the serve bind. --addr (explicit) wins, then SPYDER_ADDR
// (env), then the last successful bind (persisted), then fallback
// (the loopback default).
func Resolve(explicit, env, persisted, fallback string) string {
	if explicit != "" {
		return explicit
	}
	if env != "" {
		return env
	}
	if persisted != "" {
		return persisted
	}
	return fallback
}

// Load returns the persisted bind, or "" if missing/unreadable.
func Load(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Save writes addr to path, creating parent directories as needed.
func Save(path, addr string) error {
	if path == "" || addr == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(addr+"\n"), 0o644)
}

// IsLoopback reports whether addr is loopback-only. Empty host in
// ":3030" / "0.0.0.0:3030" / "[::]:3030" is all-interfaces, not loopback.
func IsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// Bare ":3030" is valid for Listen but SplitHostPort wants a host.
		if strings.HasPrefix(addr, ":") {
			return false
		}
		host = addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

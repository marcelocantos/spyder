// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"log/slog"
	"os"
	"path/filepath"
	"runtime/pprof"
	"time"

	"github.com/marcelocantos/spyder/internal/paths"
	"github.com/marcelocantos/spyder/internal/wedge"
)

// persistSelfRestartEvidence writes a goroutine dump and wedge snapshot
// under ~/.spyder/ before a supervised self-restart (🎯T99.3).
func persistSelfRestartEvidence(reason string) {
	base := paths.Base()
	if err := os.MkdirAll(base, 0o755); err != nil {
		slog.Error("self-restart: mkdir ~/.spyder failed", "error", err)
	} else {
		name := filepath.Join(base, "goroutine-selfrestart-"+time.Now().UTC().Format("20060102-150405")+".txt")
		if f, err := os.Create(name); err != nil {
			slog.Error("self-restart: goroutine dump create failed", "error", err)
		} else {
			_ = pprof.Lookup("goroutine").WriteTo(f, 2)
			_ = f.Close()
			slog.Info("self-restart: wrote goroutine dump", "path", name, "reason", reason)
		}
	}
	// Wedge snapshot path used by the usbmux monitor; trigger records reason.
	wedge.Capture("", "self-restart:"+reason)
}

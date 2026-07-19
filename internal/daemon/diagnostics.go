// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/pprof"
	"syscall"
	"time"

	"github.com/marcelocantos/spyder/internal/paths"
)

// startDiagnostics installs SIGQUIT → goroutine dump under ~/.spyder/
// without killing the process (🎯T99.5).
func startDiagnostics() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGQUIT)
	go func() {
		for range ch {
			if err := writeGoroutineDump(); err != nil {
				slog.Error("diagnostics: goroutine dump failed", "error", err)
			} else {
				slog.Info("diagnostics: wrote goroutine dump under ~/.spyder/")
			}
		}
	}()
}

func writeGoroutineDump() error {
	base := paths.Base()
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	name := filepath.Join(base, "goroutine-"+time.Now().UTC().Format("20060102-150405")+".txt")
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return pprof.Lookup("goroutine").WriteTo(f, 2)
}

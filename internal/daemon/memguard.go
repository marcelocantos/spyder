// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/pprof"
	"time"

	"github.com/marcelocantos/spyder/internal/exitlog"
	"github.com/marcelocantos/spyder/internal/paths"
)

// memguardExit writes a goroutine dump and a heap profile under
// ~/.spyder/, records the exit, and exits nonzero so launchd restarts the
// daemon (🎯T195). The heap profile names the allocating call site.
func memguardExit(heap, limit uint64) {
	reason := fmt.Sprintf("memguard: heap %d MB over limit %d MB", heap>>20, limit>>20)
	base := paths.Base()
	stamp := time.Now().UTC().Format("20060102-150405")
	if err := os.MkdirAll(base, 0o755); err == nil {
		for name, profile := range map[string]string{
			"memguard-" + stamp + "-goroutine.txt": "goroutine",
			"memguard-" + stamp + "-heap.pprof":    "heap",
		} {
			path := filepath.Join(base, name)
			f, err := os.Create(path)
			if err != nil {
				slog.Error("memguard: evidence create failed", "path", path, "error", err)
				continue
			}
			debug := 0
			if profile == "goroutine" {
				debug = 2
			}
			_ = pprof.Lookup(profile).WriteTo(f, debug)
			_ = f.Close()
			slog.Error("memguard: wrote evidence", "path", path)
		}
	}
	exitlog.Record(1, reason)
	os.Exit(1)
}

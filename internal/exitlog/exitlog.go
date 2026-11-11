// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package exitlog keeps a small durable record of why the daemon exited.
//
// The supervised daemon's stdout log rotates at a few hours of traffic, so an
// overnight exit was unexplainable by morning. Every non-zero exit path in
// `spyder serve` appends one line here, and the Go runtime writes fatal panic
// traces here too. The file lives under ~/.spyder/ (exits.log) and is
// trimmed to its newest half once it passes maxBytes.
package exitlog

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/marcelocantos/spyder/internal/paths"
)

// maxBytes bounds exits.log. Each exit line is short, but a fatal panic
// trace can run to megabytes.
const maxBytes = 8 << 20

// Path returns the exit log path.
func Path() string { return filepath.Join(paths.Base(), "exits.log") }

// Record appends one timestamped line naming the exit status and reason.
// Errors are ignored: the process is about to exit.
func Record(status int, reason string) {
	line := fmt.Sprintf("%s pid=%d status=%d reason=%s\n",
		time.Now().Format(time.RFC3339), os.Getpid(), status, strings.ReplaceAll(reason, "\n", " "))
	f, err := open()
	if err != nil {
		return
	}
	_, _ = f.WriteString(line)
	_ = f.Close()
}

// InstallCrashOutput makes the runtime also write fatal panics and fatal
// errors to the exit log, after a "started" marker for this process.
func InstallCrashOutput(version string) error {
	trim()
	f, err := open()
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintf(f, "%s pid=%d started version=%s\n", time.Now().Format(time.RFC3339), os.Getpid(), version)
	return debug.SetCrashOutput(f, debug.CrashOptions{})
}

func open() (*os.File, error) {
	if err := os.MkdirAll(paths.Base(), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(Path(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

// trim keeps the newest half of the log once it exceeds maxBytes, cut at
// a line boundary.
func trim() {
	b, err := os.ReadFile(Path())
	if err != nil || len(b) <= maxBytes {
		return
	}
	keep := b[len(b)-maxBytes/2:]
	if i := strings.IndexByte(string(keep), '\n'); i >= 0 {
		keep = keep[i+1:]
	}
	tmp := Path() + ".tmp"
	if os.WriteFile(tmp, keep, 0o644) == nil {
		_ = os.Rename(tmp, Path())
	}
}

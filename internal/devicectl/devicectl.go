// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package devicectl wraps Apple's `xcrun devicectl` command line as typed
// Go functions. devicectl drives CoreDevice — the iOS-17+ device channel
// that runs over Apple's own RSD tunnels and bypasses usbmuxd entirely —
// so every operation here keeps working when usbmuxd is wedged (🎯T72).
//
// Each wrapper writes results to a temporary `--json-output` file (the only
// interface devicectl documents as stable across releases — stdout text is
// explicitly unstable) and parses the document into Go structs. Every call
// is bounded twice: devicectl's own `--timeout <s>` flag aborts the
// CoreDevice operation cleanly, and a context deadline a couple of seconds
// longer reaps the subprocess if devicectl itself misbehaves.
//
// The package deliberately imports nothing from go-ios: it is the
// usbmuxd-free half of the iOS adapter and must stay usable by callers
// (internal/wedge, internal/device) that cannot afford a usbmuxd dependency.
package devicectl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DefaultTimeoutSeconds bounds a single devicectl CoreDevice operation. It
// is passed through as devicectl's own `--timeout` and mirrored (plus a
// grace margin) onto the context deadline.
const DefaultTimeoutSeconds = 30

// timeoutGrace is added to the devicectl `--timeout` value to compute the
// context deadline, so devicectl gets the chance to abort itself cleanly
// before the Go side kills the subprocess.
const timeoutGrace = 2 * time.Second

// errTailLimit caps how much stderr/stdout a CommandError carries, so a
// chatty failure can't bloat logs or error chains.
const errTailLimit = 400

// ErrUnavailable is returned (wrapped) when devicectl itself can't be
// located — i.e. `xcrun` is not on PATH. Callers that have a usbmuxd-backed
// fallback (e.g. device enumeration) use errors.Is to detect this and fall
// back rather than surfacing a hard failure.
var ErrUnavailable = errors.New("devicectl unavailable (xcrun not found)")

// CommandError captures a failed devicectl invocation with the diagnostics
// a caller needs to understand it: the subcommand, the process exit code,
// and truncated tails of both streams. It unwraps to the underlying exec
// or context error so errors.Is(err, context.DeadlineExceeded) works.
type CommandError struct {
	Subcommand string   // e.g. "device info apps"
	Args       []string // full devicectl arg vector (after "xcrun devicectl")
	ExitCode   int      // process exit code; -1 if the process never ran or was killed
	Stderr     string   // truncated stderr tail
	Stdout     string   // truncated stdout tail
	Err        error    // underlying error (exec.ExitError, context deadline, …)
}

func (e *CommandError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "devicectl %s failed", e.Subcommand)
	if e.ExitCode >= 0 {
		fmt.Fprintf(&b, " (exit %d)", e.ExitCode)
	}
	if e.Err != nil {
		fmt.Fprintf(&b, ": %v", e.Err)
	}
	if e.Stderr != "" {
		fmt.Fprintf(&b, "; stderr: %s", e.Stderr)
	} else if e.Stdout != "" {
		fmt.Fprintf(&b, "; stdout: %s", e.Stdout)
	}
	return b.String()
}

func (e *CommandError) Unwrap() error { return e.Err }

// Client is the entry point for devicectl operations. The zero value is not
// usable; call New (or NewWithExec in tests).
type Client struct {
	timeoutSeconds int
	// exec runs `xcrun devicectl <args> --timeout <n> --json-output <tmp>`,
	// reads the temp file, and returns its contents. Overridable in tests.
	exec ExecFunc
}

// ExecFunc runs a devicectl subcommand and returns the JSON document it
// wrote to its --json-output file. subcommand is a human-readable label
// (e.g. "device info apps") used only for error messages; args is the
// devicectl arg vector excluding the auto-appended --timeout / --json-output.
// Tests pass an ExecFunc to NewWithExec to drive the wrappers (and any
// caller layered on top) without a real device.
type ExecFunc func(ctx context.Context, timeoutSeconds int, subcommand string, args []string) ([]byte, error)

// New returns a Client wired to the real `xcrun devicectl` with the default
// per-call timeout.
func New() *Client {
	return &Client{timeoutSeconds: DefaultTimeoutSeconds, exec: defaultExec}
}

// NewWithExec builds a Client backed by a custom ExecFunc. Intended for
// tests that drive the wrappers without invoking xcrun. A non-positive
// timeoutSeconds falls back to DefaultTimeoutSeconds.
func NewWithExec(timeoutSeconds int, exec ExecFunc) *Client {
	if timeoutSeconds <= 0 {
		timeoutSeconds = DefaultTimeoutSeconds
	}
	return &Client{timeoutSeconds: timeoutSeconds, exec: exec}
}

// run invokes the configured exec with this client's timeout.
func (c *Client) run(ctx context.Context, subcommand string, args []string) ([]byte, error) {
	return c.exec(ctx, c.timeoutSeconds, subcommand, args)
}

// defaultExec is the production execFunc: it shells out to xcrun devicectl,
// captures both streams, writes JSON to a temp file, and returns the file
// contents. Failures come back as *CommandError.
func defaultExec(ctx context.Context, timeoutSeconds int, subcommand string, args []string) ([]byte, error) {
	if _, err := exec.LookPath("xcrun"); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	tmp, err := os.MkdirTemp("", "spyder-devicectl-*")
	if err != nil {
		return nil, fmt.Errorf("devicectl %s: temp dir: %w", subcommand, err)
	}
	defer os.RemoveAll(tmp)
	jsonPath := filepath.Join(tmp, "out.json")

	// "xcrun devicectl <args> --timeout N --json-output <tmp>"
	full := append([]string{"devicectl"}, args...)
	full = append(full,
		"--timeout", fmt.Sprintf("%d", timeoutSeconds),
		"--json-output", jsonPath)

	deadline := time.Duration(timeoutSeconds)*time.Second + timeoutGrace
	cctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	started := time.Now()
	var outBuf, errBuf bytes.Buffer
	cmd := exec.CommandContext(cctx, "xcrun", full...)
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	elapsedMs := time.Since(started).Milliseconds()

	if runErr != nil {
		// Prefer the context error so callers can detect timeouts.
		underlying := runErr
		if cctx.Err() != nil {
			underlying = cctx.Err()
		}
		cmdErr := &CommandError{
			Subcommand: subcommand,
			Args:       full,
			ExitCode:   cmd.ProcessState.ExitCode(),
			Stderr:     truncate(errBuf.String(), errTailLimit),
			Stdout:     truncate(outBuf.String(), errTailLimit),
			Err:        underlying,
		}
		slog.Error("devicectl exec failed",
			"subcommand", subcommand, "args", full,
			"duration_ms", elapsedMs, "exit", cmdErr.ExitCode,
			"error", underlying.Error(),
			"stderr_tail", cmdErr.Stderr)
		return nil, cmdErr
	}

	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil, &CommandError{
			Subcommand: subcommand,
			Args:       full,
			ExitCode:   0,
			Stderr:     truncate(errBuf.String(), errTailLimit),
			Stdout:     truncate(outBuf.String(), errTailLimit),
			Err:        fmt.Errorf("read json output: %w", err),
		}
	}
	slog.Debug("devicectl exec ok",
		"subcommand", subcommand, "args", full,
		"duration_ms", elapsedMs, "json_bytes", len(data))
	return data, nil
}

// truncate trims s and clips it to n runes with an ellipsis if longer.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// fileURLToPath converts a devicectl `file://` URL (used for app bundle and
// process executable locations) into a host filesystem path. devicectl emits
// percent-encoded URLs; we decode them and strip a trailing slash. A value
// that doesn't parse as a URL is returned unchanged — callers treat the
// result as opaque text and the worst case is a failed substring match.
func fileURLToPath(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Path == "" {
		return raw
	}
	return strings.TrimRight(u.Path, "/")
}

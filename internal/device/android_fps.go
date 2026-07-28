// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// 🎯T111 — Android compositor-side frame stats via dumpsys gfxinfo.
// Pure parsers are unit-tested with fixture text; adb is invoked only
// from MeasureFrameStats.

package device

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// FrameStats is a windowed FPS sample derived from dumpsys gfxinfo.
type FrameStats struct {
	Package      string  `json:"package"`
	WindowSec    float64 `json:"window_sec"`
	TotalFrames  int     `json:"total_frames"`
	JankyFrames  int     `json:"janky_frames,omitempty"`
	JankyPercent float64 `json:"janky_percent,omitempty"`
	FPS          float64 `json:"fps"`
	Source       string  `json:"source"` // "gfxinfo"
	RawExcerpt   string  `json:"raw_excerpt,omitempty"`
}

// androidAdb runs `adb <args...>` and returns stdout/stderr. Tests replace it.
var androidAdb = func(args ...string) (stdout, stderr []byte, err error) {
	return runCapture("adb", args...)
}

// androidSleepUntil sleeps until d elapses or ctx is cancelled.
// Tests replace it to avoid real waits.
var androidSleepUntil = func(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

var (
	reTotalFrames = regexp.MustCompile(`(?m)^Total frames rendered:\s*(\d+)`)
	reJankyFrames = regexp.MustCompile(`(?m)^Janky frames:\s*(\d+)\s*\(([\d.]+)%\)`)
	// Older/alternate wording.
	reJankyAlt = regexp.MustCompile(`(?m)^Janky frames:\s*(\d+)`)
)

// ParseGfxInfo extracts frame totals from dumpsys gfxinfo output.
// FPS is not computed here — it needs the measurement window length.
func ParseGfxInfo(out string) (total, janky int, jankyPct float64, err error) {
	m := reTotalFrames.FindStringSubmatch(out)
	if m == nil {
		return 0, 0, 0, errors.New("gfxinfo: no 'Total frames rendered' line")
	}
	total, err = strconv.Atoi(m[1])
	if err != nil {
		return 0, 0, 0, fmt.Errorf("gfxinfo: total frames: %w", err)
	}
	if jm := reJankyFrames.FindStringSubmatch(out); jm != nil {
		janky, _ = strconv.Atoi(jm[1])
		jankyPct, _ = strconv.ParseFloat(jm[2], 64)
	} else if jm := reJankyAlt.FindStringSubmatch(out); jm != nil {
		janky, _ = strconv.Atoi(jm[1])
	}
	return total, janky, jankyPct, nil
}

// MeasureFrameStats resets gfxinfo for package, waits window, dumps again,
// and returns FPS = total_frames / window_sec (compositor/UI rendering stats).
//
// ctx cancels the wait window (and should match the MCP dispatch deadline
// for long window_sec values). packageName is the Android package (bundle id).
func (a *AndroidAdapter) MeasureFrameStats(ctx context.Context, id, packageName string, window time.Duration) (FrameStats, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if id == "" {
		return FrameStats{}, errors.New("device identifier is empty")
	}
	if packageName == "" {
		return FrameStats{}, errors.New("package is required")
	}
	if window <= 0 {
		return FrameStats{}, errors.New("window must be positive")
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return FrameStats{}, fmt.Errorf("adb not found in PATH: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return FrameStats{}, err
	}

	// Reset counters so the window is clean.
	if _, stderr, err := androidAdb("-s", id, "shell", "dumpsys", "gfxinfo", packageName, "reset"); err != nil {
		msg := string(stderr)
		if isAndroidDeviceNotConnected(msg) {
			return FrameStats{}, fmt.Errorf("device not connected: %s", id)
		}
		return FrameStats{}, fmt.Errorf("gfxinfo reset: %v\n%s", err, truncate(msg, 200))
	}

	if err := androidSleepUntil(ctx, window); err != nil {
		return FrameStats{}, fmt.Errorf("gfxinfo window wait: %w", err)
	}

	out, stderr, err := androidAdb("-s", id, "shell", "dumpsys", "gfxinfo", packageName)
	if err != nil {
		msg := string(stderr)
		if isAndroidDeviceNotConnected(msg) {
			return FrameStats{}, fmt.Errorf("device not connected: %s", id)
		}
		return FrameStats{}, fmt.Errorf("gfxinfo dump: %v\n%s", err, truncate(msg, 200))
	}
	text := string(out)
	total, janky, jankyPct, err := ParseGfxInfo(text)
	if err != nil {
		// Include a short excerpt so agents can diagnose package mismatch.
		excerpt := text
		if len(excerpt) > 400 {
			excerpt = excerpt[:400]
		}
		return FrameStats{}, fmt.Errorf("%w\nexcerpt:\n%s", err, excerpt)
	}
	sec := window.Seconds()
	fps := 0.0
	if sec > 0 {
		fps = float64(total) / sec
	}
	excerpt := firstGfxLines(text, 12)
	return FrameStats{
		Package:      packageName,
		WindowSec:    sec,
		TotalFrames:  total,
		JankyFrames:  janky,
		JankyPercent: jankyPct,
		FPS:          fps,
		Source:       "gfxinfo",
		RawExcerpt:   excerpt,
	}, nil
}

func firstGfxLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package ship

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Pasteboard is injectable for hermetic tests (🎯T133.3).
type Pasteboard interface {
	Get() (string, error)
	Set(string) error
}

// MacPasteboard uses pbpaste / pbcopy.
type MacPasteboard struct{}

func (MacPasteboard) Get() (string, error) {
	out, err := exec.Command("pbpaste").Output()
	if err != nil {
		return "", fmt.Errorf("pbpaste: %w", err)
	}
	return string(out), nil
}

func (MacPasteboard) Set(s string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(s)
	return cmd.Run()
}

// DefaultPasteboard is used by import when tests do not override.
var DefaultPasteboard Pasteboard = MacPasteboard{}

const clipPoll = time.Second / 3

// ErrNoPaste means the wait timed out with an empty clipboard.
var ErrNoPaste = fmt.Errorf("nothing was copied")

// AwaitPaste clears the clipboard, waits for the next non-empty copy,
// then clears again immediately so secrets do not linger.
func AwaitPaste(pb Pasteboard, timeout time.Duration) (string, error) {
	if err := pb.Set(""); err != nil {
		return "", fmt.Errorf("clearing clipboard: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(clipPoll)
		s, err := pb.Get()
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(s) == "" {
			continue
		}
		if err := pb.Set(""); err != nil {
			return "", fmt.Errorf("clearing clipboard: %w", err)
		}
		return s, nil
	}
	return "", ErrNoPaste
}

// AbsorbOnce detects a paste and merges it into the studio envelope.
// Clears nothing itself — callers use AwaitPaste or clear after --now.
func AbsorbOnce(studio, paste string) (Detected, error) {
	d, err := Detect(paste)
	if err != nil {
		return d, err
	}
	env, err := LoadStudio(studio)
	if err != nil {
		return d, err
	}
	env.MergeDetected(d)
	if err := SaveStudio(studio, env); err != nil {
		return d, err
	}
	return d, nil
}

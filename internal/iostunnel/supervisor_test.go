// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package iostunnel

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSupervisorRestartsUnexpectedExit(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	attemptsPath := filepath.Join(tmp, "attempts")
	scriptPath := filepath.Join(tmp, "fake-ios")
	script := `#!/bin/sh
attempts_file="$FAKE_IOS_ATTEMPTS"
attempts=0
if [ -f "$attempts_file" ]; then
  attempts=$(cat "$attempts_file")
fi
attempts=$((attempts + 1))
echo "$attempts" > "$attempts_file"
if [ "$attempts" -eq 1 ]; then
  exit 42
fi
trap 'exit 0' TERM
while true; do
  sleep 1
done
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ios: %v", err)
	}
	t.Setenv("FAKE_IOS_ATTEMPTS", attemptsPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := New(scriptPath)
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		cancel()
		_ = s.Stop(context.Background())
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		attemptsBytes, err := os.ReadFile(attemptsPath)
		if err == nil {
			attempts, err := strconv.Atoi(strings.TrimSpace(string(attemptsBytes)))
			if err != nil {
				t.Fatalf("parse attempts: %v", err)
			}
			if attempts >= 2 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("supervisor did not restart child; attempts file: %q", attemptsBytes)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

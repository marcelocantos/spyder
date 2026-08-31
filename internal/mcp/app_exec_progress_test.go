// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/health"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// 🎯T103.1: sleep ticks are script progress, so a wait longer than the
// watchdog timeout must not stall the daemon entity.
func TestExec_SleepReportsProgress(t *testing.T) {
	var n atomic.Int32
	lim := defaultLim()
	lim.MaxDuration = time.Second
	lim.ProgressTick = 20 * time.Millisecond
	lim.Progress = func() { n.Add(1) }

	res := runScript(t, "sleep(80)", stubVerbs(), lim)
	if res.IsError {
		t.Fatalf("sleep failed: %v", texts(res))
	}
	if got := n.Load(); got < 3 {
		t.Fatalf("sleep progress beats = %d want >= 3", got)
	}
}

func TestExec_VerbReportsProgress(t *testing.T) {
	var n atomic.Int32
	lim := defaultLim()
	lim.Progress = func() { n.Add(1) }

	res := runScript(t, "say_text()", stubVerbs(), lim)
	if res.IsError {
		t.Fatalf("verb failed: %v", texts(res))
	}
	if got := n.Load(); got < 2 {
		t.Fatalf("verb progress beats = %d want >= 2 (enter+exit)", got)
	}
}

func TestExec_HungVerbStallsWatchdog(t *testing.T) {
	m := health.New()
	w := health.NewProgressWatchdog(m, "spyder", 80*time.Millisecond)
	entered := make(chan struct{})
	release := make(chan struct{})
	verbs := map[string]toolFunc{
		"hang": func(map[string]any) (*mcpgo.CallToolResult, error) {
			close(entered)
			<-release
			return mcpgo.NewToolResultText("ok"), nil
		},
	}

	w.Begin()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = runExec(ctx, "hang()", verbs, m, nil, execLimits{
			MaxSteps:     defaultExecSteps,
			MaxDuration:  2 * time.Second,
			Progress:     w.Beat,
			ProgressTick: 20 * time.Millisecond,
		}, nil)
	}()

	<-entered
	deadline := time.Now().Add(time.Second)
	stalled := false
	for time.Now().Before(deadline) {
		if w.Check(time.Now()) {
			stalled = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	<-done
	if !stalled {
		t.Fatal("hung verb with no further progress should stall the watchdog")
	}
	if !spyderNeedsAttentionFrom(m) {
		t.Fatal("stall should drive entity spyder to needs_attention")
	}
}

func spyderNeedsAttentionFrom(m *health.Model) bool {
	for _, e := range m.Snapshot().Entities {
		if e.ID.Name == "spyder" && e.State == health.NeedsAttention {
			return true
		}
	}
	return false
}

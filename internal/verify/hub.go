// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Hub is the daemon-wide scheduler: one pool, one human_gate lock, many
// concurrent workflow graphs.
type Hub struct {
	mu     sync.Mutex
	pool   *resourcePool
	runs   map[string]*Run
	shell  StepRunner
	script StepRunner
	model  StepRunner
	sleep  func(time.Duration)
	now    func() time.Time

	liveMu sync.Mutex
	live   map[chan struct{}]struct{}
}

// HubArgs configures a Hub. Zero values pick production defaults.
type HubArgs struct {
	MaxWorkers int
	Shell      StepRunner
	Script     StepRunner
	Model      StepRunner
	Sleep      func(time.Duration)
	Now        func() time.Time
}

// NewHub returns a daemon-wide verification hub.
func NewHub(args HubArgs) *Hub {
	shell := args.Shell
	if shell == nil {
		shell = DefaultShell
	}
	sleep := args.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	now := args.Now
	if now == nil {
		now = time.Now
	}
	return &Hub{
		pool:   newPool(args.MaxWorkers),
		runs:   map[string]*Run{},
		shell:  shell,
		script: args.Script,
		model:  args.Model,
		sleep:  sleep,
		now:    now,
		live:   map[chan struct{}]struct{}{},
	}
}

// StartArgs is one workflow invocation.
type StartArgs struct {
	Workflow     *Workflow
	WorkflowPath string
	Cwd          string
	Params       map[string]string
	Answers      map[string]Answer
	AllowWaive   bool
}

// Answer is a human_gate response (CLI --answer or REST verify_answer).
type Answer struct {
	ChoiceID string `json:"choice_id"`
	Comment  string `json:"comment,omitempty"`
}

// Start validates, substitutes, and runs a workflow on the shared pool.
// The returned Run is already executing; Wait for the result.
func (h *Hub) Start(ctx context.Context, args StartArgs) (*Run, error) {
	if args.Workflow == nil {
		return nil, &WorkflowError{Errors: []string{"workflow is required"}}
	}
	params := MergeParams(args.Workflow, args.Params)
	if errs := CheckPlaceholders(args.Workflow, params); len(errs) > 0 {
		return nil, &WorkflowError{Errors: errs}
	}
	rendered := Substitute(args.Workflow, params)
	cwd := args.Cwd
	if cwd == "" {
		cwd, _ = filepath.Abs(".")
	}
	run := newRun(h, rendered, RunOpts{
		Path:       args.WorkflowPath,
		Cwd:        cwd,
		Params:     params,
		Answers:    args.Answers,
		AllowWaive: args.AllowWaive,
		Ctx:        ctx,
	})
	if err := run.persistDefinition(); err != nil {
		run.cancel()
		return nil, fmt.Errorf("save verify definition: %w", err)
	}
	h.mu.Lock()
	h.runs[run.ID] = run
	h.mu.Unlock()
	h.notify()
	go run.drive()
	return run, nil
}

// Answer delivers a gate response on the shipped path (REST/CLI).
func (h *Hub) Answer(runID, gateID string, ans Answer) error {
	h.mu.Lock()
	run := h.runs[runID]
	h.mu.Unlock()
	if run == nil {
		return fmt.Errorf("unknown run %s", runID)
	}
	return run.submitAnswer(gateID, ans)
}

// Abort requests a run stop.
func (h *Hub) Abort(runID, reason string) error {
	h.mu.Lock()
	run := h.runs[runID]
	h.mu.Unlock()
	if run == nil {
		return fmt.Errorf("unknown run %s", runID)
	}
	run.abort(reason)
	return nil
}

// Snapshot is the dashboard/REST view of active runs plus the single in-flight gate.
func (h *Hub) Snapshot() Snapshot {
	h.mu.Lock()
	runs := make([]*Run, 0, len(h.runs))
	for _, r := range h.runs {
		runs = append(runs, r)
	}
	h.mu.Unlock()

	out := Snapshot{Runs: make([]RunView, 0, len(runs))}
	for _, r := range runs {
		view := r.view()
		out.Runs = append(out.Runs, view)
		if view.Gate != nil && out.Gate == nil {
			g := *view.Gate
			out.Gate = &g
		}
	}
	sort.Slice(out.Runs, func(i, j int) bool {
		if (out.Runs[i].Status == "running") != (out.Runs[j].Status == "running") {
			return out.Runs[i].Status == "running"
		}
		return out.Runs[i].ReportDir < out.Runs[j].ReportDir
	})
	return out
}

// forgetRun removes a completed run from memory. The caller retains its Run
// pointer for Wait; the report, screenshots, and log remain on disk.
func (h *Hub) forgetRun(id string) {
	h.mu.Lock()
	delete(h.runs, id)
	h.mu.Unlock()
	h.notify()
}

// RunByID returns a live run or nil.
func (h *Hub) RunByID(id string) *Run {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.runs[id]
}

// subscribe is a coalesced wake channel for dashboard WebSocket clients.
// A buffered 1-slot send means a slow consumer gets the latest snapshot,
// not every intermediate log line.
func (h *Hub) subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.liveMu.Lock()
	h.live[ch] = struct{}{}
	h.liveMu.Unlock()
	return ch, func() {
		h.liveMu.Lock()
		delete(h.live, ch)
		h.liveMu.Unlock()
	}
}

func (h *Hub) notify() {
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	for ch := range h.live {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Snapshot is verify_status payload.
type Snapshot struct {
	Runs []RunView `json:"runs"`
	Gate *GateView `json:"gate,omitempty"`
}

func shortID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

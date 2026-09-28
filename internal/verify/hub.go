// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"maps"
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
	// stateDir holds the retained-run index and review database; empty
	// keeps both in memory only (tests).
	stateDir string
	reviews  *reviewStore

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
	// StateDir persists the list of retained runs so they survive a daemon
	// restart. Empty disables persistence.
	StateDir string
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
	h := &Hub{
		pool:     newPool(args.MaxWorkers),
		runs:     map[string]*Run{},
		shell:    shell,
		script:   args.Script,
		model:    args.Model,
		sleep:    sleep,
		now:      now,
		stateDir: args.StateDir,
		live:     map[chan struct{}]struct{}{},
	}
	reviews, err := openReviewStore(h.stateDir)
	if err != nil {
		slog.Warn("verify: review database unavailable; reviews kept in memory", "dir", h.stateDir, "error", err)
		if reviews, err = openReviewStore(""); err != nil {
			panic(fmt.Sprintf("verify: in-memory review database: %v", err))
		}
	}
	h.reviews = reviews
	if h.stateDir != "" {
		h.rehydrate()
	}
	return h
}

// StartArgs is one workflow invocation.
type StartArgs struct {
	Workflow        *Workflow
	WorkflowPath    string
	Cwd             string
	Params          map[string]string
	Answers         map[string]Answer
	AllowWaive      bool
	DeferHumanGates bool
	ReviewDeferred  bool
	// Owner is the creating agent; only it may dismiss the finished run.
	// Empty means filepath.Base(Cwd).
	Owner string
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
	if args.DeferHumanGates && args.ReviewDeferred {
		return nil, fmt.Errorf("defer_human_gates and review_deferred cannot be combined")
	}
	var prior *Record
	if args.ReviewDeferred {
		prior = LoadRecord(cwd, rendered.Name)
		if prior == nil || len(prior.Deferred) == 0 {
			return nil, fmt.Errorf("no deferred owner checks to review for %s", rendered.Name)
		}
		if prior.DefinitionSHA256 != fmt.Sprintf("%x", sha256.Sum256(rendered.Raw)) || !maps.Equal(prior.Params, params) {
			return nil, fmt.Errorf("prepared workflow or parameters changed; run full verification before owner review")
		}
	}
	run := newRun(h, rendered, RunOpts{
		Path:            args.WorkflowPath,
		Cwd:             cwd,
		Params:          params,
		Answers:         args.Answers,
		AllowWaive:      args.AllowWaive,
		DeferHumanGates: args.DeferHumanGates,
		ReviewDeferred:  args.ReviewDeferred,
		Prior:           prior,
		Owner:           args.Owner,
		Ctx:             ctx,
	})
	if err := run.persistDefinition(); err != nil {
		run.cancel()
		return nil, fmt.Errorf("save verify definition: %w", err)
	}
	h.mu.Lock()
	h.runs[run.ID] = run
	h.saveRetainedLocked()
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

// Snapshot is the dashboard/REST view of active and retained runs plus the
// single in-flight gate.
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
	sort.Slice(out.Runs, func(i, j int) bool { return runStartOrder(out.Runs[i], out.Runs[j]) })
	return out
}

// Detail returns one step's evidence, or the run's latest screenshot when
// stepID is empty.
func (h *Hub) Detail(runID, stepID string) (*StepDetail, error) {
	run := h.RunByID(runID)
	if run == nil {
		return nil, fmt.Errorf("unknown run %s", runID)
	}
	return run.Detail(stepID)
}

// RunByID returns an active or retained run, or nil.
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

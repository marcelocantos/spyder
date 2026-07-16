// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/marcelocantos/spyder/internal/cliexit"
	"github.com/marcelocantos/spyder/internal/clitimeout"
	"github.com/marcelocantos/spyder/internal/health"
	"github.com/marcelocantos/spyder/internal/rest"
)

// runStatus implements `spyder status [--json]`: an HTTP CLIENT of the
// daemon's GET /api/v1/health surface — the single source of truth for the
// live health model (🎯T90.3). REST is the source; the CLI is one of its
// readers (so REST≡CLI by construction), the health() app_exec builtin is
// the other in-process reader.
//
// Unlike the device-tool subcommands (POST /api/v1/<tool>), health is a
// GET-only pull, so this doesn't go through postTool. It deliberately does
// NOT auto-start the daemon: `status` answers "is the daemon and its stack
// healthy?", and spawning one to answer would be misleading.
func runStatus(args []string) {
	jsonOut := false
	for _, a := range args {
		switch a {
		case "--json":
			jsonOut = true
		case "--help", "-h":
			fmt.Print(`Usage: spyder status [--json]

Prints the daemon's live health model — one line per monitored entity
(daemon, subprocess, device) with its state and most recent evidence.

  --json   Emit the raw /api/v1/health JSON body.
`)
			return
		default:
			cliexit.Errorf(cliexit.ExitUsage, "status: unknown flag %q", a)
		}
	}

	ctx, cancel := clitimeout.Context(clitimeout.DefaultRead)
	defer cancel()

	body, err := getHealth(ctx)
	if err != nil {
		// Connection failure is the common case (daemon not running). Give a
		// clear, actionable message and a distinct exit code.
		cliexit.Errorf(cliexit.ExitDaemonUnreachable,
			"spyder status: %v — is `spyder serve` running?", err)
	}

	if jsonOut {
		os.Stdout.Write(body)
		if len(body) > 0 && body[len(body)-1] != '\n' {
			fmt.Println()
		}
		return
	}

	// Accept enriched HealthReport (entities + doctor_finding + in_flight)
	// while remaining backward-compatible with model-only snapshots.
	var rep struct {
		At       time.Time               `json:"at"`
		Entities []health.EntitySnapshot `json:"entities"`
		Doctor   *struct {
			Wedged    bool      `json:"wedged"`
			Detail    string    `json:"detail"`
			UpdatedAt time.Time `json:"updated_at"`
		} `json:"doctor_finding"`
		InFlight []struct {
			Tool      string    `json:"tool"`
			Device    string    `json:"device"`
			Started   time.Time `json:"started"`
			ElapsedMs int64     `json:"elapsed_ms"`
		} `json:"in_flight"`
	}
	if err := json.Unmarshal(body, &rep); err != nil {
		cliexit.Errorf(cliexit.ExitGeneric, "spyder status: parse health body: %v", err)
	}
	fmt.Print(formatStatus(health.Snapshot{At: rep.At, Entities: rep.Entities}))
	if rep.Doctor != nil && (rep.Doctor.Wedged || rep.Doctor.Detail != "") {
		fmt.Printf("doctor: wedged=%v detail=%q\n", rep.Doctor.Wedged, rep.Doctor.Detail)
	}
	if len(rep.InFlight) > 0 {
		fmt.Println("in_flight:")
		for _, op := range rep.InFlight {
			fmt.Printf("  %s device=%s elapsed_ms=%d\n", op.Tool, op.Device, op.ElapsedMs)
		}
	}
}

// getHealth GETs the daemon's health snapshot as raw JSON bytes. Returns a
// plain error (message already suited for the "is serve running?" prompt) on
// transport failure or a non-200 status.
func getHealth(ctx context.Context) ([]byte, error) {
	url := daemonBaseURL() + rest.HealthPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if isConnRefused(err) {
			return nil, fmt.Errorf("daemon not reachable at %s", daemonBaseURL())
		}
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("daemon returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// formatStatus renders a health snapshot as a human-readable report: one
// line per entity, sorted by (Kind, Name, Layer) — matching the snapshot's
// own ordering — with state, attempt count, last probe, and the most recent
// evidence detail. Extracted (pure function) so it can be unit-tested against
// injected entities without a live daemon.
func formatStatus(snap health.Snapshot) string {
	if len(snap.Entities) == 0 {
		return "no monitored entities\n"
	}
	// The REST snapshot is already sorted by (Kind, Name, Layer); re-sort
	// defensively so the formatter is order-independent of its input.
	ents := make([]health.EntitySnapshot, len(snap.Entities))
	copy(ents, snap.Entities)
	sort.Slice(ents, func(i, j int) bool {
		a, b := ents[i].ID, ents[j].ID
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Layer < b.Layer
	})

	var sb strings.Builder
	for _, e := range ents {
		label := string(e.Kind) + "/" + e.ID.Name
		if e.ID.Layer != "" {
			label += "/" + e.ID.Layer
		}
		fmt.Fprintf(&sb, "%-32s %-16s (attempts=%d, last_probe=%s)",
			label, e.State, e.Attempts, formatProbe(e.LastProbe))
		if detail := latestEvidence(e); detail != "" {
			fmt.Fprintf(&sb, "  %s", detail)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// formatProbe renders a last-probe timestamp, or "never" for the zero time
// (an entity registered but not yet probed).
func formatProbe(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Format(time.RFC3339)
}

// latestEvidence returns the detail of the most recent observation, or "".
func latestEvidence(e health.EntitySnapshot) string {
	if len(e.Evidence) == 0 {
		return ""
	}
	return e.Evidence[len(e.Evidence)-1].Detail
}

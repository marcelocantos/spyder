// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package adbserver keeps the host adb server running (🎯T194.5).
//
// The adb server is shared by every tool on the host. Unity's Android
// tooling kills it when the editor exits (a batch build ends with
// `adb kill-server`), which drops every device transport and every
// `adb reverse` mapping. The next adb client restarts the server, but
// the reverse mappings spyder made for app channels are gone.
//
// The supervisor holds a track-devices connection to the server, so it
// sees the server go away at once. It then starts the server again,
// records which processes were around (to name the killer), and calls
// OnRestart so spyder can restore its reverse mappings.
package adbserver

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// DefaultAddr is the adb server's default address.
const DefaultAddr = "127.0.0.1:5037"

const (
	dialTimeout   = 2 * time.Second
	startTimeout  = 20 * time.Second
	retryMin      = time.Second
	retryMax      = 30 * time.Second
	evidenceLines = 12
)

// Event describes one loss and restart of the server.
type Event struct {
	Down     time.Time
	Up       time.Time // zero when the restart failed
	Evidence string    // adb and Unity processes seen when it went away
	Err      error
}

// Args configures a Supervisor.
type Args struct {
	// Adb is the adb binary spyder starts the server with.
	Adb string
	// Addr defaults to DefaultAddr.
	Addr string
	// OnRestart runs after each restart, on the supervisor's goroutine.
	OnRestart func(Event)
	// Evidence lists processes when the server goes away; defaults to ps.
	Evidence func() string
}

// Supervisor watches and restarts the adb server.
type Supervisor struct {
	adb       string
	addr      string
	onRestart func(Event)
	evidence  func() string
}

// New returns a Supervisor.
func New(args Args) *Supervisor {
	s := &Supervisor{adb: args.Adb, addr: args.Addr, onRestart: args.OnRestart, evidence: args.Evidence}
	if s.addr == "" {
		s.addr = DefaultAddr
	}
	if s.evidence == nil {
		s.evidence = psEvidence
	}
	return s
}

// Run watches until ctx ends. A server that is down at startup is
// started without an event.
func (s *Supervisor) Run(ctx context.Context) {
	backoff := retryMin
	first := true
	for ctx.Err() == nil {
		conn, err := s.track(ctx)
		if err == nil {
			backoff = retryMin
			first = false
			// Blocks until the server goes away or ctx ends.
			drain(conn)
			_ = conn.Close()
			if ctx.Err() != nil {
				return
			}
			s.restart(ctx, time.Now(), s.evidence())
			continue
		}
		if first {
			// Down before we ever saw it: start it quietly.
			first = false
			if err := s.start(ctx); err != nil {
				slog.Warn("adb server: initial start failed", "error", err)
			}
			continue
		}
		slog.Warn("adb server: not reachable", "addr", s.addr, "error", err, "retry_in", backoff)
		if s.start(ctx) == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, retryMax)
	}
}

func (s *Supervisor) restart(ctx context.Context, down time.Time, evidence string) {
	ev := Event{Down: down, Evidence: evidence}
	if err := s.start(ctx); err != nil {
		ev.Err = err
	} else {
		ev.Up = time.Now()
	}
	slog.Warn("adb server: went away; restarted", "down_ms", ev.Up.Sub(ev.Down).Milliseconds(), "error", ev.Err, "evidence", evidence)
	if s.onRestart != nil {
		s.onRestart(ev)
	}
}

// start runs `adb start-server`, which is a no-op when another client
// already restarted it.
func (s *Supervisor) start(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, s.adb, "start-server").CombinedOutput()
	if err != nil {
		return fmt.Errorf("adb start-server: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// track opens host:track-devices. The server keeps the connection open
// and writes a frame on every change; it closes when the server exits.
func (s *Supervisor) track(ctx context.Context) (net.Conn, error) {
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return nil, err
	}
	const req = "host:track-devices"
	if _, err := fmt.Fprintf(conn, "%04x%s", len(req), req); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(dialTimeout))
	status := make([]byte, 4)
	if _, err := io.ReadFull(conn, status); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if string(status) != "OKAY" {
		_ = conn.Close()
		return nil, fmt.Errorf("track-devices: %q", status)
	}
	_ = conn.SetReadDeadline(time.Time{})
	// Close the connection when ctx ends so drain returns.
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	return conn, nil
}

func drain(conn net.Conn) {
	r := bufio.NewReader(conn)
	header := make([]byte, 4)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			return
		}
		n, err := strconv.ParseUint(string(header), 16, 16)
		if err != nil {
			return
		}
		if _, err := io.CopyN(io.Discard, r, int64(n)); err != nil {
			return
		}
	}
}

// psEvidence lists adb and Unity processes with their parents, so a log
// line can say who was around when the server went away.
func psEvidence() string {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,etime=,command=").Output()
	if err != nil {
		return ""
	}
	return pickEvidence(string(out))
}

func pickEvidence(ps string) string {
	type proc struct{ pid, ppid, line string }
	byPID := map[string]proc{}
	var procs []proc
	for line := range strings.SplitSeq(ps, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		p := proc{pid: f[0], ppid: f[1], line: strings.Join(f[2:], " ")}
		byPID[p.pid] = p
		procs = append(procs, p)
	}
	var lines []string
	for _, p := range procs {
		cmd := strings.ToLower(p.line)
		if !strings.Contains(cmd, "adb") && !strings.Contains(cmd, "/unity.app/") {
			continue
		}
		s := p.pid + " " + truncate(p.line, 160)
		if parent, ok := byPID[p.ppid]; ok {
			s += " <- " + truncate(parent.line, 80)
		}
		lines = append(lines, s)
		if len(lines) == evidenceLines {
			break
		}
	}
	return strings.Join(lines, "\n")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package daemon runs spyder as an HTTP-based MCP server. Clients (e.g.
// Claude Code) connect via the streamable HTTP transport at /mcp.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/marcelocantos/spyder/internal/autoawake"
	"github.com/marcelocantos/spyder/internal/baselines"
	"github.com/marcelocantos/spyder/internal/inventory"
	spydermcp "github.com/marcelocantos/spyder/internal/mcp"
	"github.com/marcelocantos/spyder/internal/paths"
	"github.com/marcelocantos/spyder/internal/reservations"
	"github.com/marcelocantos/spyder/internal/rest"
	"github.com/marcelocantos/spyder/internal/runs"
	"github.com/marcelocantos/spyder/internal/tunneld"
)

// Run-artefact retention defaults. Overridable via env so the Homebrew
// service plist can tune without rebuilding.
const (
	defaultRunsMaxAgeDays = 30
	defaultRunsMaxSizeGB  = 20
)

// Config configures a spyder server instance.
type Config struct {
	Addr             string // HTTP listen address (e.g. ":3030"). ":0" picks a free port.
	Version          string // emitted in serverInfo
	TunneldAddr      string // tunneld probe target (host:port; empty → DefaultAddr)
	DisableAutoAwake bool   // tests set this to avoid the supervisor poking live tunneld
}

// Start creates the MCP server, registers all spyder tools, wraps it in
// a streamable-HTTP transport, probes tunneld for observability, and
// blocks serving on cfg.Addr until a SIGINT/SIGTERM arrives.
func Start(cfg Config) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	return Run(ctx, cfg)
}

// Run is Start's cancellable sibling: it returns when ctx is cancelled
// (or the underlying HTTP server errors). Exposed for tests and for
// embedders that want to own signal handling.
func Run(ctx context.Context, cfg Config) error {
	handler, tunClient, resvStore := Build(cfg)

	if !cfg.DisableAutoAwake {
		awakeOpts := []autoawake.Option{}
		if resvStore != nil {
			awakeOpts = append(awakeOpts, autoawake.WithReservations(resvStore))
		}
		go autoawake.New(tunClient, awakeOpts...).Run(ctx)
	}

	slog.Info("spyder listening",
		"addr", cfg.Addr, "mcp", "/mcp", "rest", rest.Prefix)

	srv := &http.Server{Addr: cfg.Addr, Handler: handler}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	}
}

// Build wires up the MCP server and the REST handler on a shared
// http.ServeMux without starting a listener or the autoawake
// supervisor. Tests (and embedders) can compose this with their own
// transport. The returned mux serves `/mcp` (mcp-go streamable HTTP)
// and `/api/v1/<tool>` (JSON over POST — see package rest). The
// reservation store may be nil if `~/.spyder/reservations.json` is
// unreadable; callers that need strict enforcement should check.
func Build(cfg Config) (http.Handler, *tunneld.Client, *reservations.Store) {
	tunneldAddr := cfg.TunneldAddr
	if tunneldAddr == "" {
		tunneldAddr = tunneld.DefaultAddr
	}
	tunClient := tunneld.New(tunneldAddr)
	if udids, err := tunClient.Probe(); err != nil {
		slog.Warn("tunneld unavailable — iOS DVT tools will fail",
			"addr", tunneldAddr, "error", err)
	} else {
		slog.Info("tunneld reachable", "addr", tunneldAddr, "paired_devices", len(udids))
	}

	srv := server.NewMCPServer(
		"spyder",
		cfg.Version,
		server.WithToolCapabilities(true),
	)

	invStore := inventory.New()
	resvPath := filepath.Join(paths.Base(), "reservations.json")
	resvStore, err := reservations.New(
		resvPath,
		reservations.WithNormalizer(func(ref string) string {
			if entry, ok := invStore.Lookup(ref); ok && entry.Alias != "" {
				return entry.Alias
			}
			return ref
		}),
	)
	if err != nil {
		slog.Warn("reservations unavailable; strict mode disabled",
			"path", resvPath, "error", err)
		resvStore = nil
	}

	runsStore, err := runs.New(paths.RunsBase(),
		runs.WithPolicy(runsPolicyFromEnv()))
	if err != nil {
		slog.Warn("runs store unavailable — artefact archiving disabled",
			"path", paths.RunsBase(), "error", err)
		runsStore = nil
	} else if res, perr := runsStore.Prune(); perr != nil {
		slog.Warn("runs prune failed", "error", perr)
	} else if len(res.Removed) > 0 {
		slog.Info("runs pruned on startup",
			"removed", len(res.Removed), "retained", res.Retained)
	}

	blsStore, err := baselines.New(paths.BaselinesBase())
	if err != nil {
		slog.Warn("baselines store unavailable — visual-regression tools disabled",
			"path", paths.BaselinesBase(), "error", err)
		blsStore = nil
	}

	handlerOpts := []spydermcp.HandlerOption{
		spydermcp.WithInventory(invStore),
	}
	if resvStore != nil {
		handlerOpts = append(handlerOpts, spydermcp.WithReservations(resvStore))
	}
	if runsStore != nil {
		handlerOpts = append(handlerOpts, spydermcp.WithRuns(runsStore))
	}
	if blsStore != nil {
		handlerOpts = append(handlerOpts, spydermcp.WithBaselines(blsStore))
	}
	handler := spydermcp.NewHandler(tunClient, handlerOpts...)

	for _, tool := range spydermcp.Definitions() {
		toolName := tool.Name
		srv.AddTool(tool, func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handler.Dispatch(toolName, req.GetArguments())
		})
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", server.NewStreamableHTTPServer(srv))
	mux.Handle(rest.Prefix, rest.NewHandler(handler))
	return mux, tunClient, resvStore
}

// runsPolicyFromEnv reads the retention overrides from environment
// variables. Zero either knob disables that bound; negative values
// are treated as zero.
func runsPolicyFromEnv() runs.Policy {
	days := envInt("SPYDER_RUNS_MAX_AGE_DAYS", defaultRunsMaxAgeDays)
	gb := envInt("SPYDER_RUNS_MAX_SIZE_GB", defaultRunsMaxSizeGB)
	p := runs.Policy{}
	if days > 0 {
		p.MaxAge = time.Duration(days) * 24 * time.Hour
	}
	if gb > 0 {
		p.MaxSize = int64(gb) * 1024 * 1024 * 1024
	}
	return p
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		slog.Warn("ignoring non-integer env override",
			"key", key, "value", v, "using", fallback)
		return fallback
	}
	return n
}

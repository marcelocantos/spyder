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
	"github.com/marcelocantos/spyder/internal/pmd3bridge"
	"github.com/marcelocantos/spyder/internal/pool"
	"github.com/marcelocantos/spyder/internal/poolstore"
	"github.com/marcelocantos/spyder/internal/reservations"
	"github.com/marcelocantos/spyder/internal/rest"
	"github.com/marcelocantos/spyder/internal/runs"
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
	TunneldAddr      string // Deprecated: tunneld is no longer used; field retained for backward compat.
	DisableAutoAwake bool   // tests set this to avoid the supervisor starting
}

// Start creates the MCP server, registers all spyder tools, wraps it in
// a streamable-HTTP transport, and blocks serving on cfg.Addr until a
// SIGINT/SIGTERM arrives.
func Start(cfg Config) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	return Run(ctx, cfg)
}

// Run is Start's cancellable sibling: it returns when ctx is cancelled
// (or the underlying HTTP server errors). Exposed for tests and for
// embedders that want to own signal handling.
func Run(ctx context.Context, cfg Config) error {
	slog.Info("daemon: starting",
		"addr", cfg.Addr, "version", cfg.Version,
		"disable_autoawake", cfg.DisableAutoAwake)
	handler, resvStore, bridgeSup, mcpHandler := Build(cfg)

	// bridgeBaseURL / bridgeToken are populated after a successful Start.
	// autoawake and the liveness probe each construct their own client from
	// these values — one-per-goroutine is simpler than sharing a Client.
	var bridgeBaseURL, bridgeToken string

	if bridgeSup != nil {
		// Bridge binary was resolved, so startup failure is a bug
		// (missing Python deps, broken install, etc.), not a config state.
		// Surface it by returning — the caller will treat this as a daemon
		// startup error. The whole-process panic-on-unresponsiveness model
		// only kicks in once the bridge is up.
		if err := bridgeSup.Start(ctx); err != nil {
			return fmt.Errorf("pmd3-bridge startup: %w", err)
		}
		bridgeBaseURL = bridgeSup.BaseURL()
		bridgeToken = bridgeSup.Token()
		// Liveness probe: periodic ListDevices from the daemon, so a wedged
		// Uvicorn (alive process, dead listener) panics via the client's
		// fatal hook rather than producing silent non-functionality.
		probeClient := pmd3bridge.NewClient(bridgeBaseURL, bridgeToken)
		go pmd3bridge.LivenessProbe(ctx, probeClient)
	}

	if !cfg.DisableAutoAwake {
		awakeOpts := []autoawake.Option{}
		if resvStore != nil {
			awakeOpts = append(awakeOpts, autoawake.WithReservations(resvStore))
		}
		var awakeBridge *pmd3bridge.Client
		if bridgeSup != nil {
			awakeBridge = pmd3bridge.NewClient(bridgeBaseURL, bridgeToken)
		}
		sv := autoawake.New(awakeBridge, awakeOpts...)
		// Plumb the supervisor back to the MCP handler so launch_app /
		// deploy_app can flag spyder-initiated foreground-launches and
		// suppress the opt-out edge that would otherwise misfire when
		// the launch backgrounds KeepAwake (🎯T48).
		mcpHandler.SetAutoawakeNotifier(sv)
		go sv.Run(ctx)
	}

	slog.Info("spyder listening",
		"addr", cfg.Addr, "mcp", "/mcp", "rest", rest.Prefix)

	srv := &http.Server{Addr: cfg.Addr, Handler: handler}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case <-ctx.Done():
		slog.Info("daemon: shutting down (signal or context cancel)")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if bridgeSup != nil {
			if err := bridgeSup.Stop(shutdownCtx); err != nil {
				slog.Warn("daemon: pmd3-bridge stop error", "error", err)
			}
		}
		slog.Info("daemon: draining http server")
		_ = srv.Shutdown(shutdownCtx)
		slog.Info("daemon: shutdown complete")
		return nil
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("daemon: http server errored", "error", err)
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
// The returned Supervisor is non-nil when the bridge binary was found and
// should be started by the caller; it is nil when the bridge is unavailable
// (graceful degradation).
func Build(cfg Config) (http.Handler, *reservations.Store, *pmd3bridge.Supervisor, *spydermcp.Handler) {
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

	// Build the sim/emu pool if pool.yaml exists. Missing file is not an error
	// — the pool tools return a clear "not configured" message instead.
	var poolInst *pool.Pool
	poolCfgPath := paths.PoolConfigPath()
	if poolCfg, poolErr := pool.LoadConfig(poolCfgPath); poolErr == nil {
		// Open the SQLite hold ledger. Failure to open is logged but
		// non-fatal — the pool degrades to in-memory holds (which means
		// reservations are lost across restarts, just like before this
		// landed).
		var poolStore *poolstore.Store
		if s, err := poolstore.Open(filepath.Join(paths.Base(), "pool.db")); err != nil {
			slog.Warn("pool ledger unavailable — reservations will not survive restart",
				"path", filepath.Join(paths.Base(), "pool.db"), "error", err)
		} else {
			poolStore = s
		}
		opts := []pool.Option{}
		if poolStore != nil {
			opts = append(opts, pool.WithStore(poolStore))
		}
		poolInst = pool.New(poolCfg, pool.RealExecutor{}, opts...)
		slog.Info("pool configured", "templates", len(poolCfg.Templates), "path", poolCfgPath)
	} else if !errors.Is(poolErr, os.ErrNotExist) {
		slog.Warn("pool config invalid — pool disabled",
			"path", poolCfgPath, "error", poolErr)
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
	if poolInst != nil {
		handlerOpts = append(handlerOpts, spydermcp.WithPoolManager(poolInst))
	}

	// Resolve the pmd3-bridge binary and construct a Supervisor + Client if
	// the binary is available. Missing binary is not fatal — bridge tools fall
	// back to the existing shell-out paths.
	var bridgeSup *pmd3bridge.Supervisor
	if binPath := resolveBridgeBinary(); binPath != "" {
		bridgeSup = pmd3bridge.NewSupervisor(binPath)
		// The Client reads the bridge's base URL and token from the
		// supervisor on every request, so it works whether Build or Run
		// is who eventually calls Start.
		handlerOpts = append(handlerOpts, spydermcp.WithPMD3Bridge(bridgeSup.Client()))
		slog.Info("pmd3-bridge configured", "binary", binPath)
	}

	handler := spydermcp.NewHandler(handlerOpts...)

	// Kick off pool adoption in the background so startup latency stays
	// low. Adoption rebuilds inventory from live simctl/avdmanager state
	// plus the persisted hold ledger. The pool itself is purely
	// demand-driven — sims are only created in response to Acquire, so
	// there's no Reconcile/pre-mint step at startup.
	if poolInst != nil {
		go func() {
			if err := poolInst.Adopt(context.Background()); err != nil {
				slog.Warn("pool: adopt failed; proceeding with empty inventory", "error", err)
			}
			slog.Info("pool: adoption complete")
		}()
	}

	for _, tool := range spydermcp.Definitions() {
		toolName := tool.Name
		srv.AddTool(tool, func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handler.Dispatch(toolName, req.GetArguments())
		})
	}

	mux := http.NewServeMux()
	// 30s heartbeat keeps idle Claude Code sessions' SSE GET streams from
	// being collapsed by OS/NAT after ~3 minutes of silence (🎯T40).
	// Without this, the next tool call after an idle period surfaces as
	// "MCP error -32000: Connection closed" before transparent reconnect
	// succeeds. mark3labs/mcp-go ships heartbeat disabled by default.
	mux.Handle("/mcp", server.NewStreamableHTTPServer(srv,
		server.WithHeartbeatInterval(30*time.Second)))
	mux.Handle(rest.Prefix, rest.NewHandler(handler))
	return mux, resvStore, bridgeSup, handler
}

// resolveBridgeBinary returns the path to the pmd3-bridge binary.
// Resolution order:
//  1. SPYDER_PMD3_BRIDGE environment variable.
//  2. Relative to the running executable: ../libexec/pmd3-bridge/pmd3-bridge
//     (production install layout via Homebrew). Symlinks are resolved
//     before computing the relative path so a Homebrew-style symlink
//     `/opt/homebrew/bin/spyder → /opt/homebrew/Cellar/spyder/<v>/bin/spyder`
//     points at the Cellar's libexec, not the empty `/opt/homebrew/libexec`
//     (🎯T35).
//  3. bridge/dist/pmd3-bridge/pmd3-bridge relative to the repo root
//     (development fallback — best-effort).
//
// Returns "" when no candidate exists; the caller should log a warning and
// skip the bridge rather than fail hard.
func resolveBridgeBinary() string {
	// 1. Explicit override.
	if env := os.Getenv("SPYDER_PMD3_BRIDGE"); env != "" {
		if _, err := os.Stat(env); err == nil {
			return env
		}
		slog.Warn("SPYDER_PMD3_BRIDGE set but binary not found",
			"path", env, "bridge", "disabled")
		return ""
	}

	// 2. Production layout: <real-exe-dir>/../libexec/pmd3-bridge/pmd3-bridge.
	// EvalSymlinks resolves the Homebrew bin/ symlink to the Cellar path
	// where the libexec sibling actually lives.
	if exe, err := exePathReal(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "..", "libexec", "pmd3-bridge", "pmd3-bridge")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	// 3. Development fallback: walk up from the executable looking for
	// bridge/dist/pmd3-bridge/pmd3-bridge. This handles `go run .` and
	// `bin/spyder` from the repo root. The dev fallback intentionally
	// uses os.Executable directly (no symlink eval) since the dev tree
	// layout is not symlinked.
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for range 5 {
			candidate := filepath.Join(dir, "bridge", "dist", "pmd3-bridge", "pmd3-bridge")
			if _, err := os.Stat(candidate); err == nil {
				slog.Info("pmd3-bridge: using development build", "path", candidate)
				return candidate
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	slog.Warn("pmd3-bridge binary not found — bridge tools disabled; " +
		"set SPYDER_PMD3_BRIDGE or install via Homebrew")
	return ""
}

// exePathReal returns the path to the running executable with all
// symlinks resolved. Homebrew installs each formula in a versioned
// Cellar directory and links the binary into a flat bin/ — `os.Executable`
// returns the symlink path, but the libexec sibling lives next to the
// real binary inside the Cellar. Without resolving the symlink, every
// Homebrew-installed spyder fails to find the bundled bridge (🎯T35).
func exePathReal() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		// Fall back to the unresolved path; better to attempt resolution
		// against the symlink than to fail outright.
		return exe, nil
	}
	return resolved, nil
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

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

	"github.com/marcelocantos/spyder/internal/appchannel"
	"github.com/marcelocantos/spyder/internal/baselines"
	"github.com/marcelocantos/spyder/internal/dashboard"
	"github.com/marcelocantos/spyder/internal/goios"
	"github.com/marcelocantos/spyder/internal/inventory"
	"github.com/marcelocantos/spyder/internal/iostunnel"
	"github.com/marcelocantos/spyder/internal/logcapture"
	spydermcp "github.com/marcelocantos/spyder/internal/mcp"
	"github.com/marcelocantos/spyder/internal/paths"
	"github.com/marcelocantos/spyder/internal/pool"
	"github.com/marcelocantos/spyder/internal/poolstore"
	"github.com/marcelocantos/spyder/internal/reservations"
	"github.com/marcelocantos/spyder/internal/rest"
	"github.com/marcelocantos/spyder/internal/runs"
	"github.com/marcelocantos/spyder/internal/streamrelay"
	"github.com/marcelocantos/spyder/internal/wedge"
)

// Run-artefact retention defaults. Overridable via env so the Homebrew
// service plist can tune without rebuilding.
const (
	defaultRunsMaxAgeDays = 30
	defaultRunsMaxSizeGB  = 20
)

// Config configures a spyder server instance.
type Config struct {
	Addr        string // HTTP listen address (e.g. ":3030"). ":0" picks a free port.
	Version     string // emitted in serverInfo
	TunneldAddr string // Deprecated: tunneld is no longer used; field retained for backward compat.
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
		"addr", cfg.Addr, "version", cfg.Version)
	httpHandler, _, mcpHandler, logCapMgr, appChanMgr := Build(cfg)

	// Bundled go-ios tunnel daemon. Spawned as a child process so its
	// lifecycle is tied to spyder's — stop on shutdown. Missing binary
	// is non-fatal — degraded mode where iOS DTX-dependent tools fail
	// per-call but the daemon stays up.
	var tunnelSup *iostunnel.Supervisor
	if binPath := resolveIOSTunnelBinary(); binPath != "" {
		tunnelSup = iostunnel.New(binPath, goios.DefaultTunnelHost, goios.DefaultTunnelPort)
		if err := tunnelSup.Start(ctx); err != nil {
			slog.Error("iostunnel: start failed; iOS tools degraded", "error", err)
			tunnelSup = nil
		}
	}

	// Keep the RemoteXPC tunnel registry fresh across device
	// re-enumeration: subscribe to usbmux attach/detach and drop /
	// re-establish tunnels proactively (🎯T89.2). Only meaningful when
	// the tunnel daemon is up — its registry is what the listener
	// grooms. Tied to ctx; exits on shutdown.
	if tunnelSup != nil && mcpHandler != nil {
		go mcpHandler.StartDeviceListeners(ctx)
	}

	// Self-monitoring: attach the attention notifier to the live health
	// model and start the background probes that populate it (🎯T90).
	if mcpHandler != nil {
		startHealthWiring(ctx, mcpHandler.Health(), tunnelSup != nil)
	}

	// Wedge monitor. Detects the usbmuxd third-party-table desync
	// (🎯T68) via a 30s polling timer + an opportunistic log-stream
	// tail of `log stream --process usbmuxd`. On detection, fires a
	// snapshot and attempts `sudo spyder-killusbmuxd`. Cleanly tied
	// to ctx — exits on daemon shutdown.
	go wedge.RunMonitor(ctx)

	slog.Info("spyder listening",
		"addr", cfg.Addr, "mcp", "/mcp", "rest", rest.Prefix)

	srv := &http.Server{Addr: cfg.Addr, Handler: httpHandler}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case <-ctx.Done():
		slog.Info("daemon: shutting down (signal or context cancel)")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if tunnelSup != nil {
			if err := tunnelSup.Stop(shutdownCtx); err != nil {
				slog.Error("daemon: iostunnel stop error", "error", err)
			}
		}
		if logCapMgr != nil {
			logCapMgr.Close()
		}
		if appChanMgr != nil {
			appChanMgr.Close()
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
func Build(cfg Config) (http.Handler, *reservations.Store, *spydermcp.Handler, *logcapture.Manager, *appchannel.Manager) {
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
		slog.Error("runs prune failed", "error", perr)
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
		opts := []pool.Option{pool.WithConfigPath(poolCfgPath)}
		if poolStore != nil {
			opts = append(opts, pool.WithStore(poolStore))
		}
		poolInst = pool.New(poolCfg, pool.RealExecutor{}, opts...)
		slog.Info("pool configured", "templates", len(poolCfg.Templates), "path", poolCfgPath)
	} else if !errors.Is(poolErr, os.ErrNotExist) {
		slog.Warn("pool config invalid — pool disabled",
			"path", poolCfgPath, "error", poolErr)
	}

	logCapMgr := logcapture.NewManager()
	appChanMgr := appchannel.NewManager()
	appchannel.SetSpyderVersion(cfg.Version)

	handlerOpts := []spydermcp.HandlerOption{
		spydermcp.WithInventory(invStore),
		spydermcp.WithLogCapture(logCapMgr),
		spydermcp.WithAppChannel(appChanMgr),
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

	handler := spydermcp.NewHandler(handlerOpts...)

	// Kick off pool adoption in the background so startup latency stays
	// low. Adoption rebuilds inventory from live simctl/avdmanager state
	// plus the persisted hold ledger. The pool itself is purely
	// demand-driven — sims are only created in response to Acquire, so
	// there's no Reconcile/pre-mint step at startup.
	if poolInst != nil {
		go func() {
			if err := poolInst.Adopt(context.Background()); err != nil {
				slog.Error("pool: adopt failed; proceeding with empty inventory", "error", err)
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
	// 🎯T91.5 the single-page dashboard over the app-channel surface. Pure
	// presentation on top of the same REST tools; served at /dashboard.
	dash := dashboard.NewHandler()
	mux.Handle(dashboard.Path, dash)
	mux.Handle(dashboard.Path+"/", dash)
	// 🎯T91.4/T92.2 dev H.264 stream relay — between a ge server
	// and the dashboard browser player. Speaks ge's brokered wire.
	relay := streamrelay.New()
	mux.HandleFunc("/ws/server", relay.HandleServerSideband)
	mux.HandleFunc("/ws/server/wire/", relay.HandleServerWire)
	mux.HandleFunc("/stream/player/", relay.HandlePlayerConnect)
	mux.HandleFunc("/ws/wire", relay.HandlePlayerWire) // ge's native player (PlayerWireBridge)
	mux.HandleFunc("/stream/servers", relay.HandleServerList)
	mux.HandleFunc("/stream/sessions", relay.HandleSessionList) // 🎯T96 hop telemetry
	return mux, resvStore, handler, logCapMgr, appChanMgr
}

// resolveIOSTunnelBinary returns the path to the bundled `ios` binary
// (the go-ios tunnel + CLI). Resolution order:
//
//  1. SPYDER_IOS_TUNNEL_BINARY environment variable.
//  2. <real-exe-dir>/../libexec/spyder/ios — the production install
//     layout. Homebrew installs spyder into the Cellar's bin/ and
//     the bundled ios binary into the Cellar's libexec/spyder/.
//     EvalSymlinks resolves the bin/ → Cellar symlink so the libexec
//     sibling is found relative to the real binary location.
//  3. bin/ios relative to the repo root — development fallback,
//     produced by `make build` alongside bin/spyder.
//
// Returns "" when no candidate exists; the caller logs a warning
// and proceeds without a tunnel (iOS-17+ DTX operations fail
// per-call rather than crashing the daemon).
func resolveIOSTunnelBinary() string {
	if env := os.Getenv("SPYDER_IOS_TUNNEL_BINARY"); env != "" {
		if _, err := os.Stat(env); err == nil {
			return env
		}
		slog.Warn("SPYDER_IOS_TUNNEL_BINARY set but binary not found",
			"path", env, "tunnel", "disabled")
		return ""
	}

	if exe, err := exePathReal(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "..", "libexec", "spyder", "ios")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	// Dev fallback: walk up from the executable looking for bin/ios
	// (parallel to bin/spyder).
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for range 5 {
			candidate := filepath.Join(dir, "ios")
			if _, err := os.Stat(candidate); err == nil && candidate != exe {
				slog.Info("iostunnel: using development build", "path", candidate)
				return candidate
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	slog.Warn("iostunnel: bundled `ios` binary not found — iOS-17+ DTX tools degraded; " +
		"set SPYDER_IOS_TUNNEL_BINARY or install via Homebrew")
	return ""
}

// exePathReal returns the path to the running executable with all
// symlinks resolved. Homebrew installs each formula in a versioned
// Cellar directory and links the binary into a flat bin/ —
// os.Executable returns the symlink path, but the libexec sibling
// lives next to the real binary inside the Cellar. Without resolving
// the symlink, every Homebrew-installed spyder fails to find the
// bundled tunnel binary.
func exePathReal() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
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

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/streamrelay"
)

// Default player package id across platforms (🎯T100.3).
const playerBundleID = "com.spyder.player"

// Player artifact path env overrides (optional).
const (
	envPlayerIOSPath     = "SPYDER_PLAYER_IOS"
	envPlayerAndroidPath = "SPYDER_PLAYER_ANDROID"
	envPlayerDesktopPath = "SPYDER_PLAYER_DESKTOP"
)

// launchPlayerResult is the JSON payload for launch_player.
type launchPlayerResult struct {
	Device     string `json:"device"`
	Platform   string `json:"platform"`
	Server     string `json:"server"`
	StreamAddr string `json:"stream_addr"`
	BundleID   string `json:"bundle_id"`
	PID        int    `json:"pid,omitempty"`
	Path       string `json:"path,omitempty"`
	Variant    string `json:"variant,omitempty"` // portrait|landscape|any
}

// StreamServers is satisfied by *streamrelay.Relay (optional wiring).
type StreamServers interface {
	Servers() []streamrelay.ServerInfo
	ServerOrientation(name string) string
}

// handleLaunchPlayer deploys/launches the spyder player with only device
// and optional server name — injects stream host:port and server name
// via platform-correct env (🎯T100.3).
func (h *Handler) handleLaunchPlayer(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	serverName := optString(args, "server")
	owner := optString(args, "owner")
	pathOverride := optString(args, "path")

	h.mu.Lock()
	if res := h.authorize(dev, owner); res != nil {
		h.mu.Unlock()
		return res, nil
	}
	adapter, platform, id, err := h.resolveAdapter(dev)
	if err != nil {
		h.mu.Unlock()
		return toolErr("%v", err)
	}
	h.mu.Unlock()

	// Resolve server name from catalogue when omitted.
	serverName, err = h.resolveStreamServerName(serverName)
	if err != nil {
		return toolErr("launch_player: %v", err)
	}

	host, err := pickAppChannelHost(platform, id)
	if err != nil {
		return toolErr("launch_player: stream host: %v", err)
	}
	port := h.streamPort()
	streamAddr := fmt.Sprintf("%s:%d", host, port)

	variant := h.orientationVariant(serverName) // portrait|landscape|any
	path, err := resolvePlayerPath(platform, pathOverride, variant)
	if err != nil {
		return toolErr("launch_player: %v", err)
	}

	// Platform-correct stream inject keys (iOS Documents path uses STREAM_ADDR
	// uppercase; Android Intent extras use stream_addr lowercase).
	env := map[string]string{
		"STREAM_ADDR":  streamAddr,
		"SERVER_NAME":  serverName,
		"stream_addr":  streamAddr,
		"server_name":  serverName,
	}

	// Prefer deploy when we have an artifact path; desktop may launch binary.
	bundleID := playerBundleID
	if path != "" {
		envAny := make(map[string]any, len(env))
		for k, v := range env {
			envAny[k] = v
		}
		// Install + launch via internal deploy (allowPlayer): public deploy_app
		// rejects the player package so STREAM_ADDR cannot be skipped.
		deployRes, derr := h.deployApp(map[string]any{
			"device":    dev,
			"path":      path,
			"bundle_id": bundleID,
			"owner":     owner,
			"env":       envAny,
		}, true /*allowPlayer*/)
		if derr != nil {
			return nil, derr
		}
		if deployRes != nil && deployRes.IsError {
			return deployRes, nil
		}
		// Parse pid from deploy JSON when present.
		pid := 0
		// handleDeployApp returns toolJSON(deployResult) — extract via redeploy.
		// Call wait for pid as a second check.
		if p, perr := adapter.AppPID(id, bundleID); perr == nil {
			pid = p
		}
		return toolJSON(launchPlayerResult{
			Device:     dev,
			Platform:   platform,
			Server:     serverName,
			StreamAddr: streamAddr,
			BundleID:   bundleID,
			PID:        pid,
			Path:       path,
			Variant:    variant,
		})
	}

	// No path: launch already-installed player.
	env, err = h.ensureAppChannelEnv(env, platform, id, bundleID)
	if err != nil {
		return toolErr("launch_player: %v", err)
	}
	if err := adapter.LaunchApp(id, bundleID, env); err != nil {
		return toolErr("launch_player: launch %s on %s: %v", bundleID, dev, err)
	}
	pid, _ := adapter.AppPID(id, bundleID)
	return toolJSON(launchPlayerResult{
		Device:     dev,
		Platform:   platform,
		Server:     serverName,
		StreamAddr: streamAddr,
		BundleID:   bundleID,
		PID:        pid,
		Variant:    variant,
	})
}

func (h *Handler) resolveStreamServerName(requested string) (string, error) {
	if requested != "" {
		// If catalogue is wired, verify the name exists when servers are known.
		if h.streamRelay != nil {
			servers := h.streamRelay.Servers()
			if len(servers) > 0 {
				for _, s := range servers {
					if s.Name == requested {
						return requested, nil
					}
				}
				return "", fmt.Errorf("server %q not registered; catalogue: %s", requested, serverNames(servers))
			}
		}
		return requested, nil
	}
	if h.streamRelay == nil {
		return "", fmt.Errorf("server name required (stream relay not wired; cannot auto-pick)")
	}
	servers := h.streamRelay.Servers()
	switch len(servers) {
	case 0:
		return "", fmt.Errorf("no streaming servers registered; start a game server or pass server=")
	case 1:
		return servers[0].Name, nil
	default:
		return "", fmt.Errorf("multiple streaming servers registered (%s); pass server=", serverNames(servers))
	}
}

func serverNames(servers []streamrelay.ServerInfo) string {
	parts := make([]string, 0, len(servers))
	for _, s := range servers {
		parts = append(parts, s.Name)
	}
	return strings.Join(parts, ", ")
}

func (h *Handler) streamPort() int {
	if h != nil && h.streamListenPort > 0 {
		return h.streamListenPort
	}
	return 3030
}

// orientationVariant returns portrait|landscape|any from sideband advertisement
// when available (🎯T100.4); defaults to any.
func (h *Handler) orientationVariant(serverName string) string {
	if h == nil || h.streamRelay == nil {
		return "any"
	}
	if v := h.streamRelay.ServerOrientation(serverName); v != "" {
		return v
	}
	return "any"
}

// resolvePlayerPath picks the player artifact for platform + orientation variant.
func resolvePlayerPath(platform, override, variant string) (string, error) {
	if override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", fmt.Errorf("path %q: %w", override, err)
		}
		return override, nil
	}
	candidates := playerPathCandidates(platform, variant)
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	// Desktop can launch via PATH / already-installed; mobile needs an artifact.
	if strings.EqualFold(platform, "desktop") {
		return "", nil
	}
	return "", fmt.Errorf("no player artifact found for platform=%s variant=%s (set path= or %s/%s)",
		platform, variant, envPlayerIOSPath, envPlayerAndroidPath)
}

func playerPathCandidates(platform, variant string) []string {
	// Prefer env overrides, then repo-relative build outputs.
	cwd, _ := os.Getwd()
	switch strings.ToLower(platform) {
	case "ios":
		env := os.Getenv(envPlayerIOSPath)
		var paths []string
		if env != "" {
			paths = append(paths, env)
		}
		// Orientation variants (🎯T100.4).
		switch variant {
		case "portrait":
			paths = append(paths,
				filepath.Join(cwd, "player/ios/build/xcode/Debug-Port/PlayerPort.app"),
				filepath.Join(cwd, "player/ios/build/xcode/Debug/PlayerPort.app"),
			)
		case "landscape":
			paths = append(paths,
				filepath.Join(cwd, "player/ios/build/xcode/Debug-Land/PlayerLand.app"),
				filepath.Join(cwd, "player/ios/build/xcode/Debug/PlayerLand.app"),
			)
		}
		paths = append(paths,
			filepath.Join(cwd, "player/ios/build/xcode/Debug/Player.app"),
			filepath.Join(cwd, "player/ios/build/xcode/Release/Player.app"),
		)
		return paths
	case "android":
		env := os.Getenv(envPlayerAndroidPath)
		var paths []string
		if env != "" {
			paths = append(paths, env)
		}
		paths = append(paths,
			filepath.Join(cwd, "player/android/app/build/outputs/apk/debug/app-debug.apk"),
			filepath.Join(cwd, "player/android/app/build/outputs/apk/release/app-release.apk"),
		)
		return paths
	case "desktop":
		env := os.Getenv(envPlayerDesktopPath)
		var paths []string
		if env != "" {
			paths = append(paths, env)
		}
		paths = append(paths,
			filepath.Join(cwd, "bin/player"),
			filepath.Join(cwd, "player/build/player"),
		)
		return paths
	default:
		return nil
	}
}

// parseListenPort extracts the TCP port from an addr like ":3030" or "127.0.0.1:3030".
func parseListenPort(addr string) int {
	if addr == "" {
		return 3030
	}
	// host:port or :port
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return 3030
	}
	p, err := strconv.Atoi(addr[i+1:])
	if err != nil || p <= 0 {
		return 3030
	}
	return p
}

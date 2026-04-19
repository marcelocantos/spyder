// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/inventory"
	"github.com/marcelocantos/spyder/internal/reservations"
)

func requireString(args map[string]any, key string) (string, error) {
	v, ok := args[key].(string)
	if !ok || v == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return v, nil
}

func optString(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

// toolErr wraps a user-facing error as a non-nil CallToolResult with
// IsError=true so clients surface it in the tool-result channel rather
// than treating it as a transport fault.
func toolErr(format string, args ...any) (*mcpgo.CallToolResult, error) {
	return mcpgo.NewToolResultError(fmt.Sprintf(format, args...)), nil
}

// toolText returns a successful CallToolResult carrying text.
func toolText(text string) (*mcpgo.CallToolResult, error) {
	return mcpgo.NewToolResultText(text), nil
}

// toolJSON marshals v as pretty-printed JSON and returns it as text.
func toolJSON(v any) (*mcpgo.CallToolResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return toolText(string(data))
}

func (h *Handler) handleDevices(args map[string]any) (*mcpgo.CallToolResult, error) {
	platform := optString(args, "platform")
	if platform == "" {
		platform = "all"
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	var devices []device.Info
	var perAdapterErrors []string

	if platform == "ios" || platform == "all" {
		ds, err := h.ios.List()
		if err != nil {
			if platform == "ios" {
				return toolErr("ios: %v", err)
			}
			perAdapterErrors = append(perAdapterErrors, fmt.Sprintf("ios: %v", err))
		}
		devices = append(devices, ds...)
	}
	if platform == "android" || platform == "all" {
		ds, err := h.android.List()
		if err != nil {
			if platform == "android" {
				return toolErr("android: %v", err)
			}
			perAdapterErrors = append(perAdapterErrors, fmt.Sprintf("android: %v", err))
		}
		devices = append(devices, ds...)
	}

	for i := range devices {
		if alias := h.inventory.AliasFor(devices[i].UUID); alias != "" {
			devices[i].Alias = alias
		}
	}

	if platform == "all" && len(perAdapterErrors) > 0 {
		return toolJSON(struct {
			Devices []device.Info `json:"devices"`
			Errors  []string      `json:"errors,omitempty"`
		}{devices, perAdapterErrors})
	}
	return toolJSON(devices)
}

func (h *Handler) handleResolve(args map[string]any) (*mcpgo.CallToolResult, error) {
	name, err := requireString(args, "name")
	if err != nil {
		return nil, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	entry, ok := h.inventory.Lookup(name)
	if !ok {
		entry = inventory.ClassifyRaw(name)
	}
	return toolJSON(entry)
}

func (h *Handler) handleKeepAwake(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	owner := optString(args, "owner")

	h.mu.Lock()
	defer h.mu.Unlock()

	if res := h.authorize(dev, owner); res != nil {
		return res, nil
	}
	adapter, platform, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
	}
	if err := adapter.LaunchKeepAwake(id); err != nil {
		return toolErr("launching KeepAwake on %s: %v", dev, err)
	}
	switch platform {
	case "android":
		return toolText(fmt.Sprintf("KeepAwake is a no-op on %s: Android handles stay-awake natively — enable Settings → Developer options → Stay awake while plugged in", dev))
	default:
		return toolText(fmt.Sprintf("KeepAwake launched on %s", dev))
	}
}

func (h *Handler) handleDeviceState(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	adapter, _, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
	}
	state, err := adapter.State(id)
	if err != nil {
		return toolErr("reading state: %v", err)
	}
	return toolJSON(state)
}

func (h *Handler) handleScreenshot(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	owner := optString(args, "owner")

	h.mu.Lock()
	defer h.mu.Unlock()

	if res := h.authorize(dev, owner); res != nil {
		return res, nil
	}
	adapter, platform, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
	}
	if platform == "ios" && h.tunneld != nil {
		if err := h.tunneld.Require(); err != nil {
			return toolErr("screenshot on %s: %v", dev, err)
		}
	}
	png, err := adapter.Screenshot(id)
	if err != nil {
		return toolErr("screenshot on %s: %v", dev, err)
	}
	return mcpgo.NewToolResultImage(
		fmt.Sprintf("screenshot of %s (%d bytes)", dev, len(png)),
		base64.StdEncoding.EncodeToString(png),
		"image/png",
	), nil
}

func (h *Handler) handleListApps(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	adapter, _, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
	}
	apps, err := adapter.ListApps(id)
	if err != nil {
		return toolErr("list_apps on %s: %v", dev, err)
	}
	return toolJSON(apps)
}

func (h *Handler) handleLaunchApp(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	bundleID, err := requireString(args, "bundle_id")
	if err != nil {
		return nil, err
	}
	owner := optString(args, "owner")
	h.mu.Lock()
	defer h.mu.Unlock()
	if res := h.authorize(dev, owner); res != nil {
		return res, nil
	}
	adapter, platform, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
	}
	if platform == "ios" && h.tunneld != nil {
		if err := h.tunneld.Require(); err != nil {
			return toolErr("launch_app on %s: %v", dev, err)
		}
	}
	if err := adapter.LaunchApp(id, bundleID); err != nil {
		return toolErr("launch_app %s on %s: %v", bundleID, dev, err)
	}
	return toolText(fmt.Sprintf("launched %s on %s", bundleID, dev))
}

func (h *Handler) handleTerminateApp(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	bundleID, err := requireString(args, "bundle_id")
	if err != nil {
		return nil, err
	}
	owner := optString(args, "owner")
	h.mu.Lock()
	defer h.mu.Unlock()
	if res := h.authorize(dev, owner); res != nil {
		return res, nil
	}
	adapter, platform, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
	}
	if platform == "ios" && h.tunneld != nil {
		if err := h.tunneld.Require(); err != nil {
			return toolErr("terminate_app on %s: %v", dev, err)
		}
	}
	if err := adapter.TerminateApp(id, bundleID); err != nil {
		return toolErr("terminate_app %s on %s: %v", bundleID, dev, err)
	}
	return toolText(fmt.Sprintf("terminated %s on %s", bundleID, dev))
}

// authorize checks that the caller (identified by owner) may perform
// a mutating operation against dev. Returns nil if allowed;
// a *mcpgo.CallToolResult with IsError=true otherwise. Passes through
// when no reservation store is wired (tests/embedded use).
func (h *Handler) authorize(dev, owner string) *mcpgo.CallToolResult {
	if h.reservations == nil {
		return nil
	}
	if err := h.reservations.Authorize(dev, owner); err != nil {
		res, _ := toolErr("%v", err)
		return res
	}
	return nil
}

// --- reservation tools -------------------------------------------------

func (h *Handler) handleReserve(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.reservations == nil {
		return toolErr("reservations not configured on this server")
	}
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	owner, err := requireString(args, "owner")
	if err != nil {
		return nil, err
	}
	ttl := time.Duration(optNumber(args, "ttl_seconds")) * time.Second
	note := optString(args, "note")

	r, err := h.reservations.Acquire(dev, owner, ttl, note)
	if err != nil {
		return toolErr("%v", err)
	}
	return toolJSON(r)
}

func (h *Handler) handleRelease(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.reservations == nil {
		return toolErr("reservations not configured on this server")
	}
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	owner, err := requireString(args, "owner")
	if err != nil {
		return nil, err
	}
	if err := h.reservations.Release(dev, owner); err != nil {
		return toolErr("%v", err)
	}
	return toolText(fmt.Sprintf("released reservation on %s", dev))
}

func (h *Handler) handleRenew(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.reservations == nil {
		return toolErr("reservations not configured on this server")
	}
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	owner, err := requireString(args, "owner")
	if err != nil {
		return nil, err
	}
	ttl := time.Duration(optNumber(args, "ttl_seconds")) * time.Second
	r, err := h.reservations.Renew(dev, owner, ttl)
	if err != nil {
		return toolErr("%v", err)
	}
	return toolJSON(r)
}

func (h *Handler) handleReservations(_ map[string]any) (*mcpgo.CallToolResult, error) {
	if h.reservations == nil {
		return toolJSON([]reservations.Reservation{})
	}
	return toolJSON(h.reservations.List())
}

// optNumber extracts a float64-coerced integer-ish value. MCP
// arguments arrive as map[string]any where JSON numbers decode to
// float64; we cast to int64 seconds for time.Duration.
func optNumber(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	}
	return 0
}

// resolveAdapter maps a user-provided device reference (alias or raw UUID)
// to the platform adapter, the platform name ("ios" | "android"), and the
// platform-specific identifier it expects. Raw identifiers not in the
// inventory are classified by format (iOS UDID vs. Android serial) via
// inventory.ClassifyRaw.
func (h *Handler) resolveAdapter(ref string) (device.Adapter, string, string, error) {
	entry, ok := h.inventory.Lookup(ref)
	if !ok {
		classified := inventory.ClassifyRaw(ref)
		if classified.Platform == "android" {
			return h.android, "android", ref, nil
		}
		return h.ios, "ios", ref, nil
	}
	switch entry.Platform {
	case "ios":
		id := entry.IOSUUID
		if id == "" {
			id = entry.IOSCoreDevice
		}
		return h.ios, "ios", id, nil
	case "android":
		return h.android, "android", entry.AndroidSerial, nil
	default:
		return nil, "", "", fmt.Errorf("inventory entry %q has unknown platform %q", ref, entry.Platform)
	}
}

// validateAppPath rejects paths with ".." traversal components and
// paths that do not exist. Returns the cleaned absolute path.
func validateAppPath(path string) (string, error) {
	if strings.Contains(path, "..") {
		return "", fmt.Errorf("path must not contain '..': %q", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving path: %w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("path does not exist: %q", abs)
	}
	return abs, nil
}

func (h *Handler) handleInstallApp(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	path, err := requireString(args, "path")
	if err != nil {
		return nil, err
	}
	owner := optString(args, "owner")

	path, err = validateAppPath(path)
	if err != nil {
		return toolErr("%v", err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if res := h.authorize(dev, owner); res != nil {
		return res, nil
	}
	adapter, _, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
	}
	if err := adapter.InstallApp(id, path); err != nil {
		return toolErr("install_app on %s: %v", dev, err)
	}
	return toolText(fmt.Sprintf("installed %s on %s", filepath.Base(path), dev))
}

func (h *Handler) handleUninstallApp(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	bundleID, err := requireString(args, "bundle_id")
	if err != nil {
		return nil, err
	}
	owner := optString(args, "owner")

	h.mu.Lock()
	defer h.mu.Unlock()

	if res := h.authorize(dev, owner); res != nil {
		return res, nil
	}
	adapter, _, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
	}
	if err := adapter.UninstallApp(id, bundleID); err != nil {
		return toolErr("uninstall_app on %s: %v", dev, err)
	}
	return toolText(fmt.Sprintf("uninstalled %s from %s", bundleID, dev))
}

// deployResult is the JSON payload returned by deploy_app on success.
type deployResult struct {
	BundleID string `json:"bundle_id"`
	PID      int    `json:"pid"`
}

func (h *Handler) handleDeployApp(args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	path, err := requireString(args, "path")
	if err != nil {
		return nil, err
	}
	bundleID := optString(args, "bundle_id")
	owner := optString(args, "owner")

	path, err = validateAppPath(path)
	if err != nil {
		return toolErr("%v", err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if res := h.authorize(dev, owner); res != nil {
		return res, nil
	}
	adapter, platform, id, err := h.resolveAdapter(dev)
	if err != nil {
		return toolErr("%v", err)
	}

	// Derive bundle id from the path if not supplied.
	if bundleID == "" {
		var deriveErr error
		bundleID, deriveErr = deriveBundleID(platform, path)
		if deriveErr != nil {
			return toolErr("cannot derive bundle_id from %q: %v — pass --bundle-id explicitly", path, deriveErr)
		}
	}

	// Tunneld gate for iOS DVT operations (launch + app-pid).
	if platform == "ios" && h.tunneld != nil {
		if err := h.tunneld.Require(); err != nil {
			return toolErr("deploy_app on %s: %v", dev, err)
		}
	}

	// Step 1: terminate (ignore "not running" errors — it's fine if the
	// app isn't already up; the important thing is we tried).
	if termErr := adapter.TerminateApp(id, bundleID); termErr != nil {
		// Only propagate if the error is not "not running" or "not found".
		if !isNotRunningError(termErr) {
			return toolErr("deploy_app: terminate %s on %s: %v", bundleID, dev, termErr)
		}
	}

	// Step 2: install (fail fast on error).
	if err := adapter.InstallApp(id, path); err != nil {
		return toolErr("deploy_app: install %s on %s: %v", filepath.Base(path), dev, err)
	}

	// Step 3: launch.
	if err := adapter.LaunchApp(id, bundleID); err != nil {
		return toolErr("deploy_app: launch %s on %s: %v", bundleID, dev, err)
	}

	// Step 4: verify new PID.
	pid, err := adapter.AppPID(id, bundleID)
	if err != nil {
		return toolErr("deploy_app: verify pid for %s on %s: %v", bundleID, dev, err)
	}

	return toolJSON(deployResult{BundleID: bundleID, PID: pid})
}

// isNotRunningError returns true for "app not running" / "not installed"
// errors from TerminateApp. These are safe to ignore during a deploy.
func isNotRunningError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "not running") ||
		strings.Contains(s, "app not running") ||
		strings.Contains(s, "not installed") ||
		strings.Contains(s, "not found")
}

// deriveBundleID extracts the CFBundleIdentifier from an iOS .app bundle's
// Info.plist, or the package name from an Android .apk via aapt (with a
// --bundle-id fallback message when aapt is absent).
func deriveBundleID(platform, path string) (string, error) {
	switch platform {
	case "ios":
		return iosBundleIDFromApp(path)
	case "android":
		return androidBundleIDFromAPK(path)
	default:
		return "", fmt.Errorf("unsupported platform %q", platform)
	}
}

// iosBundleIDFromApp reads CFBundleIdentifier from <path>/Info.plist.
// The plist is expected in its JSON-serialised form as produced by
// xcrun plutil or after xcrun devicectl install; for raw binary plists
// we shell out to plutil -convert json.
func iosBundleIDFromApp(appPath string) (string, error) {
	plistPath := filepath.Join(appPath, "Info.plist")
	if _, err := os.Stat(plistPath); err != nil {
		return "", fmt.Errorf("Info.plist not found in %q", appPath)
	}
	// Convert to JSON then parse — handles both binary and XML plists.
	var outBuf, errBuf bytes.Buffer
	cmd := exec.Command("plutil", "-convert", "json", "-o", "-", plistPath)
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("plutil convert Info.plist: %w", err)
	}
	var plist struct {
		CFBundleIdentifier string `json:"CFBundleIdentifier"`
	}
	if err := json.Unmarshal(outBuf.Bytes(), &plist); err != nil {
		return "", fmt.Errorf("parse Info.plist JSON: %w", err)
	}
	if plist.CFBundleIdentifier == "" {
		return "", fmt.Errorf("CFBundleIdentifier not set in Info.plist")
	}
	return plist.CFBundleIdentifier, nil
}

// androidBundleIDFromAPK extracts the package name from an APK via
// `aapt dump badging`. Falls back to a clear error when aapt is absent.
func androidBundleIDFromAPK(apkPath string) (string, error) {
	var outBuf bytes.Buffer
	cmd := exec.Command("aapt", "dump", "badging", apkPath)
	cmd.Stdout = &outBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("aapt dump badging: %w (is aapt in PATH? install Android SDK build-tools)", err)
	}
	for _, line := range strings.Split(outBuf.String(), "\n") {
		if !strings.HasPrefix(line, "package:") {
			continue
		}
		// package: name='com.example.app' versionCode=... versionName=...
		for _, field := range strings.Fields(line) {
			if after, ok := strings.CutPrefix(field, "name='"); ok {
				return strings.TrimSuffix(after, "'"), nil
			}
		}
	}
	return "", fmt.Errorf("package name not found in aapt output")
}

// Compile-time assertion that errors package is imported (for build).
var _ = errors.New

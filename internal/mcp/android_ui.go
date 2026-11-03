// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// 🎯T180 — Android native-app UI verbs: input_text, ui_tree, ui_tap,
// screen_secure (input_key's extended key table lives in
// android_control.go). Android only; iOS fails closed like input_tap.
//
// input_text redaction: the text is never echoed in a response or
// error, and dispatch/REST logging records only verb + device. When
// secret=true or the text looks like a PIN, the response says
// redacted=true; otherwise it still reports only a character count.

package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/agent"
	"github.com/marcelocantos/spyder/internal/device"
)

// errIOSUIUnsupported is the fail-closed message for the 🎯T180 verbs on iOS.
const errIOSUIUnsupported = "not supported on iOS physical devices — Android only (🎯T180). Use app_input (ge/app-channel) or mobile-mcp for iOS UI automation"

type textInputter interface {
	InputTextContext(ctx context.Context, id, text, mode string) (device.TextInputResult, error)
}

type uiTreeDumper interface {
	UITreeContext(ctx context.Context, id string) (device.UITree, error)
}

type screenSecureProber interface {
	ScreenSecureContext(ctx context.Context, id string, screenshot bool) (device.ScreenSecureInfo, error)
}

type multiKeyInjector interface {
	InjectKeyEventsContext(ctx context.Context, id string, codes []int) error
}

func optBoolArg(args map[string]any, key string, def bool) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(v) {
		case "true", "1", "yes":
			return true
		case "false", "0", "no":
			return false
		}
	}
	return def
}

// resolveOSObserve resolves a device for an observational verb (no
// reservation gate — standing policy for reads) and rejects desktop.
func (h *Handler) resolveOSObserve(dev string) (device.Adapter, string, string, *mcpgo.CallToolResult) {
	h.mu.Lock()
	defer h.mu.Unlock()
	adapter, platform, id, err := h.resolveAdapter(dev)
	if err != nil {
		res, _ := toolErr("%v", err)
		return nil, "", "", res
	}
	if platform != "android" && platform != "ios" {
		res, _ := toolErr("device %s is %s — UI verbs support android only (🎯T180)", dev, platform)
		return nil, "", "", res
	}
	return adapter, platform, id, nil
}

// authorizeOSMutate authorizes (reservation + agent gate) a mutating UI
// verb under mu, then releases mu so slow adb work does not block the
// daemon.
func (h *Handler) authorizeOSMutate(dev, owner string) (device.Adapter, string, string, *mcpgo.CallToolResult) {
	h.mu.Lock()
	defer h.mu.Unlock()
	adapter, platform, id, errRes := h.requireOSControlCap(dev, owner)
	if errRes != nil {
		return nil, "", "", errRes
	}
	if res := h.gateAgentAny(owner, agent.MutInput, dev, id); res != nil {
		return nil, "", "", res
	}
	return adapter, platform, id, nil
}

// ---- input_text ----

func (h *Handler) handleInputText(args map[string]any) (*mcpgo.CallToolResult, error) {
	return h.handleInputTextContext(context.Background(), args)
}

func (h *Handler) handleInputTextContext(ctx context.Context, args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	text, ok := args["text"].(string)
	if !ok || text == "" {
		return nil, errors.New("text is required")
	}
	owner := optString(args, "owner")
	mode := optString(args, "mode")
	secret := optBoolArg(args, "secret", false) || device.LooksSecret(text)

	adapter, platform, id, errRes := h.authorizeOSMutate(dev, owner)
	if errRes != nil {
		return errRes, nil
	}
	if platform == "ios" {
		return toolErr("input_text: %s", errIOSUIUnsupported)
	}
	ti, ok := adapter.(textInputter)
	if !ok {
		return toolErr("input_text: not supported on %s (Android only)", platform)
	}
	res, err := ti.InputTextContext(ctx, id, text, mode)
	if err != nil {
		// Device layer scrubs the text; scrub again defensively.
		return toolErr("input_text: %s", scrubText(err.Error(), text))
	}
	return toolJSON(map[string]any{
		"device":   dev,
		"platform": platform,
		"chars":    res.Chars,
		"mode":     res.Mode,
		"steps":    res.Steps,
		"redacted": secret,
		"injected": true,
	})
}

func scrubText(msg, text string) string {
	if text == "" {
		return msg
	}
	msg = strings.ReplaceAll(msg, text, "[redacted]")
	return strings.ReplaceAll(msg, strings.ReplaceAll(text, " ", "%s"), "[redacted]")
}

// ---- ui_tree ----

func (h *Handler) handleUITree(args map[string]any) (*mcpgo.CallToolResult, error) {
	return h.handleUITreeContext(context.Background(), args)
}

func (h *Handler) handleUITreeContext(ctx context.Context, args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	compact := optBoolArg(args, "compact", false)
	adapter, platform, id, errRes := h.resolveOSObserve(dev)
	if errRes != nil {
		return errRes, nil
	}
	if platform == "ios" {
		return toolErr("ui_tree: %s", errIOSUIUnsupported)
	}
	d, ok := adapter.(uiTreeDumper)
	if !ok {
		return toolErr("ui_tree: not supported on %s (Android only)", platform)
	}
	tree, err := d.UITreeContext(ctx, id)
	if err != nil {
		return toolErr("ui_tree: %v", err)
	}
	total := device.CountUINodes(tree.Nodes)
	if compact {
		tree = device.CompactUITree(tree)
	}
	return toolJSON(map[string]any{
		"device":     dev,
		"platform":   platform,
		"package":    tree.Package,
		"rotation":   tree.Rotation,
		"compact":    compact,
		"node_count": device.CountUINodes(tree.Nodes),
		"raw_count":  total,
		"nodes":      tree.Nodes,
	})
}

// ---- ui_tap ----

func (h *Handler) handleUITap(args map[string]any) (*mcpgo.CallToolResult, error) {
	return h.handleUITapContext(context.Background(), args)
}

func describeUINode(n *device.UINode) map[string]any {
	m := map[string]any{"class": n.Class, "bounds": n.Bounds, "center": n.Center}
	if n.Text != "" {
		m["text"] = n.Text
	}
	if n.ContentDesc != "" {
		m["content-desc"] = n.ContentDesc
	}
	if n.ResourceID != "" {
		m["resource-id"] = n.ResourceID
	}
	return m
}

func (h *Handler) handleUITapContext(ctx context.Context, args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	owner := optString(args, "owner")
	sel := device.UISelector{
		Text:       optString(args, "text"),
		ResourceID: optString(args, "resource_id"),
		Desc:       optString(args, "desc"),
		Contains:   optBoolArg(args, "contains", false),
	}
	if sel.Empty() {
		return toolErr("ui_tap: one of text, resource_id, or desc is required")
	}
	index := -1
	if _, ok := args["index"]; ok {
		if index, err = requireInt(args, "index"); err != nil || index < 0 {
			return toolErr("ui_tap: index must be a non-negative integer")
		}
	}

	adapter, platform, id, errRes := h.authorizeOSMutate(dev, owner)
	if errRes != nil {
		return errRes, nil
	}
	if platform == "ios" {
		return toolErr("ui_tap: %s", errIOSUIUnsupported)
	}
	d, ok := adapter.(uiTreeDumper)
	if !ok {
		return toolErr("ui_tap: not supported on %s (Android only)", platform)
	}
	tree, err := d.UITreeContext(ctx, id)
	if err != nil {
		return toolErr("ui_tap: %v", err)
	}
	matches := device.FindUINodes(tree, sel)
	selDesc := selectorString(sel)
	switch {
	case len(matches) == 0:
		return toolErr("ui_tap: no on-screen node matches %s (foreground package %s); call ui_tree to inspect", selDesc, tree.Package)
	case len(matches) > 1 && index < 0:
		var b strings.Builder
		for i, n := range matches {
			if i == 10 {
				fmt.Fprintf(&b, "\n  … %d more", len(matches)-10)
				break
			}
			fmt.Fprintf(&b, "\n  [%d] %s text=%q desc=%q id=%q bounds=%v", i, n.Class, n.Text, n.ContentDesc, n.ResourceID, n.Bounds)
		}
		return toolErr("ui_tap: %d nodes match %s — refine the selector or pass index:%s", len(matches), selDesc, b.String())
	case index >= len(matches):
		return toolErr("ui_tap: index %d out of range (%d matches for %s)", index, len(matches), selDesc)
	}
	if index < 0 {
		index = 0
	}
	n := matches[index]
	if !n.Enabled {
		return toolErr("ui_tap: matched node is disabled: %s text=%q desc=%q id=%q", n.Class, n.Text, n.ContentDesc, n.ResourceID)
	}
	var tapErr error
	if cinj, ok := adapter.(contextTouchInjector); ok {
		tapErr = cinj.InjectTapContext(ctx, id, n.Center[0], n.Center[1])
	} else if inj, ok := adapter.(touchInjector); ok {
		tapErr = inj.InjectTap(id, n.Center[0], n.Center[1])
	} else {
		return toolErr("ui_tap: adapter does not support OS input inject")
	}
	if tapErr != nil {
		return toolErr("ui_tap: %v", tapErr)
	}
	return toolJSON(map[string]any{
		"device":   dev,
		"platform": platform,
		"matches":  len(matches),
		"index":    index,
		"node":     describeUINode(n),
		"x":        n.Center[0],
		"y":        n.Center[1],
		"injected": true,
	})
}

func selectorString(s device.UISelector) string {
	var parts []string
	if s.Text != "" {
		parts = append(parts, fmt.Sprintf("text=%q", s.Text))
	}
	if s.ResourceID != "" {
		parts = append(parts, fmt.Sprintf("resource_id=%q", s.ResourceID))
	}
	if s.Desc != "" {
		parts = append(parts, fmt.Sprintf("desc=%q", s.Desc))
	}
	if s.Contains {
		parts = append(parts, "contains")
	}
	return strings.Join(parts, " ")
}

// ---- screen_secure ----

func (h *Handler) handleScreenSecure(args map[string]any) (*mcpgo.CallToolResult, error) {
	return h.handleScreenSecureContext(context.Background(), args)
}

func (h *Handler) handleScreenSecureContext(ctx context.Context, args map[string]any) (*mcpgo.CallToolResult, error) {
	dev, err := requireString(args, "device")
	if err != nil {
		return nil, err
	}
	screenshot := optBoolArg(args, "screenshot", true)
	adapter, platform, id, errRes := h.resolveOSObserve(dev)
	if errRes != nil {
		return errRes, nil
	}
	if platform == "ios" {
		return toolErr("screen_secure: %s", errIOSUIUnsupported)
	}
	p, ok := adapter.(screenSecureProber)
	if !ok {
		return toolErr("screen_secure: not supported on %s (Android only)", platform)
	}
	info, err := p.ScreenSecureContext(ctx, id, screenshot)
	if err != nil {
		return toolErr("screen_secure: %v", err)
	}
	return toolJSON(map[string]any{
		"device":           dev,
		"platform":         platform,
		"verdict":          info.Verdict,
		"secure_flag":      info.FocusedSecure,
		"focused_window":   info.FocusedWindow,
		"package":          info.Package,
		"focused_flags":    info.FocusedFlags,
		"secure_windows":   info.SecureWindows,
		"screen_awake":     info.ScreenAwake,
		"screenshot_black": info.ScreenshotBlack,
		"screenshot_error": info.ScreenshotError,
	})
}

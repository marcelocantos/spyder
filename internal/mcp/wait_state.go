// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// 🎯T129 — wait_state polls app_state / jq until the select is truthy.

package mcp

import (
	"context"
	"errors"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/vmihailenco/msgpack/v5"

	"github.com/marcelocantos/spyder/internal/appchannel"
)

func (h *Handler) handleWaitState(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	slice, err := requireString(args, "slice")
	if err != nil {
		return nil, err
	}
	selectExpr := optString(args, "select")

	timeoutMs := 10000
	if v, ok := args["timeout_ms"].(float64); ok && v > 0 {
		timeoutMs = int(v)
	}
	pollMs := 200
	if v, ok := args["poll_ms"].(float64); ok && v > 0 {
		pollMs = int(v)
	}

	timeout := time.Duration(timeoutMs) * time.Millisecond
	if timeout > appchannel.MaxWaitTimeout {
		timeout = appchannel.MaxWaitTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout+DeadlineWaitStateMargin)
	defer cancel()

	got, werr := appchannel.WaitState(ctx, appchannel.WaitStateArgs{
		Slice:     slice,
		Select:    selectExpr,
		Timeout:   timeout,
		PollEvery: time.Duration(pollMs) * time.Millisecond,
	}, func(ctx context.Context, sl string) (msgpack.RawMessage, error) {
		return s.Call(ctx, appchannel.MethodStateQuery, map[string]string{"slice": sl}, appchannel.DefaultRequestTimeout)
	})
	if werr != nil {
		var to *appchannel.WaitTimeoutError
		if errors.As(werr, &to) {
			return toolErr("%s", to.Error())
		}
		var jqErr *appchannel.JQError
		if errors.As(werr, &jqErr) {
			return toolJSON(map[string]any{"select_error": jqErr})
		}
		return toolErr("wait_state: %v", werr)
	}
	return toolJSON(map[string]any{
		"slice":      slice,
		"select":     selectExpr,
		"value":      got.Value,
		"attempts":   got.Attempts,
		"elapsed_ms": got.Elapsed.Milliseconds(),
	})
}

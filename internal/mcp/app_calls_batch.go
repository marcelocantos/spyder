// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// Hybrid device smoke — batch app_call RPCs in one round-trip.

package mcp

import (
	"context"
	"fmt"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/appchannel"
)

const maxAppCallsBatch = 32

// appCallsBatchArgKeys are accepted top-level keys for app_calls_batch.
var appCallsBatchArgKeys = map[string]bool{
	"session_id":    true,
	"device":        true,
	"bundle_id":     true,
	"calls":         true,
	"stop_on_error": true,
}

// handleAppCallsBatch runs multiple app_call RPCs against one session.
func (h *Handler) handleAppCallsBatch(args map[string]any) (*mcpgo.CallToolResult, error) {
	s, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	for k := range args {
		if !appCallsBatchArgKeys[k] {
			return toolErr("app_calls_batch: unknown argument %q", k)
		}
	}
	rawCalls, ok := args["calls"]
	if !ok {
		return toolErr("app_calls_batch: 'calls' is required — a list of {method, params?, timeout_ms?}")
	}
	calls, err := parseAppCallsBatch(rawCalls)
	if err != nil {
		return toolErr("app_calls_batch: %v", err)
	}
	if len(calls) == 0 {
		return toolErr("app_calls_batch: calls must not be empty")
	}
	if len(calls) > maxAppCallsBatch {
		return toolErr("app_calls_batch: at most %d calls per batch", maxAppCallsBatch)
	}
	stopOnError := true
	if v, ok := args["stop_on_error"]; ok {
		stopOnError, _ = v.(bool)
	}

	results := make([]map[string]any, 0, len(calls))
	for _, c := range calls {
		entry, callErr := h.invokeAppCall(s, c.method, c.params, c.timeout)
		if callErr != "" {
			results = append(results, map[string]any{
				"method": c.method,
				"error":  callErr,
			})
			if stopOnError {
				break
			}
			continue
		}
		results = append(results, map[string]any{
			"method": c.method,
			"result": entry,
		})
	}
	return toolJSON(map[string]any{
		"session_id": s.ID,
		"results":    results,
	})
}

type batchCall struct {
	method  string
	params  any
	timeout time.Duration
}

func parseAppCallsBatch(raw any) ([]batchCall, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("calls must be a list")
	}
	out := make([]batchCall, 0, len(list))
	for i, el := range list {
		m, ok := el.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("calls[%d] must be an object", i)
		}
		method, _ := m["method"].(string)
		if method == "" {
			return nil, fmt.Errorf("calls[%d].method is required", i)
		}
		if method == appchannel.MethodHello {
			return nil, fmt.Errorf("calls[%d]: cannot invoke hello", i)
		}
		var params any
		if p, ok := m["params"]; ok {
			params = p
		} else {
			params = map[string]any{}
		}
		timeout := appchannel.DefaultRequestTimeout
		if ms, ok := m["timeout_ms"].(float64); ok && ms > 0 {
			timeout = time.Duration(ms) * time.Millisecond
		}
		out = append(out, batchCall{method: method, params: params, timeout: timeout})
	}
	return out, nil
}

// invokeAppCall runs one RPC and returns the decoded result or an error string.
func (h *Handler) invokeAppCall(s *appchannel.Session, method string, params any, timeout time.Duration) (any, string) {
	if !s.Supports(method) {
		return nil, fmt.Sprintf("app does not advertise method %q (call app_methods)", method)
	}
	res, err := s.Call(context.Background(), method, params, timeout)
	if err != nil {
		return nil, fmt.Sprintf("%v%s", err, expectedParamsHint(s, method))
	}
	out, err := appchannel.ApplyJQ("", res)
	if err != nil {
		return nil, fmt.Sprintf("decode: %v", err)
	}
	return out, ""
}

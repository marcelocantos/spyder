// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package rest exposes spyder's tool surface as a plain HTTP+JSON
// API on the same listener as the MCP streamable transport. The REST
// handlers share the same Handler.Dispatch path as MCP, so reservation
// state is transport-agnostic: an agent holding a reservation via MCP
// blocks a shell script hitting REST and vice versa.
//
// Shape:
//
//	POST /api/v1/<tool>
//	  request body:  JSON object of the tool's arguments (same as MCP)
//	  response body: JSON marshalling of mcp.CallToolResult
//	                 ({"content":[{"type":"text","text":"…"}, …],
//	                   "isError": bool})
//
// Image-bearing tools (screenshot) yield an image content block with
// base64 data + mimeType, identical to the MCP surface.
package rest

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	spydermcp "github.com/marcelocantos/spyder/internal/mcp"
)

// Prefix is the URL prefix under which REST endpoints live.
const Prefix = "/api/v1/"

// StreamPath is the URL path for the SSE live log stream endpoint.
const StreamPath = Prefix + "log_stream"

// NewHandler returns an http.Handler that routes Prefix/<tool>
// POST requests to h.Dispatch. Unknown tools return 404; non-POST
// methods return 405; malformed JSON bodies return 400. The special
// path /api/v1/log_stream is handled by the SSE streaming handler.
func NewHandler(h *spydermcp.Handler) http.Handler {
	return &restHandler{
		h:      h,
		stream: NewStreamHandler(h),
	}
}

type restHandler struct {
	h      *spydermcp.Handler
	stream http.Handler
}

func (rh *restHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, Prefix) {
		http.NotFound(w, r)
		return
	}
	// The SSE log_stream endpoint has its own handler.
	if r.URL.Path == StreamPath {
		rh.stream.ServeHTTP(w, r)
		return
	}
	tool := strings.TrimPrefix(r.URL.Path, Prefix)
	if tool == "" || strings.Contains(tool, "/") {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Empty body is valid for tools with no arguments (e.g. reservations).
	args := map[string]any{}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "reading body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &args); err != nil {
			http.Error(w, "parsing JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	result, err := rh.h.Dispatch(tool, args)
	if err != nil {
		code := http.StatusBadRequest
		if strings.HasPrefix(err.Error(), "unknown tool") {
			code = http.StatusNotFound
		}
		http.Error(w, err.Error(), code)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

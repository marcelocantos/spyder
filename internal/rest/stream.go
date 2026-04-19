// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package rest — SSE streaming endpoint for live device log tailing.
//
// POST /api/v1/log_stream
//
//	Body: JSON object with keys:
//	  device    string (required)
//	  process   string (optional)
//	  subsystem string (optional, iOS only)
//	  tag       string (optional, Android only)
//	  regex     string (optional)
//
// Response: text/event-stream. Each event is a single JSON-encoded LogLine.
// The stream runs until the client disconnects (context cancellation) or
// an unrecoverable adapter error occurs.

package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/marcelocantos/spyder/internal/device"
	spydermcp "github.com/marcelocantos/spyder/internal/mcp"
)

// logStreamHandler handles POST /api/v1/log_stream with SSE output.
type logStreamHandler struct {
	h *spydermcp.Handler
}

// NewStreamHandler returns an http.Handler for the SSE log stream endpoint.
// It should be registered at /api/v1/log_stream by the router.
func NewStreamHandler(h *spydermcp.Handler) http.Handler {
	return &logStreamHandler{h: h}
}

// logStreamRequest holds parsed body parameters for the stream endpoint.
type logStreamRequest struct {
	Device    string `json:"device"`
	Process   string `json:"process"`
	Subsystem string `json:"subsystem"`
	Tag       string `json:"tag"`
	Regex     string `json:"regex"`
}

func (lh *logStreamHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "reading body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var req logStreamRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "parsing JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if req.Device == "" {
		http.Error(w, "device is required", http.StatusBadRequest)
		return
	}

	filter := device.LogFilter{
		Process:   req.Process,
		Subsystem: req.Subsystem,
		Tag:       req.Tag,
		Regex:     req.Regex,
	}

	adapter, id, err := lh.h.ResolveAdapterForStream(req.Device)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Set SSE headers before writing the first byte.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // nginx: disable proxy buffering
	w.WriteHeader(http.StatusOK)

	flusher, canFlush := w.(http.Flusher)

	out := make(chan device.LogLine, 64)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Run the adapter's stream in a goroutine; close out when done.
	go func() {
		defer close(out)
		_ = adapter.LogStream(ctx, id, filter, out)
	}()

	enc := json.NewEncoder(w)
	for {
		select {
		case ll, ok := <-out:
			if !ok {
				return
			}
			if _, err := fmt.Fprintf(w, "data: "); err != nil {
				return
			}
			if err := enc.Encode(ll); err != nil {
				return
			}
			// SSE events are separated by a blank line.
			if _, err := fmt.Fprintf(w, "\n"); err != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		case <-ctx.Done():
			return
		}
	}
}

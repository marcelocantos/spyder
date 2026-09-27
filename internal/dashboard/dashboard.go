// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package dashboard serves spyder's single-page control UI (🎯T91.5 /
// 🎯T137 / 🎯T138 / 🎯T139): a browser view over the app-channel surface
// (info/tweaks/logs/state/screenshot), a fleet Battery tab, and a Verify
// tab for DAG workflows. Mutations go through the same REST tool surface
// (POST /api/v1/<tool>) an agent uses. Verify live state is pushed on
// GET /ws/verify as Snapshot JSON; the tab does not poll.
package dashboard

import (
	_ "embed"
	"net/http"
)

// Path is the URL prefix the dashboard is served under.
const Path = "/dashboard"

//go:embed index.html
var indexHTML []byte

// NewHandler returns an http.Handler serving the dashboard SPA. It handles
// the Path prefix; any sub-path returns the same single page (the app is
// client-routed). The page talks to REST and, for Verify, GET /ws/verify
// on the same origin.
func NewHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(indexHTML)
	})
}

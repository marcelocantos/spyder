// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package playerweb serves the browser player (🎯T101/🎯T106): the spyder
// player C++ tree compiled to wasm, presented at /player/?name=<server>.
//
// v1 serves the built bundle from disk rather than embedding it in the
// binary — the wasm artifacts are large and build via Emscripten, which
// the release CI lacks; packaging them into the Homebrew distribution is
// 🎯T102. Resolution order:
//
//  1. $SPYDER_PLAYER_WEB
//  2. <executable dir>/../player/web/dist   (repo checkout: bin/spyder)
//  3. ~/.spyder/player-web
package playerweb

import (
	"net/http"
	"os"
	"path/filepath"
)

// Path is the HTTP mount point.
const Path = "/player"

// Dir resolves the bundle directory, or "" when no bundle is installed.
func Dir() string {
	var candidates []string
	if env := os.Getenv("SPYDER_PLAYER_WEB"); env != "" {
		candidates = append(candidates, env)
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates,
			filepath.Join(filepath.Dir(exe), "..", "player", "web", "dist"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".spyder", "player-web"))
	}
	for _, dir := range candidates {
		if st, err := os.Stat(filepath.Join(dir, "index.html")); err == nil &&
			!st.IsDir() {
			return dir
		}
	}
	return ""
}

// NewHandler serves the bundle. When no bundle is present it responds 404
// with a hint rather than failing daemon startup — the player page is an
// optional surface.
func NewHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dir := Dir()
		if dir == "" {
			http.Error(w,
				"browser player bundle not installed: build with "+
					"`make -C player web` or set SPYDER_PLAYER_WEB",
				http.StatusNotFound)
			return
		}
		// wasm streaming compile requires the right MIME; Go's sniffing
		// misses .wasm on some systems.
		if filepath.Ext(r.URL.Path) == ".wasm" {
			w.Header().Set("Content-Type", "application/wasm")
		}
		http.StripPrefix(Path+"/", http.FileServer(http.Dir(dir))).
			ServeHTTP(w, r)
	})
}

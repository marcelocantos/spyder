// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcelocantos/spyder/internal/rest"
)

// TestCLIHermeticity locks 🎯T37.6: each spyder CLI invocation is
// independent — proxy subcommands do not read or write any state file
// under ~/.spyder/. The foundation of every proxy subcommand is
// postTool; if it stays clean against a reachable daemon, the
// subcommands do too. (The two legitimate filesystem touches —
// autoStartDaemon's daemon.log and `spyder run`'s direct reservation
// store — are guarded by TestCLINoStickyStateOutsideAllowList below.)
func TestCLIHermeticity(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, rest.Prefix) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{{"type": "text", "text": "[]"}},
			"isError": false,
		})
	}))
	defer srv.Close()

	t.Setenv("SPYDER_DAEMON_URL", srv.URL)

	// Exercise postTool with a representative sample of read tools.
	// If any of them leak a sticky-state file, the asserter below
	// catches it regardless of which tool wrote it.
	for _, tool := range []string{"devices", "resolve", "device_state", "list_apps", "reservations"} {
		ctx := context.Background()
		if _, err := postTool(ctx, tool, map[string]any{"device": "stub", "name": "stub"}); err != nil {
			t.Fatalf("postTool(%q) unexpected error: %v", tool, err)
		}
	}

	spyderDir := filepath.Join(tempHome, ".spyder")
	if _, err := os.Stat(spyderDir); err == nil {
		var found []string
		_ = filepath.Walk(spyderDir, func(p string, info os.FileInfo, _ error) error {
			if info != nil && !info.IsDir() {
				rel, _ := filepath.Rel(tempHome, p)
				found = append(found, rel)
			}
			return nil
		})
		t.Fatalf("proxy CLI invocations created state files under %s: %v", spyderDir, found)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", spyderDir, err)
	}
}

// TestCLINoStickyStateOutsideAllowList scans the CLI source files and
// confirms paths.Base / paths.RunsBase / paths.BaselinesBase /
// paths.InventoryPath references appear ONLY inside the documented
// allow-list of functions. This catches a future "spyder use <device>"
// or similar sticky-selector regression at compile-test time, before
// any runtime test could observe it.
//
// Allow-list:
//   - autoStartDaemon (cli.go) — writes daemon.log when spawning a
//     detached daemon. Documented exception.
//   - runCmd (main.go) — `spyder run` is the daemonless wrapper that
//     directly manages a reservations.json + runs/ store. Documented
//     exception.
func TestCLINoStickyStateOutsideAllowList(t *testing.T) {
	allowed := map[string]bool{
		"autoStartDaemon": true,
		"runCmd":          true,
	}
	files := []string{"cli.go", "cli_visual.go", "main.go"}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		text := string(raw)
		// Slice the file into top-level `func Name(...)` blocks via a
		// dumb but adequate brace-counter so we can attribute each
		// `paths.` reference to its enclosing function.
		funcs := splitGoFuncs(text)
		for fname, body := range funcs {
			if allowed[fname] {
				continue
			}
			for _, key := range []string{"paths.Base(", "paths.RunsBase(", "paths.BaselinesBase(", "paths.InventoryPath("} {
				if strings.Contains(body, key) {
					t.Errorf("%s: %s references %s — add to TestCLINoStickyStateOutsideAllowList allow-list if intentional",
						file, fname, key)
				}
			}
		}
	}
}

// splitGoFuncs returns a map from function name to the source text of
// its body for top-level `func Name(...) {...}` declarations. Method
// receivers are recorded under "Receiver.Name". Anonymous closures
// stay attributed to the enclosing function. Crude but sufficient for
// the hermeticity allow-list check above — no AST dependency.
func splitGoFuncs(text string) map[string]string {
	out := map[string]string{}
	for i := 0; i < len(text); {
		idx := strings.Index(text[i:], "\nfunc ")
		if idx < 0 {
			break
		}
		i += idx + 1 // skip the leading \n
		// Parse `func [(recv)] Name`.
		lineEnd := strings.Index(text[i:], "{")
		if lineEnd < 0 {
			break
		}
		header := text[i : i+lineEnd]
		name := parseFuncName(header)
		// Find matching close brace.
		body, end := captureBracedBlock(text[i+lineEnd:])
		out[name] = body
		i += lineEnd + end
	}
	return out
}

// parseFuncName extracts "Name" from "func Name(" or "Recv.Name" from
// "func (r *Recv) Name(". Falls back to the raw header on parse failure
// so the test points at the offending text.
func parseFuncName(header string) string {
	h := strings.TrimPrefix(strings.TrimSpace(header), "func")
	h = strings.TrimSpace(h)
	// Method: starts with "(...)".
	if strings.HasPrefix(h, "(") {
		close := strings.Index(h, ")")
		if close < 0 {
			return header
		}
		recvBlock := h[1:close]
		fields := strings.Fields(recvBlock)
		recvType := fields[len(fields)-1]
		recvType = strings.TrimPrefix(recvType, "*")
		rest := strings.TrimSpace(h[close+1:])
		paren := strings.Index(rest, "(")
		if paren < 0 {
			return header
		}
		return recvType + "." + strings.TrimSpace(rest[:paren])
	}
	paren := strings.Index(h, "(")
	if paren < 0 {
		return header
	}
	return strings.TrimSpace(h[:paren])
}

// captureBracedBlock returns the text inside the outermost {...} block
// at the start of s, plus the index just after the closing brace.
// Naive — does not handle braces inside string literals or comments.
// For Go source this matters in pathological cases only; the source
// files this test scans are normal CLI handlers, not generated code.
func captureBracedBlock(s string) (body string, end int) {
	if len(s) == 0 || s[0] != '{' {
		return "", 0
	}
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[1:i], i + 1
			}
		}
	}
	return s[1:], len(s)
}

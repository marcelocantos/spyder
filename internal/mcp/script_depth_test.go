// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

// DH-02: run_script re-enters handleAppExecContext with a fresh Starlark
// thread and the same verb table. Nesting has to stop well before the
// goroutine stack does.
func TestRunScriptNestingIsBounded(t *testing.T) {
	h := NewHandler()
	script := filepath.Join(t.TempDir(), "self.star")
	src := fmt.Sprintf(`n = int(params.get("n", "0"))
if n < 2000:
    run_script(path=%q, params={"n": str(n + 1)})
else:
    fail("reached depth %%d" %% n)
`, script)
	if err := os.WriteFile(script, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := h.handleAppExecContext(context.Background(), map[string]any{
		"script": fmt.Sprintf("run_script(path=%q)\n", script),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(texts(res), "\n")
	if strings.Contains(text, "reached depth 2000") {
		t.Fatalf("run_script nested 2000 levels: %s", text)
	}
	if !res.IsError || !strings.Contains(text, "nesting exceeds") {
		t.Fatalf("deep run_script did not stop at the nesting limit: error=%v text=%s", res.IsError, text)
	}
}

func TestRunScriptOneLevelStillRuns(t *testing.T) {
	h := NewHandler()
	inner := filepath.Join(t.TempDir(), "inner.star")
	if err := os.WriteFile(inner, []byte("emit(params.get(\"k\", \"\"))\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := h.handleAppExecContext(context.Background(), map[string]any{
		"script": fmt.Sprintf("run_script(path=%q, params={\"k\": \"ok\"})\n", inner),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(texts(res), "\n")
	if res.IsError || !strings.Contains(text, "ok") {
		t.Fatalf("one nested run_script failed: error=%v text=%s", res.IsError, text)
	}
}

func TestRunScriptRecursionDoesNotKillProcess(t *testing.T) {
	if os.Getenv("SPYDER_DH02_CHILD") == "1" {
		debug.SetMaxStack(4 << 20)
		h := NewHandler()
		script := filepath.Join(t.TempDir(), "self.star")
		if err := os.WriteFile(script, []byte(fmt.Sprintf("run_script(path=%q)\n", script)), 0o600); err != nil {
			t.Fatal(err)
		}
		res, err := h.handleAppExecContext(context.Background(), map[string]any{
			"script": fmt.Sprintf("run_script(path=%q)\n", script),
		})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Fatalf("unbounded run_script returned success: %s", strings.Join(texts(res), "\n"))
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunScriptRecursionDoesNotKillProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), "SPYDER_DH02_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil || strings.Contains(string(out), "stack exceeds") {
		t.Fatalf("one app_exec request crashed the process (%v):\n%s", err, out)
	}
}

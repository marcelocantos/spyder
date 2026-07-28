// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	return buf.String()
}

func sampleJSONResult(text string) *toolResultContent {
	r := &toolResultContent{}
	r.Content = append(r.Content, struct {
		Type     string `json:"type"`
		Text     string `json:"text,omitempty"`
		Data     string `json:"data,omitempty"`
		MIMEType string `json:"mimeType,omitempty"`
	}{Type: "text", Text: text})
	return r
}

// OS-control CLI (perf-fps, port-forward, app-perf-get) must print
// success bodies even without -v so --json is usable by scripts.
func TestRenderResult_JSONSuccessPrintsWhenNotQuiet(t *testing.T) {
	payload := `{"local_port":18080,"host_url":"127.0.0.1:18080"}`
	out := captureStdout(t, func() {
		renderResult(sampleJSONResult(payload), true, false)
	})
	if !strings.Contains(out, "18080") || !strings.Contains(out, "host_url") {
		t.Fatalf("expected JSON body on stdout, got %q", out)
	}
}

func TestRenderResult_QuietSwallowsSuccessBody(t *testing.T) {
	// Documents the footgun: quietOnSuccess=true skips printing even in
	// jsonMode. Data tools must pass quiet=false.
	payload := `{"fps":60}`
	out := captureStdout(t, func() {
		renderResult(sampleJSONResult(payload), true, true)
	})
	if strings.Contains(out, "fps") {
		t.Fatalf("quiet should suppress body, got %q", out)
	}
}

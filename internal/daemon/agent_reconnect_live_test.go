// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/agent"
)

// Property: after a missed Apple automation prompt, a physical USB
// reconnect alone enables a fresh prompt and an owned, answering runner.
// Run a tree-built daemon on :3131 with isolated SPYDER_DATA_DIR and the
// DeviceKit backend, stopping the daily daemon first to avoid competing
// runners. No agent_ensure/retry or synthetic tether event is used here.
// The owner ignores the first prompt, then replugs and approves when the
// test prints REPLUG. The daemon and go-ios provider stay live throughout.
func TestAgentReconnectAfterMissedPrompt_Live(t *testing.T) {
	if os.Getenv("SPYDER_AGENT_RECONNECT_LIVE") != "1" {
		t.Skip("make test-live-agent-reconnect runs the attended iOS reconnect journey")
	}
	udid := os.Getenv("SPYDER_LIVE_UDID")
	logPath := os.Getenv("SPYDER_AGENT_RECONNECT_LOG")
	if udid == "" || logPath == "" {
		t.Fatal("SPYDER_LIVE_UDID and SPYDER_AGENT_RECONNECT_LOG are required")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	payload, err := json.Marshal(map[string]string{"device": udid})
	if err != nil {
		t.Fatal(err)
	}
	status := func() (agent.Snapshot, error) {
		var snap agent.Snapshot
		resp, err := client.Post("http://127.0.0.1:3131/api/v1/agent_status", "application/json", bytes.NewReader(payload))
		if err != nil {
			return snap, err
		}
		defer resp.Body.Close()
		var result struct {
			IsError bool                          `json:"isError"`
			Content []struct{ Type, Text string } `json:"content"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&result); err != nil {
			return snap, err
		}
		if resp.StatusCode != http.StatusOK || result.IsError || len(result.Content) != 1 || result.Content[0].Type != "text" {
			return snap, fmt.Errorf("agent_status failed: HTTP %d %+v", resp.StatusCode, result)
		}
		err = json.Unmarshal([]byte(result.Content[0].Text), &snap)
		return snap, err
	}
	wait := func(label string, accept func(agent.Snapshot) bool) agent.Snapshot {
		t.Helper()
		deadline := time.Now().Add(5 * time.Minute)
		var last agent.Snapshot
		var lastErr error
		for time.Now().Before(deadline) {
			last, lastErr = status()
			if lastErr == nil && accept(last) {
				return last
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("%s timed out: %+v err=%v", label, last, lastErr)
		return last
	}
	t.Log("IGNORE the first automation prompt; waiting for a real provider consent timeout")
	missed := wait("missed prompt", func(s agent.Snapshot) bool {
		return s.Condition == agent.CondConsentRequired && s.Attempts > 0 &&
			(strings.Contains(s.LastExit, "enabling automation") || strings.Contains(s.LastExit, "consent window"))
	})
	if missed.Ready || missed.ConsentPending || missed.Consent != nil {
		t.Fatalf("expired prompt is still actionable: %+v", missed)
	}
	before, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("MISSED_PROMPT_CONFIRMED: %+v", missed)
	t.Log("REPLUG the iPad USB connection now, then approve the new automation prompt")
	ready := wait("automatic reconnect recovery", func(s agent.Snapshot) bool {
		return s.Ready && s.RunnerHealthy && s.HostPID > 0 && s.Runner.PID > 0 &&
			s.Attempts == 0 && !s.ConsentPending && s.Consent == nil
	})
	after, err := os.ReadFile(logPath)
	if err != nil || len(after) < len(before) {
		t.Fatalf("read daemon reconnect evidence: %v", err)
	}
	reconnect := "agent USB reconnect: fresh attempt budget\" device=" + udid
	// slog's text handler quotes this message because it contains spaces.
	if !strings.Contains(string(after[len(before):]), reconnect) &&
		!strings.Contains(string(after[len(before):]), "agent USB reconnect: fresh attempt budget device="+udid) {
		t.Fatal("ready without evidence of a real USB reconnect on this daemon")
	}
	t.Logf("AUTOMATIC_RECONNECT_READY: %+v", ready)
}

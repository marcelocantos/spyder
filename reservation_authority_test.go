// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/daemon"
	"github.com/marcelocantos/spyder/internal/paths"
	"github.com/marcelocantos/spyder/internal/runs"
)

func TestMain(m *testing.M) {
	switch os.Getenv("SPYDER_RESV_HELPER") {
	case "daemon":
		os.Exit(helperDaemon())
	case "run":
		runWithReservation(strings.Split(os.Getenv("SPYDER_RESV_ARGS"), "\x1f"), func() {
			dir := os.Getenv("SPYDER_TEST_RESV_GATE")
			if dir == "" {
				return
			}
			if err := os.WriteFile(filepath.Join(dir, "ready"), nil, 0o600); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(filepath.Join(dir, "go")); err == nil {
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
			fmt.Fprintln(os.Stderr, "reservation gate timed out")
			os.Exit(1)
		})
	default:
		os.Exit(m.Run())
	}
}

// helperDaemon is the worktree daemon's HTTP server on a free port.
// It does not start the tunnel, the adb supervisor, or the battery
// sampler; those live in daemon.Run and talk to real devices.
func helperDaemon() int {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	mux, _, _, _, _ := daemon.Build(daemon.Config{Addr: "127.0.0.1:0", Version: "test"})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		return 1
	}
	fmt.Printf("ADDR %s\n", ln.Addr().String())
	_ = os.Stdout.Sync()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Supply the fake selector's single candidate without querying the lab.
		if r.URL.Path == "/api/v1/reserve" {
			var args map[string]any
			if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if _, ok := args["selector"]; ok {
				delete(args, "selector")
				args["device"] = "fake-dev"
			}
			body, err := json.Marshal(args)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		mux.ServeHTTP(w, r)
	})
	if err := http.Serve(ln, handler); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}
	return 0
}

type daemonProc struct {
	cmd    *exec.Cmd
	base   string
	stderr *bytes.Buffer
}

func startTestDaemon(t *testing.T, data string) *daemonProc {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = replaceEnv(os.Environ(), map[string]string{
		"SPYDER_RESV_HELPER": "daemon",
		paths.DataDirEnv:     data,
		"HOME":               data,
		"SPYDER_ADDR":        "",
	})
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	d := &daemonProc{cmd: cmd, stderr: &stderr}
	t.Cleanup(func() { stopProc(d.cmd) })
	line := readReady(t, stdout, &stderr)
	if !strings.HasPrefix(line, "ADDR ") {
		t.Fatalf("daemon ready %q; stderr: %s", line, stderr.String())
	}
	d.base = "http://" + strings.TrimPrefix(line, "ADDR ")
	return d
}

func startTestRun(t *testing.T, data, gate string, args []string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	env := map[string]string{
		"SPYDER_RESV_HELPER": "run",
		"SPYDER_RESV_ARGS":   strings.Join(args, "\x1f"),
		paths.DataDirEnv:     data,
		"HOME":               data,
		"SPYDER_ADDR":        "",
	}
	if gate != "" {
		env["SPYDER_TEST_RESV_GATE"] = gate
	}
	cmd.Env = replaceEnv(os.Environ(), env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopProc(cmd) })
	return cmd
}

func replaceEnv(base []string, set map[string]string) []string {
	skip := make(map[string]bool, len(set))
	for k := range set {
		skip[k+"="] = true
	}
	out := make([]string, 0, len(base)+len(set))
	for _, e := range base {
		drop := false
		for prefix := range skip {
			if strings.HasPrefix(e, prefix) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, e)
		}
	}
	for k, v := range set {
		if v == "" {
			continue
		}
		out = append(out, k+"="+v)
	}
	return out
}

func stopProc(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Wait()
	cmd.Process = nil
}

func readReady(t *testing.T, r io.Reader, stderr *bytes.Buffer) string {
	t.Helper()
	ch := make(chan string, 1)
	go func() {
		line, err := bufio.NewReader(r).ReadString('\n')
		if err != nil {
			ch <- ""
			return
		}
		ch <- strings.TrimSpace(line)
	}()
	select {
	case <-time.After(20 * time.Second):
		t.Fatalf("daemon did not listen; stderr: %s", stderr.String())
	case line := <-ch:
		if line == "" {
			t.Fatalf("daemon exited before listening; stderr: %s", stderr.String())
		}
		return line
	}
	return ""
}

func waitGate(t *testing.T, gate string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(gate, "ready")); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("spyder run did not reach the reservation gate")
}

func waitOwner(t *testing.T, data, owner string) {
	t.Helper()
	path := filepath.Join(data, "reservations.json")
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(body), owner) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	body, _ := os.ReadFile(path)
	t.Fatalf("book never named %s: %s", owner, body)
}

type reserveResult struct {
	isError bool
	text    string
}

func postReserve(t *testing.T, base, device, owner string) reserveResult {
	t.Helper()
	body := fmt.Sprintf(`{"device":%q,"owner":%q,"note":"authority-test"}`, device, owner)
	var last error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Post(base+"/api/v1/reserve", "application/json", strings.NewReader(body))
		if err != nil {
			last = err
			time.Sleep(20 * time.Millisecond)
			continue
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var parsed struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Fatalf("reserve response %s: %v", raw, err)
		}
		text := ""
		if len(parsed.Content) > 0 {
			text = parsed.Content[0].Text
		}
		return reserveResult{isError: parsed.IsError, text: text}
	}
	t.Fatalf("reserve: %v", last)
	return reserveResult{}
}

func exitCode(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()
	err := cmd.Wait()
	cmd.Process = nil
	if err == nil {
		return 0
	}
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("run: %v; stderr %s", err, cmd.Stderr.(*bytes.Buffer).String())
	}
	return ee.ExitCode()
}

func TestDaemonHoldsRunAsks(t *testing.T) {
	data := t.TempDir()
	d := startTestDaemon(t, data)
	gate := t.TempDir()
	run := startTestRun(t, data, gate, []string{"--device", "fake-dev", "--as", "run-job", "--", "/usr/bin/true"})
	waitGate(t, gate)
	got := postReserve(t, d.base, "fake-dev", "daemon-job")
	if got.isError {
		t.Fatalf("daemon reserve: %s", got.text)
	}
	if err := os.WriteFile(filepath.Join(gate, "go"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := exitCode(t, run); code != 3 {
		t.Fatalf("spyder run exit %d, want 3 (conflict); stderr %s", code, run.Stderr.(*bytes.Buffer).String())
	}
}

func TestRunHoldsDaemonAsks(t *testing.T) {
	data := t.TempDir()
	d := startTestDaemon(t, data)
	run := startTestRun(t, data, "", []string{"--device", "fake-dev", "--as", "run-job", "--", "/bin/sleep", "30"})
	waitOwner(t, data, "run-job")
	got := postReserve(t, d.base, "fake-dev", "daemon-job")
	if !got.isError {
		t.Fatalf("daemon granted fake-dev while spyder run holds it: %s", got.text)
	}
	stopProc(run)
}

func TestKilledWhileHolding(t *testing.T) {
	data := t.TempDir()
	d := startTestDaemon(t, data)
	run := startTestRun(t, data, "", []string{"--device", "fake-dev", "--as", "run-job", "--", "/bin/sleep", "30"})
	waitOwner(t, data, "run-job")
	stopProc(run)
	got := postReserve(t, d.base, "fake-dev", "daemon-job")
	if !got.isError {
		t.Fatalf("after spyder run was killed, the daemon granted fake-dev: %s", got.text)
	}
	body, err := os.ReadFile(filepath.Join(data, "reservations.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "run-job") {
		t.Fatalf("book lost the killed holder's lease: %s", body)
	}
}

func TestBothStartAtOnce(t *testing.T) {
	data := t.TempDir()
	d := startTestDaemon(t, data)
	gate := t.TempDir()
	releaseFile := filepath.Join(gate, "release")
	run := startTestRun(t, data, gate, []string{"--device", "fake-dev", "--as", "run-job", "--",
		"/bin/sh", "-c", `while [ ! -f "$1" ]; do /bin/sleep 0.1; done`, "test", releaseFile})
	waitGate(t, gate)
	errCh := make(chan error, 1)
	go func() {
		errCh <- os.WriteFile(filepath.Join(gate, "go"), []byte("1"), 0o600)
	}()
	got := postReserve(t, d.base, "fake-dev", "daemon-job")
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(releaseFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code := exitCode(t, run)
	runWon := code == 0
	daemonWon := !got.isError
	if runWon == daemonWon {
		t.Fatalf("want exactly one holder; run exit %d daemon error=%v %s; stderr %s",
			code, got.isError, got.text, run.Stderr.(*bytes.Buffer).String())
	}
	if code != 0 && code != 3 {
		t.Fatalf("spyder run exit %d; stderr %s", code, run.Stderr.(*bytes.Buffer).String())
	}
}

func TestRunReleaseReachesDaemon(t *testing.T) {
	data := t.TempDir()
	d := startTestDaemon(t, data)
	first := postReserve(t, d.base, "fake-dev", "job-A")
	if first.isError {
		t.Fatalf("daemon reserve: %s", first.text)
	}
	run := startTestRun(t, data, "", []string{"--device", "fake-dev", "--as", "job-A", "--", "/usr/bin/true"})
	if code := exitCode(t, run); code != 0 {
		t.Fatalf("spyder run exit %d; stderr %s", code, run.Stderr.(*bytes.Buffer).String())
	}
	next := postReserve(t, d.base, "fake-dev", "job-B")
	if next.isError {
		t.Fatalf("daemon still holds fake-dev after spyder run released: %s", next.text)
	}
}

func TestRunOnClosesDaemonLeaseAndManifest(t *testing.T) {
	for _, sharedState := range []bool{true, false} {
		name := "separate state"
		if sharedState {
			name = "shared state"
		}
		t.Run(name, func(t *testing.T) {
			daemonData := t.TempDir()
			runData := daemonData
			if !sharedState {
				runData = t.TempDir()
			}
			d := startTestDaemon(t, daemonData)
			t.Setenv(daemonURLEnv, d.base)
			run := startTestRun(t, runData, "", []string{"--on", "platform=ios", "--as", "job-A", "--", "/usr/bin/true"})
			if code := exitCode(t, run); code != 0 {
				t.Fatalf("spyder run exit %d; stderr %s", code, run.Stderr.(*bytes.Buffer).String())
			}
			s, err := runs.New(filepath.Join(daemonData, "runs"))
			if err != nil {
				t.Fatal(err)
			}
			list, err := s.List()
			if err != nil {
				t.Fatal(err)
			}
			if len(list) != 1 || list[0].ClosedAt == nil {
				t.Errorf("daemon has %d manifests, want one closed manifest: %+v", len(list), list)
			}
			if got := postReserve(t, d.base, "fake-dev", "job-B"); got.isError {
				t.Errorf("daemon still holds the finished selector lease: %s", got.text)
			}
			if !sharedState {
				if _, err := os.Stat(filepath.Join(runData, "reservations.json")); !os.IsNotExist(err) {
					t.Errorf("independent CLI wrote its own reservation book: %v", err)
				}
			}
		})
	}
}

func TestDaemonStartsAndRestartsDuringStandaloneRun(t *testing.T) {
	data := t.TempDir()
	releaseFile := filepath.Join(t.TempDir(), "release")
	run := startTestRun(t, data, "", []string{"--device", "fake-dev", "--as", "standalone-job", "--",
		"/bin/sh", "-c", `while [ ! -f "$1" ]; do /bin/sleep 0.1; done`, "test", releaseFile})
	waitOwner(t, data, "standalone-job")
	d := startTestDaemon(t, data)
	if got := postReserve(t, d.base, "fake-dev", "other"); !got.isError || !strings.Contains(got.text, "standalone-job") {
		t.Fatalf("daemon startup ignored standalone holder: %+v", got)
	}
	stopProc(d.cmd)
	if err := os.WriteFile(releaseFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := exitCode(t, run); code != 0 {
		t.Fatalf("standalone run exit %d; stderr %s", code, run.Stderr.(*bytes.Buffer).String())
	}
	d = startTestDaemon(t, data)
	if got := postReserve(t, d.base, "fake-dev", "other"); got.isError {
		t.Fatalf("standalone run's lease survived its normal exit: %s", got.text)
	}
}

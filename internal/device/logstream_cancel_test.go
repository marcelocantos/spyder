// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// writeFlushingLogcat installs a stand-in for adb. A shell printf is fully
// buffered on a pipe, and a freshly compiled binary can sit in the macOS
// scan queue while the rest of the suite is launching, so neither one
// delivers a line before the test gives up. System Python is already
// scanned; -u makes its stdout unbuffered.
func writeFlushingLogcat(t *testing.T, dir string) string {
	t.Helper()
	python := "/usr/bin/python3"
	if _, err := os.Stat(python); err != nil {
		t.Fatal("need /usr/bin/python3 for the logcat stand-in")
	}
	program := fmt.Sprintf(`import os, sys, time
serial = ""
args = sys.argv[1:]
for i, arg in enumerate(args):
    if arg == "-s" and i + 1 < len(args):
        serial = args[i + 1]
if serial:
    with open(os.path.join(%q, serial + ".pid"), "w") as fh:
        fh.write(str(os.getpid()))
        fh.flush()
sys.stdout.write("04-15 14:23:01.123  1  1 I Owned: hello\n")
sys.stdout.flush()
while True:
    time.sleep(3600)
`, dir)
	src := filepath.Join(dir, "fake_logcat.py")
	if err := os.WriteFile(src, []byte(program), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "logcat")
	script := fmt.Sprintf("#!/bin/sh\nexec %s -u %q \"$@\"\n", python, src)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestLogStreamCancelReapsOwnedReaderOnly(t *testing.T) {
	dir := t.TempDir()
	script := writeFlushingLogcat(t, dir)
	start := func(serial string) (context.CancelFunc, <-chan error, <-chan LogLine) {
		ctx, cancel := context.WithCancel(context.Background())
		out := make(chan LogLine, 4)
		errCh := make(chan error, 1)
		adapter := &AndroidAdapter{logcatBin: script}
		go func() {
			errCh <- adapter.LogStream(ctx, serial, LogFilter{}, out)
		}()
		return cancel, errCh, out
	}
	waitLine := func(serial string, errCh <-chan error, out <-chan LogLine) LogLine {
		t.Helper()
		select {
		case ll := <-out:
			return ll
		case err := <-errCh:
			t.Fatalf("reader %s returned before a line: %v", serial, err)
		case <-time.After(15 * time.Second):
			pid := "missing"
			if raw, err := os.ReadFile(filepath.Join(dir, serial+".pid")); err == nil {
				pid = string(raw)
			}
			t.Fatalf("reader %s produced no line (pid %s)", serial, pid)
		}
		return LogLine{}
	}
	pidAlive := func(serial string) bool {
		raw, err := os.ReadFile(filepath.Join(dir, serial+".pid"))
		if err != nil {
			return false
		}
		var pid int
		if _, err := fmt.Sscan(string(raw), &pid); err != nil || pid <= 0 {
			return false
		}
		err = syscall.Kill(pid, 0)
		return err == nil
	}

	cancelA, errA, outA := start("dev-a")
	line := waitLine("dev-a", errA, outA)
	if line.Diagnostic == nil || line.Diagnostic.SchemaVersion != DiagnosticSchemaVersion {
		t.Fatalf("stream line missing schema: %+v", line.Diagnostic)
	}
	if line.Diagnostic.DeviceTime.Source == line.Diagnostic.HostReceipt.Source {
		t.Fatalf("stream collapsed clocks: %+v %+v", line.Diagnostic.DeviceTime, line.Diagnostic.HostReceipt)
	}
	cancelB, errB, outB := start("dev-b")
	_ = waitLine("dev-b", errB, outB)
	if !pidAlive("dev-a") || !pidAlive("dev-b") {
		t.Fatal("both readers should be alive before cancellation")
	}
	cancelA()
	select {
	case err := <-errA:
		if err != nil {
			t.Fatalf("cancelled reader: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled reader did not return")
	}
	deadline := time.Now().Add(2 * time.Second)
	for pidAlive("dev-a") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if pidAlive("dev-a") {
		t.Fatal("cancelled device reader is still running")
	}
	if !pidAlive("dev-b") {
		t.Fatal("cancelling dev-a reaped dev-b")
	}
	cancelB()
	select {
	case err := <-errB:
		if err != nil {
			t.Fatalf("second reader: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second reader did not return")
	}
}

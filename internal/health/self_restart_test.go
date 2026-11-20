// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/paths"
)

func TestSelfRestartLimiter_RateLimits(t *testing.T) {
	exits := 0
	l := NewSelfRestartLimiterForTest(2, time.Hour, nil)
	l.exitFn = func(int) { exits++ }
	now := time.Now()
	l.now = func() time.Time { return now }

	if !l.Request("wedge-1") {
		t.Fatal("first request should allow")
	}
	if !l.Request("wedge-2") {
		t.Fatal("second request should allow")
	}
	if l.Request("wedge-3") {
		t.Fatal("third request should be rate-limited")
	}
	if exits != 2 {
		t.Fatalf("exits=%d want 2", exits)
	}
}

func TestSelfRestartLimiter_BudgetSurvivesProcessExit(t *testing.T) {
	const clockEnv = "SPYDER_SELF_RESTART_TEST_CLOCK"
	if value := os.Getenv(clockEnv); value != "" {
		unix, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		l := NewSelfRestartLimiter(3, time.Hour)
		l.now = func() time.Time { return time.Unix(unix, 0) }
		if l.Request("fake stuck dispatch") {
			t.Fatal("allowed restart returned to the caller")
		}
		return
	}
	t.Setenv(paths.DataDirEnv, t.TempDir())
	at := time.Now().Unix()
	for i, offset := range []time.Duration{0, time.Minute, 2 * time.Minute, 3 * time.Minute, time.Hour} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSelfRestartLimiter_BudgetSurvivesProcessExit$")
		cmd.Env = append(os.Environ(), clockEnv+"="+strconv.FormatInt(at+int64(offset/time.Second), 10))
		out, err := cmd.CombinedOutput()
		if i == 3 {
			if err != nil {
				t.Fatalf("fourth lifetime exceeded the budget (%v): %s", err, out)
			}
			continue
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(out), "exiting for supervised relaunch") {
			t.Fatalf("lifetime %d did not restart (%v): %s", i+1, err, out)
		}
	}
}

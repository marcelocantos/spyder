// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/runs"
)

func TestRunsPolicyFromEnv(t *testing.T) {
	cases := []struct {
		days, gb, quiet string
		want            runs.Policy
	}{
		{"", "", "", runs.Policy{MaxAge: 30 * 24 * time.Hour, MaxSize: 20 << 30, Quiet: runs.DefaultQuiet}},
		{"0", "0", "", runs.Policy{Quiet: runs.DefaultQuiet}}, // stopgap: both bounds disabled
		{"2", "0.5", "0", runs.Policy{MaxAge: 48 * time.Hour, MaxSize: 512 << 20}},
	}
	for _, c := range cases {
		t.Setenv("SPYDER_RUNS_MAX_AGE_DAYS", c.days)
		t.Setenv("SPYDER_RUNS_MAX_SIZE_GB", c.gb)
		t.Setenv("SPYDER_RUNS_ARCHIVE_QUIET_MINUTES", c.quiet)
		if got := runsPolicyFromEnv(); got != c.want {
			t.Errorf("days=%q gb=%q quiet=%q: got %+v want %+v", c.days, c.gb, c.quiet, got, c.want)
		}
	}
}

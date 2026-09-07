package mcp

import (
	"regexp"
	"testing"
	"time"
)

// durationLiteral matches the human duration tokens an agent reads out of the
// tool description: "30s", "120s", "10 min", "600000".
var durationLiteral = regexp.MustCompile(`\b\d+\s*(?:minutes?|seconds?|min|ms|s|m)?\b`)

// TestAppExecDefinition_DurationCapMatchesDescription pins the app_exec tool
// description to the wall-clock constants it describes. The description is the
// only schema an agent sees (🎯T88), so a stale ceiling there makes agents
// split scripts that the server would have run.
func TestAppExecDefinition_DurationCapMatchesDescription(t *testing.T) {
	allowed := map[string]bool{
		durationHuman(defaultExecDuration):  true,
		durationHuman(maxExecDuration):      true,
		durationMillis(defaultExecDuration): true,
		durationMillis(maxExecDuration):     true,
		// Deterministic-capture recipe in the same description.
		"1": true,
	}

	def := appExecDefinition()
	texts := []string{def.Description}
	for name, prop := range def.InputSchema.Properties {
		m, ok := prop.(map[string]any)
		if !ok {
			continue
		}
		desc, ok := m["description"].(string)
		if !ok {
			continue
		}
		texts = append(texts, name+": "+desc)
	}

	for _, text := range texts {
		for _, lit := range durationLiteral.FindAllString(text, -1) {
			if !allowed[lit] {
				t.Errorf("app_exec description mentions %q, which is not derived from "+
					"defaultExecDuration (%s) or maxExecDuration (%s): %s",
					lit, defaultExecDuration, maxExecDuration, text)
			}
		}
	}
}

func TestDurationHelpers(t *testing.T) {
	if got := durationHuman(30 * time.Second); got != "30s" {
		t.Errorf("durationHuman(30s) = %q", got)
	}
	if got := durationMillis(10 * time.Minute); got != "600000" {
		t.Errorf("durationMillis(10m) = %q", got)
	}
}

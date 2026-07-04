// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package gedharness

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Study renders a side-by-side triage view for ONE case: raw golden and
// raw candidate (pretty-printed), followed by the normalized-diff summary.
// It is for humans eyeballing why a case diverges, not for machine use.
func Study(golden, candidate json.RawMessage, rules []Rule) string {
	var b strings.Builder

	b.WriteString("== GOLDEN (raw) ==\n")
	b.WriteString(pretty(golden))
	b.WriteString("\n\n== CANDIDATE (raw) ==\n")
	b.WriteString(pretty(candidate))
	b.WriteString("\n\n== NORMALIZED DIFF ==\n")

	rep, err := DiffJSON(golden, candidate, rules)
	if err != nil {
		fmt.Fprintf(&b, "diff error: %v\n", err)
		return b.String()
	}
	b.WriteString(rep.String())
	if !strings.HasSuffix(b.String(), "\n") {
		b.WriteByte('\n')
	}
	return b.String()
}

// pretty indents raw JSON for display, falling back to the raw bytes when
// the input isn't valid JSON (so study never hides malformed data).
func pretty(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "(empty)"
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(out)
}

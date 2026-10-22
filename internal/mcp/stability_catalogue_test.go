package mcp

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// ENT-003 / 🎯T132.3: STABILITY.md must not catalogue per-verb MCP tools on the wire.
func TestSTABILITY_MCPToolsSectionListsOnlyAppExec(t *testing.T) {
	data, err := os.ReadFile("../../STABILITY.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "### REST / Starlark verbs") {
		t.Fatal("STABILITY.md missing ### REST / Starlark verbs section")
	}

	const heading = "### MCP tools"
	start := strings.Index(text, heading)
	if start < 0 {
		t.Fatal("STABILITY.md missing ### MCP tools section")
	}
	rest := text[start+len(heading):]
	nextH3 := regexp.MustCompile(`\n### `).FindStringIndex(rest)
	section := rest
	if nextH3 != nil {
		section = rest[:nextH3[0]]
	}

	row := regexp.MustCompile(`^\| ` + "`" + `([^` + "`" + `]+)` + "`" + ` \|`)
	var names []string
	for _, line := range strings.Split(section, "\n") {
		if m := row.FindStringSubmatch(line); m != nil {
			names = append(names, m[1])
		}
	}
	if len(names) == 0 {
		t.Fatal("### MCP tools section has no tool rows")
	}
	for _, name := range names {
		if name != "app_exec" {
			t.Errorf("MCP tools table must list only app_exec, found %q", name)
		}
	}
}

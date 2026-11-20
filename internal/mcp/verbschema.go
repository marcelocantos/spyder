// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"sort"
	"time"
)

// VerbParam is one parameter of a verb, as its schema declares it.
type VerbParam struct {
	Name        string
	Type        string // string | number | integer | boolean | array | object
	ItemType    string // element type when Type is array
	Description string
	Required    bool
}

// VerbSchema is one verb of the daemon's verb table: the operation MCP
// reaches as an app_exec builtin, REST as POST /api/v1/<name>, and the
// CLI as `spyder <name>` (🎯T198). All three read this one schema.
type VerbSchema struct {
	Name        string
	Description string
	Params      []VerbParam // sorted by name
	// Deadline is the daemon's dispatch budget for the verb; the CLI
	// sizes its request timeout from it.
	Deadline time.Duration
}

// VerbSchemas returns every verb with its parameters, sorted by name.
// It covers the whole verb table: TestExec_BuiltinCoversEveryAdvertisedVerb
// keeps legacyDefinitions and toolHandlers in step, and app_exec is
// added here.
func VerbSchemas() []VerbSchema {
	var out []VerbSchema
	for _, tool := range append(legacyDefinitions(), appExecDefinition()) {
		required := map[string]bool{}
		for _, r := range tool.InputSchema.Required {
			required[r] = true
		}
		v := VerbSchema{
			Name:        tool.Name,
			Description: tool.Description,
			Deadline:    toolDeadlineClass(tool.Name),
		}
		for name, raw := range tool.InputSchema.Properties {
			p := VerbParam{Name: name, Required: required[name]}
			if m, ok := raw.(map[string]any); ok {
				p.Type, _ = m["type"].(string)
				p.Description, _ = m["description"].(string)
				if items, ok := m["items"].(map[string]any); ok {
					p.ItemType, _ = items["type"].(string)
				}
			}
			v.Params = append(v.Params, p)
		}
		sort.Slice(v.Params, func(i, j int) bool { return v.Params[i].Name < v.Params[j].Name })
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

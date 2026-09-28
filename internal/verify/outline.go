// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"sort"
	"strconv"
)

// CleanupGroupID is the dashboard section that holds cleanup steps. It is a
// view-only group; the parentheses keep it apart from workflow group IDs.
const CleanupGroupID = "(cleanup)"

// Outline gives every group and step a dotted number ("2.3.1") by its place
// in the dashboard tree, so owners and agents can name an item briefly.
// Within a group, subgroups and steps are ordered by where they first appear
// in the step list; cleanup is the last top-level section. Numbers depend
// only on the workflow definition, so they are the same for every run of it,
// including runs saved before numbering existed.
type Outline struct {
	Groups map[string]string // group ID -> number
	Steps  map[string]string // step ID -> number
}

func outlineOf(wf *Workflow) Outline {
	out := Outline{Groups: map[string]string{}, Steps: map[string]string{}}
	parent := map[string]string{}
	declared := map[string]int{}
	for i, g := range wf.Groups {
		parent[g.ID] = g.Parent
		declared[g.ID] = i
	}
	// A group sits where its first step (at any depth) sits in the flow.
	// Groups without steps follow every step, in declaration order.
	first := map[string]int{}
	for i, s := range wf.Steps {
		seen := map[string]bool{}
		for g := s.Group; g != "" && !seen[g]; g = parent[g] {
			seen[g] = true
			if _, ok := first[g]; !ok {
				first[g] = i
			}
		}
	}
	type item struct {
		group, step string
		pos         int
	}
	children := map[string][]item{}
	for _, g := range wf.Groups {
		pos, ok := first[g.ID]
		if !ok {
			pos = len(wf.Steps) + declared[g.ID]
		}
		children[g.Parent] = append(children[g.Parent], item{group: g.ID, pos: pos})
	}
	for i, s := range wf.Steps {
		children[s.Group] = append(children[s.Group], item{step: s.ID, pos: i})
	}
	var number func(container, prefix string, visiting map[string]bool) int
	number = func(container, prefix string, visiting map[string]bool) int {
		kids := children[container]
		sort.SliceStable(kids, func(i, j int) bool { return kids[i].pos < kids[j].pos })
		for i, k := range kids {
			n := prefix + strconv.Itoa(i+1)
			if k.step != "" {
				out.Steps[k.step] = n
				continue
			}
			out.Groups[k.group] = n
			if !visiting[k.group] {
				visiting[k.group] = true
				number(k.group, n+".", visiting)
			}
		}
		return len(kids)
	}
	top := number("", "", map[string]bool{})
	if len(wf.Cleanup) > 0 {
		n := strconv.Itoa(top + 1)
		out.Groups[CleanupGroupID] = n
		for i, s := range wf.Cleanup {
			out.Steps[s.ID] = n + "." + strconv.Itoa(i+1)
		}
	}
	return out
}

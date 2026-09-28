// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"testing"
)

func TestOutlineNumbersFollowTheDashboardTree(t *testing.T) {
	wf := loadWF(t, `
name: outline
groups:
  - {id: phone, label: Phone}
  - {id: setup, label: Setup, parent: phone}
  - {id: looks, label: Looks, parent: phone}
  - {id: tablet, label: Tablet}
  - {id: spare, label: Unused}
steps:
  - {id: preflight, type: shell, group: setup, argv: [/usr/bin/true]}
  - {id: deploy, type: shell, group: setup, requires: [preflight], argv: [/usr/bin/true]}
  - {id: early, type: shell, group: phone, argv: [/usr/bin/true]}
  - {id: stage, type: shell, group: looks, argv: [/usr/bin/true]}
  - {id: tablet_deploy, type: shell, group: tablet, argv: [/usr/bin/true]}
  - {id: signoff, type: shell, group: phone, argv: [/usr/bin/true]}
  - {id: loose, type: shell, argv: [/usr/bin/true]}
cleanup:
  - {id: stop, type: shell, argv: [/usr/bin/true]}
  - {id: release, type: shell, argv: [/usr/bin/true]}
`)
	got := outlineOf(wf)
	for id, want := range map[string]string{
		"phone": "1", "setup": "1.1", "looks": "1.3", "tablet": "2", "spare": "4", CleanupGroupID: "5",
	} {
		if got.Groups[id] != want {
			t.Errorf("group %s = %q, want %q", id, got.Groups[id], want)
		}
	}
	for id, want := range map[string]string{
		// Steps and subgroups interleave in flow order: "early" comes after
		// Setup's first step and before Looks.
		"preflight": "1.1.1", "deploy": "1.1.2", "early": "1.2", "stage": "1.3.1",
		"signoff": "1.4", "tablet_deploy": "2.1", "loose": "3", "stop": "5.1", "release": "5.2",
	} {
		if got.Steps[id] != want {
			t.Errorf("step %s = %q, want %q", id, got.Steps[id], want)
		}
	}

	hub := NewHub(HubArgs{})
	run, err := hub.Start(context.Background(), StartArgs{Workflow: wf, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	run.Wait()
	view := run.view()
	byID := map[string]StepView{}
	for _, s := range view.Steps {
		byID[s.ID] = s
	}
	if byID["stage"].Number != "1.3.1" || byID["stop"].Group != CleanupGroupID || byID["stop"].Number != "5.1" {
		t.Fatalf("view numbers: %+v %+v", byID["stage"], byID["stop"])
	}
	last := view.Groups[len(view.Groups)-1]
	if last.ID != CleanupGroupID || last.Number != "5" || last.Label != "Cleanup" {
		t.Fatalf("cleanup section: %+v", last)
	}
	d, err := hub.Detail(run.ID, "1.1.2")
	if err != nil || d.StepID != "deploy" || d.Step.Number != "1.1.2" {
		t.Fatalf("detail by number: %+v %v", d, err)
	}
}

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"testing"
	"time"
)

func TestVerificationLifecycleObservers(t *testing.T) {
	events := make(chan Event, 8)
	hub := NewHub(HubArgs{OnEvent: func(e Event) { events <- e }})
	wf := loadWF(t, `name: event-test
steps:
  - id: owner
    type: human_gate
    prompt: Is it ready?
    choices:
      - id: pass
        label: Yes
`)
	run, err := hub.Start(context.Background(), StartArgs{Workflow: wf, Cwd: t.TempDir(), Owner: "test-owner"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hub.Abort(run.ID, "test cleanup"); run.Wait() })
	for _, kind := range []string{"started", "waiting"} {
		select {
		case e := <-events:
			if e.Kind != kind || e.RunID != run.ID || e.Workflow != "event-test" || e.Owner != "test-owner" {
				t.Fatalf("wrong lifecycle: %+v", e)
			}
		case <-time.After(time.Second):
			t.Fatalf("missing %s", kind)
		}
	}
	if err := hub.Answer(run.ID, "owner", Answer{ChoiceID: "pass"}); err != nil {
		t.Fatal(err)
	}
	result := run.Wait()
	if result.Status != StatusPassed {
		t.Fatalf("result %+v", result)
	}
	select {
	case e := <-events:
		if e.Kind != "completed" || e.Status != StatusPassed {
			t.Fatalf("wrong completion: %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("missing completion")
	}
}

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package logcapture

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/oslog"
)

type failedDTXAdapter struct{ failure error }

func (a failedDTXAdapter) LogStream(ctx context.Context, id string, filter device.LogFilter, out chan<- device.LogLine) error {
	out <- mkLine(0, "before loss")
	return a.failure
}

func TestDTXIntegrityFailureMakesManagedCaptureIncomplete(t *testing.T) {
	t.Setenv("SPYDER_DATA_DIR", t.TempDir())
	for _, cause := range []string{"DTX connection EOF", "activitytracetap batch decode underflow"} {
		t.Run(cause, func(t *testing.T) {
			manager := NewManager()
			defer manager.Close()
			session, err := manager.Start(context.Background(), failedDTXAdapter{fmt.Errorf("%w: %s", oslog.ErrCaptureIntegrity, cause)}, StartParams{Device: "iPad", DeviceID: "controlled-udid", Platform: "ios"})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-session.done:
			case <-time.After(time.Second):
				t.Fatal("owned reader was not reaped")
			}
			peek, err := manager.Get(session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(peek.Lines) != 1 || !peek.Integrity.Incomplete || !peek.Integrity.ReaderReaped || !strings.Contains(peek.Integrity.TransportError, cause) {
				t.Fatalf("loss looked clean: %+v", peek.Integrity)
			}
			// Lost device-record count is unknown. Do not invent a parse/row count.
			if peek.Integrity.DroppedLines != 0 || peek.Integrity.ParseErrors != 0 {
				t.Fatalf("invented lost-record count: %+v", peek.Integrity)
			}
			final, err := manager.Stop(session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !final.Integrity.Incomplete || !final.Integrity.ReaderReaped || !strings.Contains(final.Integrity.TransportError, cause) {
				t.Fatalf("final drain erased loss: %+v", final.Integrity)
			}
		})
	}
}

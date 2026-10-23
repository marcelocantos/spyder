// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package wedge

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestAttemptRecovery_UsesControlledExecutor(t *testing.T) {
	for _, failure := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		cause := errors.New("fixture recovery denied")
		err := attemptRecovery(ctx, func(actual context.Context, name string, args ...string) ([]byte, error) {
			calls++
			if actual != ctx || name != "sudo" || !reflect.DeepEqual(args, []string{"-n", recoveryHelperPath}) {
				t.Fatalf("wrong recovery command %q %q", name, args)
			}
			if failure {
				return []byte("fixture denial"), cause
			}
			return []byte("fixture success"), nil
		})
		if calls != 1 || (failure && !errors.Is(err, cause)) || (!failure && err != nil) {
			t.Fatalf("failure=%v calls=%d err=%v", failure, calls, err)
		}
	}
}

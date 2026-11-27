// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package simemu

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestQuietSimAudio_DisablesThenStopsCallServices(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failOn    string
		wantCalls []string
	}{
		{"both succeed", "", []string{
			"spawn U launchctl disable system/com.apple.telephonyutilities.callservicesd",
			"spawn U launchctl bootout system/com.apple.telephonyutilities.callservicesd",
		}},
		{"already stopped", "bootout", []string{
			"spawn U launchctl disable system/com.apple.telephonyutilities.callservicesd",
			"spawn U launchctl bootout system/com.apple.telephonyutilities.callservicesd",
		}},
		{"disable fails", "disable", []string{
			"spawn U launchctl disable system/com.apple.telephonyutilities.callservicesd",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			quietSimAudio("U", func(args ...string) error {
				calls = append(calls, strings.Join(args, " "))
				if tc.failOn != "" && args[3] == tc.failOn {
					return errors.New("exit status 1")
				}
				return nil
			})
			if !reflect.DeepEqual(calls, tc.wantCalls) {
				t.Fatalf("calls = %q, want %q", calls, tc.wantCalls)
			}
		})
	}
}

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/danielpaulus/go-ios/ios/appservice"
)

func TestRetryIOSProcessList_OneShotDecodeOpensFreshConnection(t *testing.T) {
	var calls, invalidations, connection int
	read := func() ([]appservice.Process, error) {
		calls++
		if calls == 1 {
			if connection != 0 {
				t.Fatal("first read did not use original connection")
			}
			return nil, fmt.Errorf("listProcesses receive: ReceiveOnServerClientStream: DecodeMessage: failed to decode wrapper")
		}
		if connection != 1 {
			t.Fatal("second read reused failed DTX connection")
		}
		return []appservice.Process{{Pid: 42, Path: "/Applications/StockCars.app/StockCars"}}, nil
	}
	got, err := RetryIOSProcessList(read, func() { invalidations++; connection++ })
	if err != nil || len(got) != 1 || got[0].Pid != 42 || calls != 2 || invalidations != 1 {
		t.Fatalf("processes=%v err=%v calls=%d invalidations=%d", got, err, calls, invalidations)
	}
}

func TestRetryIOSProcessList_Exhausted(t *testing.T) {
	var calls, invalidations int
	decode := errors.New("DecodeMessage: failed to decode wrapper")
	_, err := RetryIOSProcessList(func() ([]appservice.Process, error) {
		calls++
		return nil, decode
	}, func() { invalidations++ })
	if !errors.Is(err, decode) || calls != 2 || invalidations != 2 {
		t.Fatalf("err=%v calls=%d invalidations=%d; want last error, two attempts and bad connection evicted", err, calls, invalidations)
	}
}

func TestRetryIOSProcessList_NonretryableAndNotRunning(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		processes []appservice.Process
	}{
		{"semantic error", errors.New("permission denied"), nil},
		{"empty list means app not running", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, invalidations := 0, 0
			got, err := RetryIOSProcessList(func() ([]appservice.Process, error) {
				calls++
				return tc.processes, tc.err
			}, func() { invalidations++ })
			if calls != 1 || invalidations != 0 || !errors.Is(err, tc.err) || len(got) != len(tc.processes) {
				t.Fatalf("got=%v err=%v calls=%d invalidations=%d", got, err, calls, invalidations)
			}
		})
	}
}

func TestRetryableIOSProcessListError(t *testing.T) {
	for _, err := range []error{io.EOF, io.ErrUnexpectedEOF, errors.New("broken pipe"), errors.New("connection reset by peer")} {
		if !retryableIOSProcessListError(err) {
			t.Errorf("not retryable: %v", err)
		}
	}
	for _, err := range []error{nil, errors.New("app not running"), errors.New("invalid process selector"), errors.New("listProcesses receive: permission denied")} {
		if retryableIOSProcessListError(err) {
			t.Errorf("should not retry: %v", err)
		}
	}
}

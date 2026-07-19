// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package scriptlib

import "testing"

func TestAssertTrajectoryCorridor(t *testing.T) {
	ok := []Point{{0, 0}, {1, 1}, {2, 0.5}}
	if err := AssertTrajectoryCorridor(ok, -1, 3, -1, 2); err != nil {
		t.Fatal(err)
	}
	bad := []Point{{0, 0}, {10, 0}}
	if err := AssertTrajectoryCorridor(bad, -1, 3, -1, 2); err == nil {
		t.Fatal("expected failure outside corridor")
	}
	if err := AssertTrajectoryCorridor(nil, 0, 1, 0, 1); err == nil {
		t.Fatal("empty series must fail")
	}
}

func TestAssertDragFollow(t *testing.T) {
	finger := []Point{{0, 0}, {0.1, 0}, {0.2, 0}}
	object := []Point{{0, 0}, {0.1, 0.01}, {0.2, 0}}
	if err := AssertDragFollow(finger, object, 0.05); err != nil {
		t.Fatal(err)
	}
	drift := []Point{{0, 0}, {0.5, 0.5}, {1, 1}}
	if err := AssertDragFollow(finger, drift, 0.05); err == nil {
		t.Fatal("expected p95 failure")
	}
	if err := AssertDragFollow(finger, finger[:1], 1); err == nil {
		t.Fatal("length mismatch must fail")
	}
}

func TestAssertSettle(t *testing.T) {
	// awake for first 3 samples, then quiet — maxSteps=5 ok
	awake := []bool{true, true, true, false, false, false}
	if err := AssertSettle(awake, 5); err != nil {
		t.Fatal(err)
	}
	if err := AssertSettle(awake, 2); err == nil {
		t.Fatal("expected settle failure when last awake past maxSteps")
	}
	if err := AssertSettle([]bool{true, true, true}, 10); err == nil {
		t.Fatal("ends while awake must fail")
	}
}

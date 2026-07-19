// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package scriptlib

import (
	"fmt"
	"math"
	"sort"
)

// Point is a 2D sample used by dynamic asserts.
type Point struct {
	X float64
	Y float64
}

// AssertTrajectoryCorridor fails closed if any sample leaves the axis-aligned
// corridor [minX,maxX] × [minY,maxY]. Empty series is an error.
func AssertTrajectoryCorridor(pts []Point, minX, maxX, minY, maxY float64) error {
	if len(pts) == 0 {
		return fmt.Errorf("assert_trajectory: empty series")
	}
	if minX > maxX || minY > maxY {
		return fmt.Errorf("assert_trajectory: invalid corridor bounds")
	}
	for i, p := range pts {
		if p.X < minX || p.X > maxX || p.Y < minY || p.Y > maxY {
			return fmt.Errorf(
				"assert_trajectory: sample %d (%.4f,%.4f) outside corridor x[%.4f,%.4f] y[%.4f,%.4f]",
				i, p.X, p.Y, minX, maxX, minY, maxY,
			)
		}
	}
	return nil
}

// AssertDragFollow fails closed if the p95 Euclidean distance between
// paired finger and object samples exceeds maxP95. Series must be equal length ≥1.
func AssertDragFollow(finger, object []Point, maxP95 float64) error {
	if len(finger) == 0 || len(object) == 0 {
		return fmt.Errorf("assert_drag_follow: empty series")
	}
	if len(finger) != len(object) {
		return fmt.Errorf("assert_drag_follow: length mismatch finger=%d object=%d", len(finger), len(object))
	}
	if maxP95 < 0 {
		return fmt.Errorf("assert_drag_follow: max_p95 must be >= 0")
	}
	errs := make([]float64, len(finger))
	for i := range finger {
		dx := finger[i].X - object[i].X
		dy := finger[i].Y - object[i].Y
		errs[i] = math.Hypot(dx, dy)
	}
	sort.Float64s(errs)
	// nearest-rank p95
	idx := int(math.Ceil(0.95*float64(len(errs)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(errs) {
		idx = len(errs) - 1
	}
	p95 := errs[idx]
	if p95 > maxP95 {
		return fmt.Errorf(
			"assert_drag_follow: p95 error %.6f exceeds max_p95 %.6f (n=%d, max_err=%.6f)",
			p95, maxP95, len(errs), errs[len(errs)-1],
		)
	}
	return nil
}

// AssertSettle fails closed if awake flags stay true past maxIndex inclusive
// (0-based). The series must contain at least one true then all false after
// the settle index, or start false and stay false. Empty is an error.
// maxSteps is the maximum number of steps that may still report awake=true
// after the first sample; settle must occur with awake=false for all samples
// with index >= maxSteps.
func AssertSettle(awake []bool, maxSteps int) error {
	if len(awake) == 0 {
		return fmt.Errorf("assert_settle: empty series")
	}
	if maxSteps < 0 {
		return fmt.Errorf("assert_settle: max_steps must be >= 0")
	}
	lastAwake := -1
	for i, a := range awake {
		if a {
			lastAwake = i
		}
	}
	if lastAwake >= maxSteps {
		return fmt.Errorf(
			"assert_settle: still awake at sample %d (max_steps=%d, n=%d)",
			lastAwake, maxSteps, len(awake),
		)
	}
	// After lastAwake, all must be false (already true by construction).
	// Require at least one quiet sample at the end if maxSteps is within range.
	if lastAwake >= 0 && lastAwake == len(awake)-1 {
		return fmt.Errorf("assert_settle: series ends while still awake (index %d)", lastAwake)
	}
	return nil
}

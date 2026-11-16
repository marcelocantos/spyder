// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
)

// devices rows carry the sampler's fresh power reading and drop a stale one.
func TestAnnotatePower_FreshOnly(t *testing.T) {
	h := &Handler{lastPower: map[string]powerReading{
		"fresh": {power: device.Power{Health: device.PowerDraining}, at: time.Now()},
		"stale": {power: device.Power{Health: device.PowerOK}, at: time.Now().Add(-2 * powerFresh)},
	}}
	rows := []device.Info{{UUID: "fresh"}, {UUID: "stale"}, {UUID: "unseen"}}
	h.annotatePower(rows)
	if rows[0].Power == nil || rows[0].Power.Health != device.PowerDraining {
		t.Fatalf("fresh row: %+v", rows[0].Power)
	}
	if rows[1].Power != nil || rows[2].Power != nil {
		t.Fatalf("stale/unseen rows annotated: %+v %+v", rows[1].Power, rows[2].Power)
	}
}

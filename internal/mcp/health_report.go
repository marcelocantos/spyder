// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"time"

	"github.com/marcelocantos/spyder/internal/health"
	"github.com/marcelocantos/spyder/internal/wedge"
)

// HealthReport is the full GET /api/v1/health body (🎯T99.5 / T99.6):
// the live model snapshot plus doctor/wedge shared finding and in-flight
// tool calls so `spyder status` can show WHAT is stuck.
type HealthReport struct {
	At            time.Time               `json:"at"`
	Entities      []health.EntitySnapshot `json:"entities"`
	DoctorFinding wedge.DoctorFinding     `json:"doctor_finding"`
	InFlight      []InFlightOp            `json:"in_flight"`
}

// HealthReport builds the unified health surface for REST, status, and health().
func (h *Handler) HealthReport() HealthReport {
	var snap health.Snapshot
	if h != nil && h.health != nil {
		snap = h.health.Model().Snapshot()
	}
	ops := h.InFlightOps()
	if ops == nil {
		ops = []InFlightOp{}
	}
	ents := snap.Entities
	if ents == nil {
		ents = []health.EntitySnapshot{}
	}
	return HealthReport{
		At:            snap.At,
		Entities:      ents,
		DoctorFinding: wedge.LastDoctorFinding(),
		InFlight:      ops,
	}
}

// Snapshot returns the model-only view (entities) for callers that do not
// need doctor/in-flight enrichment.
func (r HealthReport) Snapshot() health.Snapshot {
	return health.Snapshot{At: r.At, Entities: r.Entities}
}

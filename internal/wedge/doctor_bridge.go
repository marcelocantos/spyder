// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package wedge

import (
	"sync"
	"time"
)

// DoctorFinding is the last wedge/health-aligned diagnosis shared between
// the wedge monitor, spyder doctor, and (when the daemon is up) health status.
// Resolves the parallel-diagnosis gap (🎯T99.6 / former TODO T90.3).
type DoctorFinding struct {
	Wedged    bool      `json:"wedged"`
	Detail    string    `json:"detail,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

var (
	findingMu   sync.Mutex
	lastFinding DoctorFinding
)

// RecordDoctorFinding updates the shared finding (monitor + doctor).
func RecordDoctorFinding(wedged bool, detail string) {
	findingMu.Lock()
	defer findingMu.Unlock()
	lastFinding = DoctorFinding{
		Wedged:    wedged,
		Detail:    detail,
		UpdatedAt: time.Now().UTC(),
	}
}

// LastDoctorFinding returns the shared diagnosis snapshot.
func LastDoctorFinding() DoctorFinding {
	findingMu.Lock()
	defer findingMu.Unlock()
	return lastFinding
}

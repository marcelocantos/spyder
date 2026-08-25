// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package ship

import "fmt"

// LaneFor is a consumer preflight selector for `spyder secret missing
// --for …` (🎯T133.2 / 🎯T133.7).
type LaneFor string

const (
	ForMatch    LaneFor = "match"
	ForPilot    LaneFor = "pilot"
	ForDeliver  LaneFor = "deliver"
	ForSupply   LaneFor = "supply"
	ForFirebase LaneFor = "firebase"
)

// RequiredKinds returns the envelope kinds a lane class needs.
func RequiredKinds(forLane LaneFor) ([]string, error) {
	switch forLane {
	case ForMatch:
		return []string{KindMatchPassword, KindASC}, nil
	case ForPilot, ForDeliver:
		return []string{KindASC}, nil
	case ForSupply:
		return []string{KindPlayServiceAccount}, nil
	case ForFirebase:
		return []string{KindFirebaseAdmin}, nil
	default:
		return nil, fmt.Errorf("unknown --for %q (want match|pilot|deliver|supply|firebase)", forLane)
	}
}

// MissingKinds lists required kinds that are absent from the envelope.
func (e *Envelope) MissingKinds(forLane LaneFor) ([]string, error) {
	need, err := RequiredKinds(forLane)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, k := range need {
		if !e.hasKind(k) {
			missing = append(missing, k)
		}
	}
	return missing, nil
}

func (e *Envelope) hasKind(k string) bool {
	switch k {
	case KindMatchPassword:
		return e.MatchPassword != ""
	case KindASC:
		return e.ASC != nil && e.ASC.P8 != ""
	case KindPlayUpload:
		return e.PlayUpload != nil && len(e.PlayUpload.Keystore) > 0
	case KindPlayServiceAccount:
		return len(e.PlayServiceAccount) > 0
	case KindFirebaseAdmin:
		return len(e.FirebaseAdmin) > 0
	case KindFirebaseAdminDev:
		return len(e.FirebaseAdminDev) > 0
	default:
		return false
	}
}

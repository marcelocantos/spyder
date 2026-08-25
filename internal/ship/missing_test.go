// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package ship

import "testing"

func TestMissingKinds(t *testing.T) {
	e := &Envelope{Version: EnvelopeVersion}
	miss, err := e.MissingKinds(ForPilot)
	if err != nil {
		t.Fatal(err)
	}
	if len(miss) != 1 || miss[0] != KindASC {
		t.Fatalf("got %v", miss)
	}
	e.ASC = &ASCCreds{KeyID: "ABC", P8: "pem"}
	miss, err = e.MissingKinds(ForPilot)
	if err != nil || len(miss) != 0 {
		t.Fatalf("got %v %v", miss, err)
	}
	miss, err = e.MissingKinds(ForMatch)
	if err != nil {
		t.Fatal(err)
	}
	if len(miss) != 1 || miss[0] != KindMatchPassword {
		t.Fatalf("match missing: %v", miss)
	}
}

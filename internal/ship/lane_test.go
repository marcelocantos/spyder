// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package ship

import "testing"

func TestClassifyLane(t *testing.T) {
	cases := []struct {
		args []string
		want LaneClass
	}{
		{[]string{"sync_certs"}, ClassRead},
		{[]string{"match", "development"}, ClassRead},
		{[]string{"gym"}, ClassBuild},
		{[]string{"pilot", "--ipa", "x.ipa"}, ClassTestPublish},
		{[]string{"supply", "track:internal"}, ClassTestPublish},
		{[]string{"deliver", "--submit_for_review"}, ClassProdPublish},
		{[]string{"supply", "track:production"}, ClassProdPublish},
		{[]string{"match", "nuke"}, ClassIrreversible},
		{[]string{"nuke"}, ClassIrreversible},
	}
	for _, tc := range cases {
		got, action, err := ClassifyLane(tc.args)
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if got != tc.want {
			t.Fatalf("%v: class %s want %s (action %s)", tc.args, got, tc.want, action)
		}
		if !tc.want.NeedsConfirm() && (got == ClassProdPublish || got == ClassIrreversible) {
			t.Fatalf("NeedsConfirm mismatch")
		}
	}
}

func TestClassifyLane_NeedsConfirm(t *testing.T) {
	if !ClassProdPublish.NeedsConfirm() || !ClassIrreversible.NeedsConfirm() {
		t.Fatal("prod/irreversible need confirm")
	}
	if ClassRead.NeedsConfirm() || ClassBuild.NeedsConfirm() || ClassTestPublish.NeedsConfirm() {
		t.Fatal("read/build/test_publish must not need confirm")
	}
}

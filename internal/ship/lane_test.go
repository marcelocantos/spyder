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

// T155: ge's named lanes must be classified before a child can launch.
func TestClassifyLane_GeReleaseLanes(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want LaneClass
	}{
		{"submit", []string{"submit_release"}, ClassProdPublish},
		{"platform-submit", []string{"ios", "submit_release"}, ClassProdPublish},
		{"external", []string{"upload_testflight", "project:Game.xcodeproj", "external:true"}, ClassProdPublish},
		{"external-flag", []string{"ios", "upload_testflight", "--external=true"}, ClassProdPublish},
		{"external-split", []string{"upload_testflight", "--external", "true"}, ClassProdPublish},
		{"internal", []string{"upload_testflight", "project:Game.xcodeproj"}, ClassTestPublish},
		{"false", []string{"upload_testflight", "external:false"}, ClassTestPublish},
		{"false-flag", []string{"ios", "upload_testflight", "--external=false"}, ClassTestPublish},
		{"false-split", []string{"upload_testflight", "--external", "false"}, ClassTestPublish},
		{"unknown-unchanged", []string{"some_unknown_lane", "external:true"}, ClassTestPublish},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := ClassifyLane(tc.args)
			if err != nil || got != tc.want {
				t.Fatalf("ClassifyLane(%q) = %s, %v; want %s", tc.args, got, err, tc.want)
			}
		})
	}
}

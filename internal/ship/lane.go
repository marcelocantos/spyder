// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package ship

import (
	"fmt"
	"strings"
)

// LaneClass is the safety class of a fastlane invocation (🎯T133.5).
type LaneClass string

const (
	ClassRead         LaneClass = "read"
	ClassBuild        LaneClass = "build"
	ClassTestPublish  LaneClass = "test_publish"
	ClassProdPublish  LaneClass = "prod_publish"
	ClassIrreversible LaneClass = "irreversible"
)

// NeedsConfirm reports whether --confirm is required before exec.
func (c LaneClass) NeedsConfirm() bool {
	return c == ClassProdPublish || c == ClassIrreversible
}

// ClassifyLane maps fastlane argv (after spyder flags) to a lane class.
// The first non-flag token is treated as the action / lane name.
func ClassifyLane(fastlaneArgs []string) (LaneClass, string, error) {
	action := firstAction(fastlaneArgs)
	if action == "" {
		return "", "", fmt.Errorf("fastlane: missing action (e.g. pilot, deliver, sync_certs)")
	}
	low := strings.ToLower(action)
	joined := strings.ToLower(strings.Join(fastlaneArgs, " "))

	switch {
	case low == "nuke" || low == "match_nuke" || strings.Contains(joined, "match nuke") ||
		strings.Contains(joined, "nuke_certs") || strings.Contains(joined, "nuke_device"):
		return ClassIrreversible, action, nil
	case strings.Contains(joined, "upload_key_reset") || strings.Contains(joined, "request_upload_key"):
		return ClassIrreversible, action, nil
	case low == "deliver" && (strings.Contains(joined, "submit_for_review") ||
		strings.Contains(joined, "submit_for_review:true") ||
		hasFlag(fastlaneArgs, "--submit_for_review")):
		return ClassProdPublish, action, nil
	case low == "supply" && (strings.Contains(joined, "production") ||
		hasArgValue(fastlaneArgs, "track", "production")):
		return ClassProdPublish, action, nil
	case low == "pilot" && (strings.Contains(joined, "external") ||
		hasFlag(fastlaneArgs, "--distribute_external") ||
		hasFlag(fastlaneArgs, "--external")):
		return ClassProdPublish, action, nil
	case low == "pilot" || low == "supply" || low == "upload_to_testflight" ||
		low == "upload_to_play_store":
		return ClassTestPublish, action, nil
	case low == "gym" || low == "build_ipa" || low == "build_app" ||
		low == "build_android_app" || low == "gradle":
		return ClassBuild, action, nil
	case low == "sync_certs" || low == "match" || low == "precheck" ||
		low == "cert" || low == "sigh" || low == "get_certificates" ||
		low == "get_provisioning_profile":
		// match without nuke is read/build tooling for certs.
		if strings.Contains(joined, "nuke") {
			return ClassIrreversible, action, nil
		}
		return ClassRead, action, nil
	default:
		// Unknown lanes default to test_publish (audit, no confirm) so
		// a typo does not silently skip the confirm gate for prod.
		return ClassTestPublish, action, nil
	}
}

func firstAction(args []string) string {
	for _, a := range args {
		if a == "" || strings.HasPrefix(a, "-") {
			continue
		}
		return a
	}
	return ""
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			return true
		}
	}
	return false
}

func hasArgValue(args []string, key, want string) bool {
	want = strings.ToLower(want)
	for i, a := range args {
		if a == "--"+key || a == "-"+key {
			if i+1 < len(args) && strings.EqualFold(args[i+1], want) {
				return true
			}
		}
		if strings.HasPrefix(strings.ToLower(a), key+":") {
			if strings.EqualFold(strings.TrimPrefix(a, key+":"), want) ||
				strings.EqualFold(strings.TrimPrefix(strings.ToLower(a), key+":"), want) {
				return true
			}
		}
		prefix := "--" + key + "="
		if strings.HasPrefix(strings.ToLower(a), strings.ToLower(prefix)) {
			if strings.EqualFold(a[len(prefix):], want) {
				return true
			}
		}
	}
	return false
}

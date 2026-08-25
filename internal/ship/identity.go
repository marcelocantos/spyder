// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package ship is the laptop front door for studio secrets and fastlane
// (🎯T133). Build tools call spyder; spyder is the only Security.framework
// principal and the only process that execs fastlane.
package ship

// BundleID is the stable codesign identifier for the spyder binary.
// Keychain ACLs and TCC attach to this designated requirement; changing
// it forces a new authorisation prompt for every studio envelope.
const BundleID = "com.marcelocantos.spyder"

// KeychainService is the SecItem service attribute for all studio envelopes.
const KeychainService = "spyder.studio"

// Studios are the two first-class studio slugs (Apple teams + Play orgs).
const (
	StudioSquz      = "squz"
	StudioMinicades = "minicades"
)

// AppleTeamID maps studio slug → Apple Team ID.
var AppleTeamID = map[string]string{
	StudioSquz:      "SWA3H3N7TW",
	StudioMinicades: "R4D5JQEEE2",
}

// AllowUnsignedEnv enables secret/fastlane ops on an unsigned binary.
// Test-only; never set in production or Homebrew service plists.
const AllowUnsignedEnv = "SPYDER_ALLOW_UNSIGNED_SECRETS"

// CodesignIdentityEnv selects the codesign -s identity for `make sign`.
// Empty → scripts/codesign-spyder.sh picks the preferred Development /
// Developer ID Application identity on the machine.
const CodesignIdentityEnv = "SPYDER_CODESIGN_IDENTITY"

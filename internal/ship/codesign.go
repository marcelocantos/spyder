// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package ship

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrUnsigned is returned when a secrets/fastlane command is refused
// because this binary is not codesigned with a stable identity.
var ErrUnsigned = errors.New("spyder binary is not codesigned for studio secrets")

// Signature describes how this process is signed (🎯T133.1).
type Signature struct {
	// Signed is true when codesign reports a signature (not ad-hoc-only
	// with empty Authority, and not "code object is not signed at all").
	Signed bool
	// Identifier is the codesign -i / Info.plist CFBundleIdentifier.
	Identifier string
	// Authority is the leaf signing certificate common name, if any.
	Authority string
	// TeamID is the Apple team id embedded in the signature, if any.
	TeamID string
	// AdHoc is true for `codesign -s -` style signatures.
	AdHoc bool
	// Path is the resolved executable that was inspected.
	Path string
	// Raw is the trimmed codesign -dv stderr for debugging.
	Raw string
}

// StablePrincipal reports whether this signature is acceptable as the
// studio-secrets keychain principal: signed, not ad-hoc, and identifier
// is BundleID (or empty on some Development builds that only set -i at
// sign time — we require BundleID explicitly after `make sign`).
func (s Signature) StablePrincipal() bool {
	if !s.Signed || s.AdHoc {
		return false
	}
	if s.Identifier != "" && s.Identifier != BundleID {
		return false
	}
	// Prefer an explicit BundleID; unsigned-identifier Development
	// certs still count once Signed && !AdHoc after make sign sets -i.
	return s.Identifier == BundleID
}

// InspectSelf returns the codesign status of the running executable.
// Tests may override InspectExe.
func InspectSelf() (Signature, error) {
	path, err := os.Executable()
	if err != nil {
		return Signature{}, err
	}
	return InspectExe(path)
}

// InspectExe is the injectable codesign inspector (default: real
// codesign on darwin; stub elsewhere).
var InspectExe = inspectExe

// RequireSecretsAccess refuses secret import/fastlane when this binary
// is not a stable codesign principal, unless AllowUnsignedEnv is "1".
func RequireSecretsAccess() error {
	if os.Getenv(AllowUnsignedEnv) == "1" {
		return nil
	}
	sig, err := InspectSelf()
	if err != nil {
		return fmt.Errorf("%w: inspect: %v", ErrUnsigned, err)
	}
	if sig.StablePrincipal() {
		return nil
	}
	why := "not signed"
	switch {
	case sig.AdHoc:
		why = "ad-hoc signature (CDHash churns on every rebuild)"
	case sig.Signed && sig.Identifier != BundleID:
		why = fmt.Sprintf("identifier %q want %q", sig.Identifier, BundleID)
	case !sig.Signed:
		why = "code object is not signed"
	}
	return fmt.Errorf("%w: %s — run `make sign` (or set %s=1 for hermetic tests only)",
		ErrUnsigned, why, AllowUnsignedEnv)
}

// FormatSignature is a one-line human status for `spyder secret status`.
func FormatSignature(s Signature) string {
	if !s.Signed {
		return "unsigned"
	}
	parts := []string{}
	if s.AdHoc {
		parts = append(parts, "ad-hoc")
	}
	if s.Identifier != "" {
		parts = append(parts, "id="+s.Identifier)
	}
	if s.Authority != "" {
		parts = append(parts, "authority="+s.Authority)
	}
	if s.TeamID != "" {
		parts = append(parts, "team="+s.TeamID)
	}
	if s.StablePrincipal() {
		parts = append(parts, "principal=ok")
	} else {
		parts = append(parts, "principal=refuse")
	}
	return strings.Join(parts, " ")
}

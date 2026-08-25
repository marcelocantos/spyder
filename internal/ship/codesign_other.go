// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

//go:build !darwin

package ship

import "fmt"

func inspectExe(path string) (Signature, error) {
	return Signature{Path: path}, fmt.Errorf("codesign inspect is darwin-only")
}

//go:build !unix

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package oslog

import "io/fs"

// This opt-in diagnostic refuses hosts without a verified ownership primitive.
func batchPathOwned(info fs.FileInfo) bool { return false }

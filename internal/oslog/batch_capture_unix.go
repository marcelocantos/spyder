//go:build unix

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package oslog

import (
	"io/fs"
	"os"
	"syscall"
)

func batchPathOwned(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid())
}

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package installdeadline budgets wall-clock time for install/deploy transfers
// over slow USB links (🎯T172).
package installdeadline

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// BaseInstall is the minimum install-class dispatch budget (matches mcp.DeadlineInstall).
const BaseInstall = 5 * time.Minute

// MaxInstall caps scaled budgets so a missing size does not run unbounded.
const MaxInstall = 45 * time.Minute

// Budget returns a wall-clock budget for moving artifactPath to a device.
// usbSpeedLabel is the negotiated link from device inventory (e.g. "480 Mb/s");
// empty means assume a pessimistic USB 2-class link. When deploy is true,
// extra margin covers terminate, launch, and pid verify after install.
func Budget(artifactPath string, usbSpeedLabel string, deploy bool) time.Duration {
	bytes, err := ArtifactBytes(artifactPath)
	if err != nil || bytes <= 0 {
		if deploy {
			return maxDuration(BaseInstall, 10*time.Minute)
		}
		return BaseInstall
	}
	transfer := time.Duration(float64(bytes) / float64(effectiveBytesPerSec(usbSpeedLabel)) * float64(time.Second))
	margin := 3 * time.Minute
	if deploy {
		margin += 2 * time.Minute
	}
	total := transfer + margin
	if total < BaseInstall {
		return BaseInstall
	}
	if total > MaxInstall {
		return MaxInstall
	}
	return total
}

// ArtifactBytes returns the byte size of a .ipa/.apk file or the sum of a .app tree.
func ArtifactBytes(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return info.Size(), nil
	}
	var total int64
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		fi, statErr := d.Info()
		if statErr != nil {
			return statErr
		}
		total += fi.Size()
		return nil
	})
	return total, err
}

func effectiveBytesPerSec(usbSpeedLabel string) int64 {
	// Pessimistic effective throughput (not wire rate): zipconduit + device unpack.
	switch strings.TrimSpace(usbSpeedLabel) {
	case "480 Mb/s":
		return 3 * 1024 * 1024
	case "5 Gb/s":
		return 10 * 1024 * 1024
	case "10 Gb/s", "20 Gb/s":
		return 16 * 1024 * 1024
	default:
		// Unknown / wireless / emulator: assume USB-2-class transfer unless proven faster.
		return 4 * 1024 * 1024
	}
}

func maxDuration(a, b time.Duration) time.Duration {
	if a >= b {
		return a
	}
	return b
}

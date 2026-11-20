// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package androidobb knows how an Android app built with an APK
// expansion file (Unity's "split application binary") is laid out on
// the host and where its OBB files go on the device (🎯T197).
//
// Unity writes `<name>.apk` beside `<name>.main.obb` (and, rarely,
// `<name>.patch.obb`). The device expects them at
// `/sdcard/Android/obb/<package>/<kind>.<versionCode>.<package>.obb`.
package androidobb

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Kind is the expansion-file slot: main or patch.
type Kind string

const (
	Main  Kind = "main"
	Patch Kind = "patch"
)

// DeviceRoot is the shared-storage directory Android reads OBBs from.
const DeviceRoot = "/sdcard/Android/obb"

// File is one expansion file on the host.
type File struct {
	Local string
	Kind  Kind
}

// Siblings returns the OBB files that sit beside apkPath under the
// names Unity gives them. A path that is not an .apk has none.
func Siblings(apkPath string) []File {
	if !strings.EqualFold(filepath.Ext(apkPath), ".apk") {
		return nil
	}
	base := strings.TrimSuffix(apkPath, filepath.Ext(apkPath))
	var out []File
	for _, k := range []Kind{Main, Patch} {
		p := base + "." + string(k) + ".obb"
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			out = append(out, File{Local: p, Kind: k})
		}
	}
	return out
}

// Classify names the slot an explicitly listed OBB fills. Both Unity's
// host name (`x.patch.obb`) and the device name (`patch.12.pkg.obb`)
// are recognised; anything else is the main file.
func Classify(path string) (File, error) {
	name := strings.ToLower(filepath.Base(path))
	if filepath.Ext(name) != ".obb" {
		return File{}, fmt.Errorf("%q is not an .obb file", path)
	}
	kind := Main
	if strings.HasPrefix(name, "patch.") || strings.Contains(name, ".patch.") {
		kind = Patch
	}
	return File{Local: path, Kind: kind}, nil
}

// FromList classifies an explicit list and rejects two files for the
// same slot.
func FromList(paths []string) ([]File, error) {
	var out []File
	seen := map[Kind]string{}
	for _, p := range paths {
		f, err := Classify(p)
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[f.Kind]; dup {
			return nil, fmt.Errorf("two %s OBB files: %q and %q", f.Kind, prev, p)
		}
		seen[f.Kind] = p
		out = append(out, f)
	}
	return out, nil
}

// DeviceDir is the per-package OBB directory on the device.
func DeviceDir(pkg string) string {
	return DeviceRoot + "/" + pkg
}

// DevicePath is where Android looks for this file of pkg at versionCode.
func DevicePath(kind Kind, pkg, versionCode string) string {
	return fmt.Sprintf("%s/%s.%s.%s.obb", DeviceDir(pkg), kind, versionCode, pkg)
}

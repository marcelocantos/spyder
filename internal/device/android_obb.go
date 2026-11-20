// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/marcelocantos/spyder/internal/androidobb"
)

// APKIdentity is what `aapt dump badging` says about an APK.
type APKIdentity struct {
	Package     string
	VersionCode string
}

// readAPKIdentity is ReadAPKIdentity; tests replace it.
var readAPKIdentity = ReadAPKIdentity

// ReadAPKIdentity returns the package name and versionCode of an APK.
func ReadAPKIdentity(apkPath string) (APKIdentity, error) {
	aapt, err := findAapt()
	if err != nil {
		return APKIdentity{}, err
	}
	var outBuf, errBuf bytes.Buffer
	cmd := exec.Command(aapt, "dump", "badging", apkPath)
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return APKIdentity{}, fmt.Errorf("aapt dump badging: %v: %s", err, truncate(errBuf.String(), 200))
	}
	return parseBadging(outBuf.String())
}

// parseBadging reads the `package:` line of `aapt dump badging`:
//
//	package: name='com.example.app' versionCode='254' versionName='3.28' ...
func parseBadging(out string) (APKIdentity, error) {
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "package:") {
			continue
		}
		var id APKIdentity
		for _, field := range strings.Fields(line) {
			if v, ok := strings.CutPrefix(field, "name="); ok {
				id.Package = strings.Trim(v, "'")
			}
			if v, ok := strings.CutPrefix(field, "versionCode="); ok {
				id.VersionCode = strings.Trim(v, "'")
			}
		}
		if id.Package == "" {
			return APKIdentity{}, errors.New("package name not found in aapt output")
		}
		return id, nil
	}
	return APKIdentity{}, errors.New("no package line in aapt output")
}

// findAapt prefers aapt on PATH, then the newest build-tools in the
// Android SDK. The LaunchAgent daemon's PATH may not include the SDK.
func findAapt() (string, error) {
	if p, err := exec.LookPath("aapt"); err == nil {
		return p, nil
	}
	var roots []string
	for _, env := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
		if v := os.Getenv(env); v != "" {
			roots = append(roots, v)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Join(home, "Library", "Android", "sdk"))
	}
	for _, root := range roots {
		matches, _ := filepath.Glob(filepath.Join(root, "build-tools", "*", "aapt"))
		if len(matches) == 0 {
			continue
		}
		// Version directories sort lexically within a major; good enough
		// to pick the newest of 34.0.0 … 37.0.0.
		sort.Strings(matches)
		return matches[len(matches)-1], nil
	}
	return "", errors.New("aapt not found on PATH or in the Android SDK build-tools (install build-tools)")
}

// ExpansionParts returns the OBB files that go with apkPath: the
// explicit list when one is given, otherwise the ones Unity wrote
// beside the APK.
func ExpansionParts(apkPath string, explicit []string) ([]androidobb.File, error) {
	if len(explicit) > 0 {
		return androidobb.FromList(explicit)
	}
	return androidobb.Siblings(apkPath), nil
}

// InstallExpansion pushes an app's OBB files to where Android reads
// them (🎯T197) and removes that slot's files left by other
// versionCodes. It returns the device paths written. It never
// uninstalls anything: a failed push leaves the app and its data as
// they were.
func (a *AndroidAdapter) InstallExpansion(ctx context.Context, id, apkPath string, files []androidobb.File) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	ident, err := readAPKIdentity(apkPath)
	if err != nil {
		return nil, fmt.Errorf("read package and versionCode from %s: %w", filepath.Base(apkPath), err)
	}
	if ident.VersionCode == "" {
		return nil, fmt.Errorf("no versionCode in %s", filepath.Base(apkPath))
	}
	dir := androidobb.DeviceDir(ident.Package)
	if _, stderr, err := androidAdb(ctx, "-s", id, "shell", "mkdir", "-p", adbShellWord(dir)); err != nil {
		return nil, fmt.Errorf("mkdir %s: %v %s", dir, err, truncate(string(stderr), 200))
	}
	var written []string
	for _, f := range files {
		remote := androidobb.DevicePath(f.Kind, ident.Package, ident.VersionCode)
		out, stderr, err := androidAdb(ctx, "-s", id, "push", f.Local, remote)
		if err != nil {
			combined := string(stderr) + " " + string(out)
			if isAndroidDeviceNotConnected(combined) {
				return written, fmt.Errorf("device not connected: %s", id)
			}
			return written, fmt.Errorf("adb push %s: %v %s", filepath.Base(f.Local), err, truncate(combined, 300))
		}
		written = append(written, remote)
	}
	removeStaleExpansions(ctx, id, ident, files)
	return written, nil
}

// removeStaleExpansions deletes OBBs for the same slots at other
// versionCodes. Android only reads the current one; each stale file is
// hundreds of megabytes. Best-effort.
func removeStaleExpansions(ctx context.Context, id string, ident APKIdentity, files []androidobb.File) {
	dir := androidobb.DeviceDir(ident.Package)
	out, _, err := androidAdb(ctx, "-s", id, "shell", "ls", adbShellWord(dir))
	if err != nil {
		return
	}
	keep := map[string]bool{}
	kinds := map[androidobb.Kind]bool{}
	for _, f := range files {
		keep[filepath.Base(androidobb.DevicePath(f.Kind, ident.Package, ident.VersionCode))] = true
		kinds[f.Kind] = true
	}
	for _, name := range strings.Fields(string(out)) {
		if keep[name] || !strings.HasSuffix(name, "."+ident.Package+".obb") {
			continue
		}
		kind, _, _ := strings.Cut(name, ".")
		if !kinds[androidobb.Kind(kind)] {
			continue
		}
		_, _, _ = androidAdb(ctx, "-s", id, "shell", "rm", "-f", adbShellWord(dir+"/"+name))
	}
}

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package pool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/marcelocantos/spyder/internal/simemu"
)

// RealExecutor is the production Executor that delegates to
// simemu (xcrun simctl + avdmanager).
type RealExecutor struct{}

// SimCreate creates a new iOS simulator and returns its UDID.
func (RealExecutor) SimCreate(name, deviceTypeID, runtimeID string) (string, error) {
	return simemu.SimCreate(name, deviceTypeID, runtimeID)
}

// SimBoot boots an iOS simulator by UDID.
func (RealExecutor) SimBoot(udid string) error {
	return simemu.SimBoot(udid)
}

// SimShutdown shuts down an iOS simulator by UDID.
func (RealExecutor) SimShutdown(udid string) error {
	return simemu.SimShutdown(udid)
}

// SimDelete deletes an iOS simulator by UDID.
func (RealExecutor) SimDelete(udid string) error {
	return simemu.SimDelete(udid)
}

// SimList returns sim devices as SimInfo slices.
func (RealExecutor) SimList() ([]SimInfo, error) {
	devs, err := simemu.SimList()
	if err != nil {
		return nil, err
	}
	out := make([]SimInfo, len(devs))
	for i, d := range devs {
		out[i] = SimInfo{UDID: d.UDID, Name: d.Name, State: d.State}
	}
	return out, nil
}

// AVDClone clones a template AVD by copying its directory and .ini file.
// The template is identified by DeviceType field in the TemplateConfig,
// which for Android is treated as the source AVD name (not a simctl
// identifier). The clone gets newName.
//
// Cloning strategy (arm64-only):
//   - Copy ~/.android/avd/<templateName>.avd/ → ~/.android/avd/<newName>.avd/
//   - Copy ~/.android/avd/<templateName>.ini  → ~/.android/avd/<newName>.ini
//   - Rewrite path= in <newName>.ini to point at the new .avd dir
//   - Rewrite AvdId in <newName>.avd/config.ini to newName
func (RealExecutor) AVDClone(templateName, newName string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("avd clone: home dir: %w", err)
	}
	avdRoot := filepath.Join(home, ".android", "avd")

	srcDir := filepath.Join(avdRoot, templateName+".avd")
	dstDir := filepath.Join(avdRoot, newName+".avd")
	srcIni := filepath.Join(avdRoot, templateName+".ini")
	dstIni := filepath.Join(avdRoot, newName+".ini")

	// Validate source exists.
	if _, err := os.Stat(srcDir); err != nil {
		return fmt.Errorf("avd clone: template AVD dir %s: %w", srcDir, err)
	}
	if _, err := os.Stat(srcIni); err != nil {
		return fmt.Errorf("avd clone: template AVD ini %s: %w", srcIni, err)
	}

	// Copy directory tree.
	if err := copyDir(srcDir, dstDir); err != nil {
		return fmt.Errorf("avd clone: copy dir: %w", err)
	}

	// Copy and rewrite the top-level .ini (path= line).
	iniData, err := os.ReadFile(srcIni)
	if err != nil {
		return fmt.Errorf("avd clone: read ini: %w", err)
	}
	newIni := rewriteINIPath(string(iniData), dstDir)
	if err := os.WriteFile(dstIni, []byte(newIni), 0o644); err != nil {
		return fmt.Errorf("avd clone: write ini: %w", err)
	}

	// Rewrite AvdId in the clone's config.ini.
	configIni := filepath.Join(dstDir, "config.ini")
	if data, err := os.ReadFile(configIni); err == nil {
		updated := rewriteINIValue(string(data), "AvdId", newName)
		updated = rewriteINIValue(updated, "avd.ini.displayname", newName)
		_ = os.WriteFile(configIni, []byte(updated), 0o644)
	}

	return nil
}

// AVDBoot starts an Android emulator and returns its serial.
func (RealExecutor) AVDBoot(name string) (string, error) {
	return simemu.AVDBoot(name)
}

// AVDShutdown stops the emulator with the given serial.
func (RealExecutor) AVDShutdown(serial string) error {
	return simemu.AVDShutdown(serial)
}

// AVDDelete deletes an Android AVD by name.
func (RealExecutor) AVDDelete(name string) error {
	return simemu.AVDDelete(name)
}

// AVDList returns all AVD names.
func (RealExecutor) AVDList() ([]AVDInfo, error) {
	avds, err := simemu.AVDList()
	if err != nil {
		return nil, err
	}
	out := make([]AVDInfo, len(avds))
	for i, a := range avds {
		out[i] = AVDInfo{Name: a.Name, Path: a.Path}
	}
	return out, nil
}

// --------------------------------------------------------------------------
// file-copy helpers
// --------------------------------------------------------------------------

func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, info.Mode())
}

// rewriteINIPath rewrites the path= line in a .ini file to newPath.
func rewriteINIPath(ini, newPath string) string {
	return rewriteINIValue(ini, "path", newPath)
}

// rewriteINIValue rewrites key=<value> lines in a simple key=value ini file.
func rewriteINIValue(ini, key, value string) string {
	lines := strings.Split(ini, "\n")
	prefix := key + "="
	for i, line := range lines {
		if strings.HasPrefix(line, prefix) {
			lines[i] = prefix + value
		}
	}
	return strings.Join(lines, "\n")
}

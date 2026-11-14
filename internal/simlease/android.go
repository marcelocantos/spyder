// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package simlease

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// AVD is one Android Virtual Device on disk (🎯T189).
type AVD struct {
	Name        string
	Path        string // the <name>.avd directory
	IniPath     string // the <name>.ini beside it
	DeviceType  string // hw.device.name
	SystemImage string // sdkmanager package id, e.g. system-images;android-35;google_apis;arm64-v8a
	Running     bool
	LastUsedAt  time.Time // newest mtime among the .avd directory's entries
	Bytes       int64
}

// SystemImage is one installed Android system image.
type SystemImage struct {
	ID      string // system-images;android-35;google_apis;arm64-v8a
	Version string // android-35
	Path    string
	Bytes   int64
}

// AndroidHost lists and deletes AVDs and system images.
type AndroidHost interface {
	AVDs(ctx context.Context) ([]AVD, error)
	SystemImages(ctx context.Context) ([]SystemImage, error)
	DeleteAVD(ctx context.Context, name string) error
	DeleteSystemImage(ctx context.Context, id string) error
}

// FSAndroid reads AVDs and system images straight from disk, so it
// needs no Android SDK tools on the daemon's PATH.
type FSAndroid struct {
	AVDHome string // ~/.android/avd
	SDKRoot string // ~/Library/Android/sdk

	mu    sync.Mutex
	sizes map[string]sizeEntry
}

type sizeEntry struct {
	mtime time.Time
	bytes int64
	at    time.Time
}

// sizeCacheTTL bounds how long a directory size is reused when the
// directory's own mtime has not moved.
const sizeCacheTTL = 6 * time.Hour

// NewFSAndroid locates the AVD home and SDK from the environment, falling
// back to the standard macOS locations.
func NewFSAndroid() *FSAndroid {
	home, _ := os.UserHomeDir()
	avd := os.Getenv("ANDROID_AVD_HOME")
	if avd == "" {
		avd = filepath.Join(home, ".android", "avd")
	}
	sdk := os.Getenv("ANDROID_HOME")
	if sdk == "" {
		sdk = os.Getenv("ANDROID_SDK_ROOT")
	}
	if sdk == "" {
		sdk = filepath.Join(home, "Library", "Android", "sdk")
	}
	return &FSAndroid{AVDHome: avd, SDKRoot: sdk}
}

// readKV reads a key=value ini file.
func readKV(path string) map[string]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	kv := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if ok {
			kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return kv
}

// dirBytes is the disk space a tree occupies (allocated blocks, so
// sparse emulator images count at their real size), cached by the
// directory's mtime.
func (a *FSAndroid) dirBytes(ctx context.Context, dir string) int64 {
	fi, err := os.Stat(dir)
	if err != nil {
		return 0
	}
	a.mu.Lock()
	if a.sizes == nil {
		a.sizes = map[string]sizeEntry{}
	}
	if e, ok := a.sizes[dir]; ok && e.mtime.Equal(fi.ModTime()) && time.Since(e.at) < sizeCacheTTL {
		a.mu.Unlock()
		return e.bytes
	}
	a.mu.Unlock()
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			total += st.Blocks * 512
		} else {
			total += info.Size()
		}
		return nil
	})
	if ctx.Err() == nil {
		a.mu.Lock()
		a.sizes[dir] = sizeEntry{mtime: fi.ModTime(), bytes: total, at: time.Now()}
		a.mu.Unlock()
	}
	return total
}

func (a *FSAndroid) AVDs(ctx context.Context) ([]AVD, error) {
	entries, err := os.ReadDir(a.AVDHome)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	running := runningAVDs(ctx)
	var avds []AVD
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".ini")
		if !ok || e.IsDir() {
			continue
		}
		ini := filepath.Join(a.AVDHome, e.Name())
		dir := readKV(ini)["path"]
		if dir == "" {
			dir = filepath.Join(a.AVDHome, name+".avd")
		}
		cfg := readKV(filepath.Join(dir, "config.ini"))
		avd := AVD{
			Name:        name,
			Path:        dir,
			IniPath:     ini,
			DeviceType:  cfg["hw.device.name"],
			SystemImage: sysdirToPackage(cfg["image.sysdir.1"]),
			Running:     running[name],
			Bytes:       a.dirBytes(ctx, dir),
		}
		if des, err := os.ReadDir(dir); err == nil {
			for _, de := range des {
				if info, err := de.Info(); err == nil && info.ModTime().After(avd.LastUsedAt) {
					avd.LastUsedAt = info.ModTime()
				}
			}
		}
		avds = append(avds, avd)
	}
	return avds, nil
}

// sysdirToPackage turns "system-images/android-35/google_apis/arm64-v8a/"
// into "system-images;android-35;google_apis;arm64-v8a".
func sysdirToPackage(sysdir string) string {
	sysdir = strings.Trim(sysdir, "/")
	if sysdir == "" {
		return ""
	}
	return strings.Join(strings.Split(sysdir, "/"), ";")
}

// runningAVDs names the AVDs a running emulator process was started with
// (`-avd NAME` or `@NAME`).
func runningAVDs(ctx context.Context) map[string]bool {
	out, err := exec.CommandContext(ctx, "ps", "-A", "-o", "command=").Output()
	if err != nil {
		return nil
	}
	return parseRunningAVDs(string(out))
}

func parseRunningAVDs(ps string) map[string]bool {
	m := map[string]bool{}
	for _, line := range strings.Split(ps, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		exe := filepath.Base(fields[0])
		if !strings.HasPrefix(exe, "emulator") && !strings.HasPrefix(exe, "qemu-system") {
			continue
		}
		for i, f := range fields[1:] {
			switch {
			case f == "-avd" && i+2 < len(fields):
				m[fields[i+2]] = true
			case strings.HasPrefix(f, "@") && len(f) > 1:
				m[f[1:]] = true
			}
		}
	}
	return m
}

func (a *FSAndroid) SystemImages(ctx context.Context) ([]SystemImage, error) {
	root := filepath.Join(a.SDKRoot, "system-images")
	apis, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var imgs []SystemImage
	for _, api := range apis {
		if !api.IsDir() {
			continue
		}
		tags, _ := os.ReadDir(filepath.Join(root, api.Name()))
		for _, tag := range tags {
			if !tag.IsDir() {
				continue
			}
			abis, _ := os.ReadDir(filepath.Join(root, api.Name(), tag.Name()))
			for _, abi := range abis {
				if !abi.IsDir() {
					continue
				}
				p := filepath.Join(root, api.Name(), tag.Name(), abi.Name())
				imgs = append(imgs, SystemImage{
					ID:      strings.Join([]string{"system-images", api.Name(), tag.Name(), abi.Name()}, ";"),
					Version: api.Name(),
					Path:    p,
					Bytes:   a.dirBytes(ctx, p),
				})
			}
		}
	}
	return imgs, nil
}

// DeleteAVD removes <name>.avd and <name>.ini, as `avdmanager delete avd`
// does.
func (a *FSAndroid) DeleteAVD(_ context.Context, name string) error {
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return fmt.Errorf("invalid AVD name %q", name)
	}
	ini := filepath.Join(a.AVDHome, name+".ini")
	dir := readKV(ini)["path"]
	if dir == "" {
		dir = filepath.Join(a.AVDHome, name+".avd")
	}
	if !strings.HasSuffix(dir, ".avd") {
		return fmt.Errorf("AVD %s: refusing to delete %s (not an .avd directory)", name, dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.Remove(ini); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// DeleteSystemImage removes an installed system image directory.
func (a *FSAndroid) DeleteSystemImage(_ context.Context, id string) error {
	parts := strings.Split(id, ";")
	if len(parts) != 4 || parts[0] != "system-images" {
		return fmt.Errorf("invalid system image id %q", id)
	}
	for _, p := range parts[1:] {
		if p == "" || p == "." || p == ".." || strings.ContainsAny(p, `/\`) {
			return fmt.Errorf("invalid system image id %q", id)
		}
	}
	root := filepath.Join(a.SDKRoot, "system-images")
	dir := filepath.Join(root, parts[1], parts[2], parts[3])
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	// Tidy now-empty tag and API directories.
	for _, d := range []string{filepath.Dir(dir), filepath.Dir(filepath.Dir(dir))} {
		if d != root {
			_ = os.Remove(d) // fails harmlessly when not empty
		}
	}
	return nil
}

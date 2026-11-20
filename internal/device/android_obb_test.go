// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/marcelocantos/spyder/internal/androidobb"
)

func TestParseBadging(t *testing.T) {
	out := "package: name='com.minicades.stockcars' versionCode='254' versionName='3.28' platformBuildVersionName='16'\nsdkVersion:'24'\n"
	got, err := parseBadging(out)
	if err != nil {
		t.Fatal(err)
	}
	want := APKIdentity{Package: "com.minicades.stockcars", VersionCode: "254"}
	if got != want {
		t.Fatalf("parseBadging = %+v, want %+v", got, want)
	}
	if _, err := parseBadging("sdkVersion:'24'\n"); err == nil {
		t.Fatal("missing package line accepted")
	}
}

func TestInstallExpansionPushesAndPrunesStale(t *testing.T) {
	oldIdent := readAPKIdentity
	readAPKIdentity = func(string) (APKIdentity, error) {
		return APKIdentity{Package: "com.ex.game", VersionCode: "12"}, nil
	}
	t.Cleanup(func() { readAPKIdentity = oldIdent })

	var calls []string
	old := androidAdb
	t.Cleanup(func() { androidAdb = old })
	androidAdb = func(ctx context.Context, args ...string) ([]byte, []byte, error) {
		cmd := strings.Join(args, " ")
		calls = append(calls, cmd)
		if strings.HasSuffix(cmd, "shell ls /sdcard/Android/obb/com.ex.game") {
			return []byte("main.11.com.ex.game.obb\nmain.12.com.ex.game.obb\npatch.10.com.ex.game.obb\n"), nil, nil
		}
		return nil, nil, nil
	}

	files := []androidobb.File{{Local: "/b/Game-vc12.main.obb", Kind: androidobb.Main}}
	written, err := (&AndroidAdapter{}).InstallExpansion(context.Background(), "SER", "/b/Game-vc12.apk", files)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/sdcard/Android/obb/com.ex.game/main.12.com.ex.game.obb"}; !reflect.DeepEqual(written, want) {
		t.Fatalf("written = %v, want %v", written, want)
	}
	want := []string{
		"-s SER shell mkdir -p /sdcard/Android/obb/com.ex.game",
		"-s SER push /b/Game-vc12.main.obb /sdcard/Android/obb/com.ex.game/main.12.com.ex.game.obb",
		"-s SER shell ls /sdcard/Android/obb/com.ex.game",
		// Only the stale main file goes: the patch slot was not pushed.
		"-s SER shell rm -f /sdcard/Android/obb/com.ex.game/main.11.com.ex.game.obb",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("adb calls:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestInstallExpansionPushFailureNeverUninstalls(t *testing.T) {
	oldIdent := readAPKIdentity
	readAPKIdentity = func(string) (APKIdentity, error) {
		return APKIdentity{Package: "com.ex.game", VersionCode: "12"}, nil
	}
	t.Cleanup(func() { readAPKIdentity = oldIdent })
	old := androidAdb
	t.Cleanup(func() { androidAdb = old })
	androidAdb = func(ctx context.Context, args ...string) ([]byte, []byte, error) {
		for _, a := range args {
			if a == "uninstall" {
				t.Fatalf("uninstall called: %v", args)
			}
		}
		if len(args) > 2 && args[2] == "push" {
			return nil, []byte("adb: error: no space left on device"), errors.New("exit status 1")
		}
		return nil, nil, nil
	}
	files := []androidobb.File{{Local: "/b/g.main.obb", Kind: androidobb.Main}}
	if _, err := (&AndroidAdapter{}).InstallExpansion(context.Background(), "SER", "/b/g.apk", files); err == nil {
		t.Fatal("push failure not reported")
	}
}

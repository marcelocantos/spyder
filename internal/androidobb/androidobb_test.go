// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package androidobb

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSiblingsFindsUnityNames(t *testing.T) {
	dir := t.TempDir()
	apk := filepath.Join(dir, "StockCars-3.28-vc256.apk")
	touch(t, apk)
	touch(t, filepath.Join(dir, "StockCars-3.28-vc256.main.obb"))
	touch(t, filepath.Join(dir, "StockCars-3.28-vc256.patch.obb"))
	touch(t, filepath.Join(dir, "StockCars-3.28-vc255.main.obb")) // another build: not ours

	got := Siblings(apk)
	want := []File{
		{Local: filepath.Join(dir, "StockCars-3.28-vc256.main.obb"), Kind: Main},
		{Local: filepath.Join(dir, "StockCars-3.28-vc256.patch.obb"), Kind: Patch},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Siblings = %+v, want %+v", got, want)
	}
}

func TestSiblingsNoneForPlainAPKOrOtherArtifacts(t *testing.T) {
	dir := t.TempDir()
	apk := filepath.Join(dir, "app.apk")
	touch(t, apk)
	if got := Siblings(apk); len(got) != 0 {
		t.Fatalf("plain APK: got %+v", got)
	}
	ipa := filepath.Join(dir, "app.ipa")
	touch(t, ipa)
	touch(t, filepath.Join(dir, "app.main.obb"))
	if got := Siblings(ipa); len(got) != 0 {
		t.Fatalf("ipa: got %+v", got)
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]Kind{
		"/x/Game-1.0-vc12.main.obb":       Main,
		"/x/Game-1.0-vc12.patch.obb":      Patch,
		"/x/main.12.com.example.game.obb": Main,
		"/x/patch.12.com.example.obb":     Patch,
		"/x/assets.obb":                   Main,
	}
	for p, want := range cases {
		f, err := Classify(p)
		if err != nil || f.Kind != want || f.Local != p {
			t.Errorf("Classify(%q) = %+v, %v; want kind %s", p, f, err, want)
		}
	}
	if _, err := Classify("/x/readme.txt"); err == nil {
		t.Error("Classify accepted a non-.obb file")
	}
}

func TestFromListRejectsTwoForOneSlot(t *testing.T) {
	if _, err := FromList([]string{"/a/x.main.obb", "/b/y.main.obb"}); err == nil {
		t.Fatal("two main OBBs accepted")
	}
	got, err := FromList([]string{"/a/x.main.obb", "/a/x.patch.obb"})
	if err != nil || len(got) != 2 {
		t.Fatalf("FromList = %+v, %v", got, err)
	}
}

func TestDevicePath(t *testing.T) {
	got := DevicePath(Main, "com.minicades.stockcars", "256")
	want := "/sdcard/Android/obb/com.minicades.stockcars/main.256.com.minicades.stockcars.obb"
	if got != want {
		t.Fatalf("DevicePath = %q, want %q", got, want)
	}
}

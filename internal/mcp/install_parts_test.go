// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/marcelocantos/spyder/internal/androidobb"
)

// obbStub is an Android adapter that records the OBB push (🎯T197).
type obbStub struct {
	stubAdapter
	installed []string
	pushed    []androidobb.File
}

func (s *obbStub) InstallApp(id, path string) error {
	s.installed = append(s.installed, path)
	return nil
}

func (s *obbStub) InstallExpansion(ctx context.Context, id, apkPath string, files []androidobb.File) ([]string, error) {
	s.pushed = files
	var out []string
	for _, f := range files {
		out = append(out, "/sdcard/Android/obb/pkg/"+string(f.Kind)+".1.pkg.obb")
	}
	return out, nil
}

func writeFile(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The agent passes only the APK; the OBB beside it goes too.
func TestInstallApp_APKTakesSiblingOBB(t *testing.T) {
	dir := t.TempDir()
	apk := filepath.Join(dir, "Game-1.0-vc1.apk")
	writeFile(t, apk)
	writeFile(t, filepath.Join(dir, "Game-1.0-vc1.main.obb"))

	stub := &obbStub{}
	h := newHandlerWithStubs(t, nil, stub)
	r := dispatchJSON(t, h, "install_app", map[string]any{"device": "Raspberry", "path": apk})
	if r.IsError {
		t.Fatalf("install_app: %s", resultText(t, &r))
	}
	want := []androidobb.File{{Local: filepath.Join(dir, "Game-1.0-vc1.main.obb"), Kind: androidobb.Main}}
	if !reflect.DeepEqual(stub.pushed, want) {
		t.Fatalf("pushed %+v, want %+v", stub.pushed, want)
	}
	if text := resultText(t, &r); !strings.Contains(text, "main.1.pkg.obb") || !strings.Contains(text, "Game-1.0-vc1.apk") {
		t.Fatalf("result does not name every part: %s", text)
	}
}

// An explicit parts list replaces discovery.
func TestInstallApp_ExplicitPartsReplaceSiblings(t *testing.T) {
	dir := t.TempDir()
	apk := filepath.Join(dir, "Game.apk")
	writeFile(t, apk)
	writeFile(t, filepath.Join(dir, "Game.main.obb"))
	other := filepath.Join(dir, "elsewhere.patch.obb")
	writeFile(t, other)

	stub := &obbStub{}
	h := newHandlerWithStubs(t, nil, stub)
	r := dispatchJSON(t, h, "install_app", map[string]any{
		"device": "Raspberry", "path": apk, "parts": []any{other},
	})
	if r.IsError {
		t.Fatalf("install_app: %s", resultText(t, &r))
	}
	want := []androidobb.File{{Local: other, Kind: androidobb.Patch}}
	if !reflect.DeepEqual(stub.pushed, want) {
		t.Fatalf("pushed %+v, want %+v", stub.pushed, want)
	}
}

// Parts on a non-Android install is an error, not a silent drop.
func TestInstallApp_PartsRefusedOffAndroid(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "My.app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	obb := filepath.Join(dir, "x.main.obb")
	writeFile(t, obb)
	installs := 0
	ios := &stubAdapter{installApp: func(id, path string) error { installs++; return nil }}
	h := newHandlerWithStubs(t, ios, nil)
	r := dispatchJSON(t, h, "install_app", map[string]any{"device": "iPad", "path": app, "parts": []any{obb}})
	if !r.IsError {
		t.Fatal("parts on iOS accepted")
	}
	if installs != 0 {
		t.Fatal("iOS install ran despite the refused parts")
	}
}

// The MCP surface is app_exec (🎯T88): a Starlark deploy_app with only
// the APK reports the OBB in parts, and an explicit parts list reaches
// the handler as a list.
func TestAppExec_DeployAppInstallsOBBs(t *testing.T) {
	dir := t.TempDir()
	apk := filepath.Join(dir, "Game-1.0-vc1.apk")
	writeFile(t, apk)
	obb := filepath.Join(dir, "Game-1.0-vc1.main.obb")
	writeFile(t, obb)

	stub := &obbStub{}
	h := newHandlerWithStubs(t, nil, stub)
	script := `d = deploy_app(device="Raspberry", path="` + apk + `", bundle_id="com.ex.game")
emit(d["parts"])
install_app(device="Raspberry", path="` + apk + `", parts=["` + obb + `"])`
	r := dispatchJSON(t, h, "app_exec", map[string]any{"script": script})
	text := resultText(t, &r)
	if !strings.Contains(text, "main.1.pkg.obb") {
		t.Fatalf("app_exec deploy_app did not report the OBB part: %s", text)
	}
	if len(stub.installed) != 2 || len(stub.pushed) != 1 || stub.pushed[0].Local != obb {
		t.Fatalf("installed=%v pushed=%+v", stub.installed, stub.pushed)
	}
}

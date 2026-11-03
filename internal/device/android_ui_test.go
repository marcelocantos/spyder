// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"reflect"
	"strings"
	"testing"
)

func TestPlanTextInput_AutoDigitsUseKeyevents(t *testing.T) {
	p, err := PlanTextInput("4071", TextModeAuto)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"input", "keyevent", "11", "7", "14", "8"}}
	if p.Mode != TextModeKeys || !reflect.DeepEqual(p.Steps, want) {
		t.Fatalf("got %+v", p)
	}
}

func TestPlanTextInput_TextEscaping(t *testing.T) {
	cases := []struct {
		in   string
		want [][]string
	}{
		{"wifi", [][]string{{"input", "text", "'wifi'"}}},
		{"a b", [][]string{{"input", "text", "'a%sb'"}}},
		{"it's", [][]string{{"input", "text", `'it'\''s'`}}},
		{`$(rm -rf /);"x"&|<>`, [][]string{{"input", "text", `'$(rm%s-rf%s/);"x"&|<>'`}}},
		// Literal "%s" must not be turned into a space by `input text`.
		{"5%s", [][]string{{"input", "text", "'5%'"}, {"input", "text", "'s'"}}},
		{"a\nb", [][]string{{"input", "text", "'a'"}, {"input", "keyevent", "66"}, {"input", "text", "'b'"}}},
		{"x\t", [][]string{{"input", "text", "'x'"}, {"input", "keyevent", "61"}}},
	}
	for _, c := range cases {
		p, err := PlanTextInput(c.in, TextModeAuto)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if p.Mode != TextModeText || !reflect.DeepEqual(p.Steps, c.want) {
			t.Errorf("%q: got %q want %q", c.in, p.Steps, c.want)
		}
	}
}

func TestPlanTextInput_ForceTextForDigits(t *testing.T) {
	p, err := PlanTextInput("123", TextModeText)
	if err != nil || !reflect.DeepEqual(p.Steps, [][]string{{"input", "text", "'123'"}}) {
		t.Fatalf("got %+v %v", p, err)
	}
}

func TestPlanTextInput_KeysMode(t *testing.T) {
	p, err := PlanTextInput("ab 9\n", TextModeKeys)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Steps, [][]string{{"input", "keyevent", "29", "30", "62", "16", "66"}}) {
		t.Fatalf("got %q", p.Steps)
	}
	if _, err := PlanTextInput("Ab", TextModeKeys); err == nil || strings.Contains(err.Error(), "A") {
		t.Fatalf("uppercase in keys mode should fail without echoing: %v", err)
	}
}

func TestPlanTextInput_Rejects(t *testing.T) {
	if _, err := PlanTextInput("", ""); err == nil {
		t.Fatal("empty should fail")
	}
	if _, err := PlanTextInput("café", ""); err == nil || strings.Contains(err.Error(), "é") {
		t.Fatalf("non-ASCII should fail without echo: %v", err)
	}
	if _, err := PlanTextInput("x", "bogus"); err == nil {
		t.Fatal("bad mode should fail")
	}
}

func TestPlanTextInput_ChunksLongText(t *testing.T) {
	p, err := PlanTextInput(strings.Repeat("x", 450), TextModeText)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 3 {
		t.Fatalf("want 3 chunks, got %d", len(p.Steps))
	}
}

func TestLooksSecret(t *testing.T) {
	for in, want := range map[string]bool{
		"1234": true, "123456": true, "4111 1111": true, "12": false,
		"abc": false, "wifi": false, "   ": false,
	} {
		if got := LooksSecret(in); got != want {
			t.Errorf("LooksSecret(%q)=%v want %v", in, got, want)
		}
	}
}

func TestInputTextContext_ScrubsErrors(t *testing.T) {
	restore := SetAndroidAdbForTest(func(_ context.Context, args ...string) ([]byte, []byte, error) {
		return nil, []byte("boom while typing " + strings.Join(args, " ")), errors.New("exit status 1 " + strings.Join(args, " "))
	})
	defer restore()
	_, err := (&AndroidAdapter{}).InputTextContext(context.Background(), "SER", "hunter2 pw", TextModeText)
	if err == nil {
		t.Fatal("want error")
	}
	for _, leak := range []string{"hunter2", "hunter2%spw"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error leaks secret: %v", err)
		}
	}
}

func TestInputTextContext_RunsSteps(t *testing.T) {
	var calls [][]string
	restore := SetAndroidAdbForTest(func(_ context.Context, args ...string) ([]byte, []byte, error) {
		calls = append(calls, args)
		return nil, nil, nil
	})
	defer restore()
	res, err := (&AndroidAdapter{}).InputTextContext(context.Background(), "SER", "a\nb", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Steps != 3 || res.Chars != 3 || len(calls) != 3 || calls[1][4] != "keyevent" {
		t.Fatalf("res=%+v calls=%q", res, calls)
	}
}

const sampleDump = `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation="0"><node index="0" text="" resource-id="" class="android.widget.FrameLayout" package="com.android.settings" content-desc="" checkable="false" checked="false" clickable="false" enabled="true" focusable="false" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[0,0][1080,2400]"><node index="0" text="" resource-id="" class="android.widget.LinearLayout" package="com.android.settings" content-desc="" checkable="false" checked="false" clickable="false" enabled="true" focusable="false" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[0,0][1080,2400]"><node index="0" text="Search settings" resource-id="com.android.settings:id/search_action_bar_title" class="android.widget.TextView" package="com.android.settings" content-desc="" checkable="false" checked="false" clickable="true" enabled="true" focusable="true" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[40,200][1040,320]" /><node index="1" text="secret!" resource-id="com.android.settings:id/pw" class="android.widget.EditText" package="com.android.settings" content-desc="" checkable="false" checked="false" clickable="true" enabled="true" focusable="true" focused="true" scrollable="false" long-clickable="false" password="true" selected="false" bounds="[40,400][1040,520]" /><node index="2" text="Network &amp; internet" resource-id="android:id/title" class="android.widget.TextView" package="com.android.settings" content-desc="" checkable="false" checked="false" clickable="false" enabled="true" focusable="false" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[40,600][1040,700]" /><node index="3" text="Network &amp; internet" resource-id="android:id/title" class="android.widget.TextView" package="com.android.settings" content-desc="" checkable="false" checked="false" clickable="false" enabled="true" focusable="false" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[40,800][1040,900]" /><node index="4" text="" resource-id="" class="android.widget.ImageButton" package="com.android.settings" content-desc="Navigate up" checkable="false" checked="false" clickable="true" enabled="true" focusable="true" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[0,60][120,180]" /><node index="5" text="ghost" resource-id="" class="android.widget.TextView" package="com.android.settings" content-desc="" checkable="false" checked="false" clickable="false" enabled="true" focusable="false" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[0,0][0,0]" /></node></node></hierarchy>UI hierchary dumped to: /dev/tty`

func TestParseUIAutomatorXML(t *testing.T) {
	tree, err := ParseUIAutomatorXML([]byte(sampleDump))
	if err != nil {
		t.Fatal(err)
	}
	if tree.Package != "com.android.settings" || len(tree.Nodes) != 1 {
		t.Fatalf("tree=%+v", tree)
	}
	if n := CountUINodes(tree.Nodes); n != 8 {
		t.Fatalf("count=%d", n)
	}
	search := tree.Nodes[0].Children[0].Children[0]
	if search.Text != "Search settings" || !search.Clickable || search.Bounds != [4]int{40, 200, 1040, 320} || search.Center != [2]int{540, 260} {
		t.Fatalf("search=%+v", search)
	}
	pw := tree.Nodes[0].Children[0].Children[1]
	if !pw.Password || pw.Text != "[redacted]" || !pw.Focused {
		t.Fatalf("password node not redacted: %+v", pw)
	}
	if got := tree.Nodes[0].Children[0].Children[2].Text; got != "Network & internet" {
		t.Fatalf("entity decode: %q", got)
	}
}

func TestParseUIAutomatorXML_Errors(t *testing.T) {
	if _, err := ParseUIAutomatorXML([]byte("ERROR: could not get idle state.")); !errors.Is(err, ErrUIIdle) {
		t.Fatalf("want ErrUIIdle, got %v", err)
	}
	if _, err := ParseUIAutomatorXML([]byte("garbage")); err == nil {
		t.Fatal("want error")
	}
	if _, err := ParseUIAutomatorXML([]byte(`<hierarchy rotation="0"><node bounds="[0,0][1,1]">`)); err == nil {
		t.Fatal("want truncated error")
	}
}

func TestCompactUITree(t *testing.T) {
	tree, _ := ParseUIAutomatorXML([]byte(sampleDump))
	c := CompactUITree(tree)
	// Both layout wrappers dropped and the zero-area ghost removed.
	if len(c.Nodes) != 5 {
		t.Fatalf("compact top-level=%d: %+v", len(c.Nodes), c.Nodes)
	}
	for _, n := range c.Nodes {
		if n.Text == "ghost" || strings.HasSuffix(n.Class, "Layout") {
			t.Fatalf("unexpected node kept: %+v", n)
		}
	}
	if CountUINodes(tree.Nodes) != 8 {
		t.Fatal("compact mutated input")
	}
}

func TestFindUINodes(t *testing.T) {
	tree, _ := ParseUIAutomatorXML([]byte(sampleDump))
	cases := []struct {
		sel  UISelector
		want int
	}{
		{UISelector{Text: "Search settings"}, 1},
		{UISelector{Text: "search", Contains: true}, 1},
		{UISelector{Text: "Network & internet"}, 2},
		{UISelector{ResourceID: "search_action_bar_title"}, 1},
		{UISelector{ResourceID: "id/search_action_bar_title"}, 1},
		{UISelector{ResourceID: "com.android.settings:id/search_action_bar_title"}, 1},
		{UISelector{ResourceID: "title"}, 2},
		{UISelector{Desc: "Navigate up"}, 1},
		{UISelector{Text: "ghost"}, 0}, // zero-area
		{UISelector{Text: "nope"}, 0},
		{UISelector{}, 0},
	}
	for _, c := range cases {
		if got := len(FindUINodes(tree, c.sel)); got != c.want {
			t.Errorf("%+v: got %d want %d", c.sel, got, c.want)
		}
	}
}

func TestUITreeContext_RetriesIdle(t *testing.T) {
	prevB := uiDumpBackoff
	uiDumpBackoff = 0
	defer func() { uiDumpBackoff = prevB }()
	n := 0
	restore := SetAndroidAdbForTest(func(_ context.Context, args ...string) ([]byte, []byte, error) {
		if args[2] == "exec-out" && args[3] == "uiautomator" {
			n++
			if n < 3 {
				return []byte("ERROR: could not get idle state."), nil, nil
			}
			return []byte(sampleDump), nil, nil
		}
		return nil, nil, errors.New("unexpected " + strings.Join(args, " "))
	})
	defer restore()
	tree, err := (&AndroidAdapter{}).UITreeContext(context.Background(), "SER")
	if err != nil || n != 3 || tree.Package != "com.android.settings" {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestUITreeContext_FileFallback(t *testing.T) {
	var seen []string
	restore := SetAndroidAdbForTest(func(_ context.Context, args ...string) ([]byte, []byte, error) {
		seen = append(seen, strings.Join(args[2:], " "))
		switch {
		case args[2] == "exec-out" && args[3] == "uiautomator":
			return []byte("UI hierchary dumped to: /dev/tty"), nil, nil // no XML (old builds)
		case args[2] == "exec-out" && args[3] == "cat":
			return []byte(sampleDump), nil, nil
		}
		return nil, nil, nil
	})
	defer restore()
	tree, err := (&AndroidAdapter{}).UITreeContext(context.Background(), "SER")
	if err != nil || len(tree.Nodes) != 1 {
		t.Fatalf("err=%v seen=%q", err, seen)
	}
	if !strings.Contains(strings.Join(seen, "\n"), "rm -f /data/local/tmp/spyder-ui-dump.xml") {
		t.Fatalf("temp file not removed: %q", seen)
	}
}

const dumpsysAOSP = `WINDOW MANAGER WINDOWS (dumpsys window windows)
  Window #0 Window{a62e385 u0 ScreenDecorOverlayBottom}:
    mAttrs={(0,0)(fillx100) ty=NAVIGATION_BAR_PANEL fmt=TRANSLUCENT
      fl=NOT_FOCUSABLE NOT_TOUCH_MODAL LAYOUT_IN_SCREEN FLAG_SLIPPERY
      pfl=SHOW_FOR_ALL_USERS
  Window #7 Window{1b2c3d4 u0 com.bank.app/com.bank.app.MainActivity}:
    mAttrs={(0,0)(fillxfill) ty=BASE_APPLICATION
      fl=LAYOUT_IN_SCREEN SECURE HARDWARE_ACCELERATED
      pfl=NO_MOVE_ANIMATION
        fl=NOT_FOCUSABLE
  mCurrentFocus=Window{1b2c3d4 u0 com.bank.app/com.bank.app.MainActivity}
`

const dumpsysSamsung = `  Window #11 Window{586fc88 u0 com.sec.android.app.launcher/com.sec.android.app.launcher.activities.LauncherActivity}:
    mAttrs={(0,0)(fillxfill) ty=BASE_APPLICATION fmt=TRANSPARENT wanim=0x1030301
      fl=81910100
      pfl=10008840
  Window #12 Window{aaa111 u0 com.wallet/com.wallet.Pay}:
    mAttrs={(0,0)(fillxfill) ty=BASE_APPLICATION
      fl=81012100
  mCurrentFocus=Window{586fc88 u0 com.sec.android.app.launcher/com.sec.android.app.launcher.activities.LauncherActivity}
`

func TestParseDumpsysWindowSecure(t *testing.T) {
	a := ParseDumpsysWindowSecure(dumpsysAOSP)
	if !a.FocusedSecure || a.Package != "com.bank.app" || len(a.SecureWindows) != 1 {
		t.Fatalf("aosp: %+v", a)
	}
	s := ParseDumpsysWindowSecure(dumpsysSamsung)
	if s.FocusedSecure || s.Package != "com.sec.android.app.launcher" || s.FocusedFlags != "81910100" {
		t.Fatalf("samsung focused: %+v", s)
	}
	if len(s.SecureWindows) != 1 || s.SecureWindows[0].Name != "com.wallet/com.wallet.Pay" {
		t.Fatalf("samsung secure windows (0x2000 in 81012100): %+v", s.SecureWindows)
	}
	if e := ParseDumpsysWindowSecure(""); e.FocusedWindow != "" || e.FocusedSecure {
		t.Fatalf("empty: %+v", e)
	}
}

func TestFlagsSecure(t *testing.T) {
	for in, want := range map[string]bool{
		"SECURE": true, "LAYOUT_IN_SCREEN SECURE": true, "FLAG_SECURE": true,
		"2000": true, "0x00002000": true, "81910100": false, "1000208": false,
		"NOT_FOCUSABLE": false, "SECURE_X": false,
	} {
		if got := flagsSecure(in); got != want {
			t.Errorf("flagsSecure(%q)=%v want %v", in, got, want)
		}
	}
}

func pngOf(t *testing.T, c color.Color) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 100, 200))
	for y := range 200 {
		for x := range 100 {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPNGIsBlack(t *testing.T) {
	if b, err := PNGIsBlack(pngOf(t, color.Black)); err != nil || !b {
		t.Fatalf("black: %v %v", b, err)
	}
	if b, err := PNGIsBlack(pngOf(t, color.RGBA{200, 200, 200, 255})); err != nil || b {
		t.Fatalf("grey: %v %v", b, err)
	}
	if _, err := PNGIsBlack([]byte("nope")); err == nil {
		t.Fatal("want decode error")
	}
}

func TestSecureVerdict(t *testing.T) {
	tr, fa := true, false
	cases := []struct {
		in   ScreenSecureInfo
		want string
	}{
		{ScreenSecureInfo{WindowSecureInfo: WindowSecureInfo{FocusedWindow: "x", FocusedSecure: true}}, "secure"},
		{ScreenSecureInfo{WindowSecureInfo: WindowSecureInfo{FocusedWindow: "x"}, ScreenAwake: &fa}, "screen_off"},
		{ScreenSecureInfo{WindowSecureInfo: WindowSecureInfo{FocusedWindow: "x"}, ScreenAwake: &tr, ScreenshotBlack: &tr}, "likely_secure"},
		{ScreenSecureInfo{WindowSecureInfo: WindowSecureInfo{FocusedWindow: "x"}, ScreenAwake: &tr, ScreenshotBlack: &fa}, "not_secure"},
		{ScreenSecureInfo{}, "unknown"},
	}
	for i, c := range cases {
		if got := secureVerdict(c.in); got != c.want {
			t.Errorf("%d: got %s want %s", i, got, c.want)
		}
	}
}

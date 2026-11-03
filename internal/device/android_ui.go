// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// 🎯T180 — Android native-app UI verbs: text entry (`input text` /
// keyevent fallback), accessibility tree via `uiautomator dump`, and a
// FLAG_SECURE probe via `dumpsys window` + an all-black screencap
// heuristic. These let spyder drive third-party native apps that have no
// cooperative app channel. Text passed to InputTextContext may be a
// secret (PIN, password): nothing in this file logs it, and errors are
// scrubbed of it before they leave.

package device

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------
// Text input
// ---------------------------------------------------------------------

// Text input modes.
const (
	TextModeAuto = "auto" // digits-only → keyevents; otherwise `input text`
	TextModeText = "text" // `input text` (with \n/\t as ENTER/TAB keyevents)
	TextModeKeys = "keys" // keyevents only (digits, a-z, space, a few symbols)
)

// maxTextChunk bounds a single `input text` argument. Long strings are
// split so no single shell command line grows unbounded.
const maxTextChunk = 200

// androidCharKeyCodes maps characters to KEYCODE_* values for keys mode.
// Only characters with an unshifted key are listed; anything else must
// go through `input text`.
var androidCharKeyCodes = func() map[rune]int {
	m := map[rune]int{
		' ': 62, '\n': 66, '\t': 61,
		'.': 56, ',': 55, '-': 69, '=': 70, '@': 77, '/': 76,
		'+': 81, '*': 17, '#': 18, ';': 74, '\'': 75, '\\': 73,
		'[': 71, ']': 72, '`': 68,
	}
	for r := '0'; r <= '9'; r++ {
		m[r] = 7 + int(r-'0') // KEYCODE_0 = 7
	}
	for r := 'a'; r <= 'z'; r++ {
		m[r] = 29 + int(r-'a') // KEYCODE_A = 29
	}
	return m
}()

// TextInputPlan is the sequence of `adb shell` invocations that types a
// string. Each step is the argv that follows `adb -s <id> shell`.
// Plans contain the secret text; never log them.
type TextInputPlan struct {
	Mode  string     // resolved mode (auto resolves to text or keys)
	Steps [][]string // shell argv per step
}

var digitsOnly = regexp.MustCompile(`^[0-9]+$`)

// pinLike matches strings that look like PINs / OTPs / card fragments.
// Such text is redacted even when the caller did not set secret.
var pinLike = regexp.MustCompile(`^[0-9 ]{3,24}$`)

// LooksSecret reports whether text should be treated as secret even when
// the caller did not ask: PIN-like digit strings (3–24 digits/spaces).
func LooksSecret(text string) bool {
	return pinLike.MatchString(text) && strings.TrimSpace(text) != ""
}

// PlanTextInput builds the adb steps that type text in the given mode.
func PlanTextInput(text, mode string) (TextInputPlan, error) {
	if text == "" {
		return TextInputPlan{}, errors.New("text is empty")
	}
	switch mode {
	case "", TextModeAuto:
		if digitsOnly.MatchString(text) {
			mode = TextModeKeys
		} else {
			mode = TextModeText
		}
	case TextModeText, TextModeKeys:
	default:
		return TextInputPlan{}, fmt.Errorf("mode must be auto, text, or keys (got %q)", mode)
	}
	if mode == TextModeKeys {
		codes := make([]string, 0, len(text))
		for i, r := range text {
			c, ok := androidCharKeyCodes[r]
			if !ok {
				// Position only — never echo the character itself.
				return TextInputPlan{}, fmt.Errorf("character at position %d has no unshifted keycode; use mode=text", i)
			}
			codes = append(codes, strconv.Itoa(c))
		}
		var steps [][]string
		for len(codes) > 0 {
			n := min(len(codes), 64)
			steps = append(steps, append([]string{"input", "keyevent"}, codes[:n]...))
			codes = codes[n:]
		}
		return TextInputPlan{Mode: mode, Steps: steps}, nil
	}

	// Text mode: printable ASCII runs go through `input text`; \n and \t
	// become ENTER / TAB keyevents.
	var steps [][]string
	var run []byte
	flush := func() {
		for _, chunk := range splitTextRun(string(run)) {
			steps = append(steps, []string{"input", "text", shellQuote(encodeInputText(chunk))})
		}
		run = run[:0]
	}
	for i, r := range text {
		switch {
		case r == '\n' || r == '\t':
			if len(run) > 0 {
				flush()
			}
			steps = append(steps, []string{"input", "keyevent", strconv.Itoa(androidCharKeyCodes[r])})
		case r >= 0x20 && r <= 0x7e:
			run = append(run, byte(r))
		default:
			return TextInputPlan{}, fmt.Errorf("character at position %d is not printable ASCII; adb input text cannot type it", i)
		}
	}
	if len(run) > 0 {
		flush()
	}
	return TextInputPlan{Mode: mode, Steps: steps}, nil
}

// splitTextRun splits a printable-ASCII run into `input text` chunks.
// Android's `input text` turns the two-character sequence "%s" into a
// space, so a literal '%' followed by 's' must end one invocation and
// the 's' start the next. Chunks are also capped at maxTextChunk.
func splitTextRun(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		cut := false
		if i > start && s[i] == 's' && s[i-1] == '%' {
			cut = true
		}
		if i-start >= maxTextChunk {
			cut = true
		}
		if cut {
			out = append(out, s[start:i])
			start = i
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// encodeInputText applies `input text`'s own encoding: spaces become %s.
func encodeInputText(s string) string {
	return strings.ReplaceAll(s, " ", "%s")
}

// shellQuote single-quotes s for the device's /system/bin/sh (adb shell
// joins argv with spaces and hands it to sh -c).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// scrubSecret removes every occurrence of the secret (and its encoded
// forms) from msg.
func scrubSecret(msg, text string, plan TextInputPlan) string {
	if text == "" {
		return msg
	}
	repl := []string{text, encodeInputText(text)}
	for _, st := range plan.Steps {
		if len(st) >= 3 && st[1] == "text" {
			repl = append(repl, st[2], strings.Trim(st[2], "'"))
		}
	}
	for _, r := range repl {
		if len(r) >= 1 {
			msg = strings.ReplaceAll(msg, r, "[redacted]")
		}
	}
	return msg
}

// TextInputResult describes what InputTextContext did — never the text.
type TextInputResult struct {
	Mode  string `json:"mode"`
	Steps int    `json:"steps"`
	Chars int    `json:"chars"`
}

// InputTextContext types text into the focused field. mode is auto,
// text, or keys. The text never appears in returned errors.
func (a *AndroidAdapter) InputTextContext(ctx context.Context, id, text, mode string) (TextInputResult, error) {
	if id == "" {
		return TextInputResult{}, errors.New("device identifier is empty")
	}
	plan, err := PlanTextInput(text, mode)
	if err != nil {
		return TextInputResult{}, err
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return TextInputResult{}, fmt.Errorf("adb not found in PATH: %w", err)
	}
	res := TextInputResult{Mode: plan.Mode, Steps: len(plan.Steps), Chars: len([]rune(text))}
	for i, step := range plan.Steps {
		args := append([]string{"-s", id, "shell"}, step...)
		stdout, stderr, err := androidAdb(ctx, args...)
		msg := strings.TrimSpace(string(stderr) + " " + string(stdout))
		if err == nil && strings.Contains(strings.ToLower(msg), "exception") {
			err = errors.New("input command reported an exception")
		}
		if err != nil {
			if isAndroidDeviceNotConnected(msg) {
				return res, fmt.Errorf("device not connected: %s", id)
			}
			return res, fmt.Errorf("adb input step %d/%d failed: %s\n%s",
				i+1, len(plan.Steps), scrubSecret(err.Error(), text, plan),
				truncate(scrubSecret(msg, text, plan), 200))
		}
	}
	return res, nil
}

// InjectKeyEventsContext sends one or more keycodes in a single
// `input keyevent` invocation.
func (a *AndroidAdapter) InjectKeyEventsContext(ctx context.Context, id string, codes []int) error {
	if id == "" {
		return errors.New("device identifier is empty")
	}
	if len(codes) == 0 {
		return errors.New("no keycodes")
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return fmt.Errorf("adb not found in PATH: %w", err)
	}
	args := []string{"-s", id, "shell", "input", "keyevent"}
	for _, c := range codes {
		if c < 0 {
			return fmt.Errorf("invalid keycode %d", c)
		}
		args = append(args, strconv.Itoa(c))
	}
	_, stderr, err := androidAdb(ctx, args...)
	if err != nil {
		msg := string(stderr)
		if isAndroidDeviceNotConnected(msg) {
			return fmt.Errorf("device not connected: %s", id)
		}
		return fmt.Errorf("adb input keyevent: %v\n%s", err, truncate(msg, 200))
	}
	return nil
}

// ---------------------------------------------------------------------
// UI tree (uiautomator dump)
// ---------------------------------------------------------------------

// UINode is one accessibility node from `uiautomator dump`.
type UINode struct {
	Class         string    `json:"class,omitempty"`
	Text          string    `json:"text,omitempty"`
	ContentDesc   string    `json:"content-desc,omitempty"`
	ResourceID    string    `json:"resource-id,omitempty"`
	Package       string    `json:"package,omitempty"`
	Bounds        [4]int    `json:"bounds"`
	Center        [2]int    `json:"center"`
	Clickable     bool      `json:"clickable"`
	Enabled       bool      `json:"enabled"`
	Focused       bool      `json:"focused"`
	Checked       bool      `json:"checked"`
	Password      bool      `json:"password"`
	Checkable     bool      `json:"checkable,omitempty"`
	Focusable     bool      `json:"focusable,omitempty"`
	Scrollable    bool      `json:"scrollable,omitempty"`
	LongClickable bool      `json:"long-clickable,omitempty"`
	Selected      bool      `json:"selected,omitempty"`
	Hint          string    `json:"hint,omitempty"`
	Children      []*UINode `json:"children,omitempty"`
}

// UITree is a parsed hierarchy.
type UITree struct {
	Rotation int       `json:"rotation"`
	Package  string    `json:"package,omitempty"` // package of the first top-level node
	Nodes    []*UINode `json:"nodes"`
}

// Width / Height of the node's bounds.
func (n *UINode) Width() int  { return n.Bounds[2] - n.Bounds[0] }
func (n *UINode) Height() int { return n.Bounds[3] - n.Bounds[1] }

var boundsRe = regexp.MustCompile(`^\[(-?\d+),(-?\d+)\]\[(-?\d+),(-?\d+)\]$`)

func parseBounds(s string) ([4]int, error) {
	m := boundsRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return [4]int{}, fmt.Errorf("bad bounds %q", s)
	}
	var b [4]int
	for i := range 4 {
		b[i], _ = strconv.Atoi(m[i+1])
	}
	return b, nil
}

// ErrUIIdle is returned when uiautomator cannot reach an idle state
// (continuous animation); callers may retry.
var ErrUIIdle = errors.New("uiautomator could not get idle state")

// ParseUIAutomatorXML parses `uiautomator dump` output. Leading/trailing
// noise (e.g. "UI hierchary dumped to: /dev/tty") is tolerated. Text of
// password nodes is redacted.
func ParseUIAutomatorXML(raw []byte) (UITree, error) {
	s := string(raw)
	if strings.Contains(strings.ToLower(s), "could not get idle state") {
		return UITree{}, ErrUIIdle
	}
	start := strings.Index(s, "<hierarchy")
	end := strings.LastIndex(s, "</hierarchy>")
	if start < 0 {
		return UITree{}, fmt.Errorf("no <hierarchy> in uiautomator output: %s", truncate(s, 200))
	}
	if end < 0 {
		// Self-closing empty hierarchy, or truncated output.
		if strings.Contains(s[start:], "/>") && !strings.Contains(s[start:], "<node") {
			return UITree{Nodes: []*UINode{}}, nil
		}
		return UITree{}, errors.New("truncated uiautomator output (no </hierarchy>)")
	}
	s = s[start : end+len("</hierarchy>")]
	dec := xml.NewDecoder(strings.NewReader(s))
	var tree UITree
	tree.Nodes = []*UINode{}
	var stack []*UINode
	for {
		tok, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return UITree{}, fmt.Errorf("parse uiautomator xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "hierarchy":
				for _, a := range t.Attr {
					if a.Name.Local == "rotation" {
						tree.Rotation, _ = strconv.Atoi(a.Value)
					}
				}
			case "node":
				n := &UINode{}
				for _, a := range t.Attr {
					v := a.Value
					switch a.Name.Local {
					case "class":
						n.Class = v
					case "text":
						n.Text = v
					case "content-desc":
						n.ContentDesc = v
					case "resource-id":
						n.ResourceID = v
					case "package":
						n.Package = v
					case "hint":
						n.Hint = v
					case "bounds":
						if b, err := parseBounds(v); err == nil {
							n.Bounds = b
						}
					case "clickable":
						n.Clickable = v == "true"
					case "enabled":
						n.Enabled = v == "true"
					case "focused":
						n.Focused = v == "true"
					case "checked":
						n.Checked = v == "true"
					case "password":
						n.Password = v == "true"
					case "checkable":
						n.Checkable = v == "true"
					case "focusable":
						n.Focusable = v == "true"
					case "scrollable":
						n.Scrollable = v == "true"
					case "long-clickable":
						n.LongClickable = v == "true"
					case "selected":
						n.Selected = v == "true"
					}
				}
				n.Center = [2]int{(n.Bounds[0] + n.Bounds[2]) / 2, (n.Bounds[1] + n.Bounds[3]) / 2}
				if n.Password && n.Text != "" {
					n.Text = "[redacted]"
				}
				if len(stack) == 0 {
					tree.Nodes = append(tree.Nodes, n)
					if tree.Package == "" {
						tree.Package = n.Package
					}
				} else {
					p := stack[len(stack)-1]
					p.Children = append(p.Children, n)
				}
				stack = append(stack, n)
			}
		case xml.EndElement:
			if t.Name.Local == "node" && len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return tree, nil
}

// meaningful reports whether a node carries information or affordance
// worth keeping in a compact tree.
func (n *UINode) meaningful() bool {
	if n.Width() <= 0 || n.Height() <= 0 {
		return false
	}
	return n.Text != "" || n.ContentDesc != "" || n.Hint != "" || n.Clickable || n.LongClickable ||
		n.Scrollable || n.Checkable || n.Focused || n.Password ||
		strings.HasSuffix(n.Class, "EditText")
}

// CompactUITree drops empty layout nodes, hoisting their children into
// the nearest kept ancestor. The input tree is not modified.
func CompactUITree(t UITree) UITree {
	out := t
	out.Nodes = compactNodes(t.Nodes)
	if out.Nodes == nil {
		out.Nodes = []*UINode{}
	}
	return out
}

func compactNodes(ns []*UINode) []*UINode {
	var out []*UINode
	for _, n := range ns {
		kids := compactNodes(n.Children)
		if n.meaningful() {
			c := *n
			c.Children = kids
			out = append(out, &c)
		} else {
			out = append(out, kids...)
		}
	}
	return out
}

// WalkUITree calls fn for every node depth-first.
func WalkUITree(ns []*UINode, fn func(*UINode)) {
	for _, n := range ns {
		fn(n)
		WalkUITree(n.Children, fn)
	}
}

// CountUINodes returns the number of nodes in ns (recursively).
func CountUINodes(ns []*UINode) int {
	c := 0
	WalkUITree(ns, func(*UINode) { c++ })
	return c
}

// UISelector picks nodes for ui_tap. Empty fields are ignored; at least
// one of Text, ResourceID, Desc must be set.
type UISelector struct {
	Text       string
	ResourceID string
	Desc       string
	Contains   bool // case-insensitive substring match for Text/Desc
}

func (s UISelector) Empty() bool { return s.Text == "" && s.ResourceID == "" && s.Desc == "" }

func matchStr(have, want string, contains bool) bool {
	if contains {
		return strings.Contains(strings.ToLower(have), strings.ToLower(want))
	}
	return have == want
}

// Match reports whether n satisfies the selector.
func (s UISelector) Match(n *UINode) bool {
	if s.Empty() {
		return false
	}
	if s.Text != "" && !matchStr(n.Text, s.Text, s.Contains) {
		return false
	}
	if s.Desc != "" && !matchStr(n.ContentDesc, s.Desc, s.Contains) {
		return false
	}
	if s.ResourceID != "" {
		rid := n.ResourceID
		want := s.ResourceID
		ok := rid == want
		if !ok && !strings.Contains(want, ":") {
			// Short forms: "id/foo" or "foo" match "pkg:id/foo".
			ok = strings.HasSuffix(rid, ":"+want) || strings.HasSuffix(rid, ":id/"+want)
		}
		if !ok {
			return false
		}
	}
	return true
}

// FindUINodes returns every on-screen node matching sel.
func FindUINodes(t UITree, sel UISelector) []*UINode {
	var out []*UINode
	WalkUITree(t.Nodes, func(n *UINode) {
		if n.Width() > 0 && n.Height() > 0 && sel.Match(n) {
			out = append(out, n)
		}
	})
	return out
}

// dumpUITreeOnce tries exec-out to stdout, then falls back to a temp file.
func dumpUITreeOnce(ctx context.Context, id string) ([]byte, error) {
	stdout, stderr, err := androidAdb(ctx, "-s", id, "exec-out", "uiautomator", "dump", "/dev/tty")
	if err == nil && bytes.Contains(stdout, []byte("<hierarchy")) {
		return stdout, nil
	}
	combined := string(stderr) + string(stdout)
	if isAndroidDeviceNotConnected(combined) {
		return nil, fmt.Errorf("device not connected: %s", id)
	}
	if strings.Contains(strings.ToLower(combined), "could not get idle state") {
		return nil, ErrUIIdle
	}
	// Fallback: dump to a device temp file and cat it back.
	const tmp = "/data/local/tmp/spyder-ui-dump.xml"
	stdout, stderr, err = androidAdb(ctx, "-s", id, "shell", "uiautomator", "dump", tmp)
	combined = string(stderr) + string(stdout)
	if strings.Contains(strings.ToLower(combined), "could not get idle state") {
		return nil, ErrUIIdle
	}
	if err != nil {
		return nil, fmt.Errorf("uiautomator dump: %v\n%s", err, truncate(combined, 200))
	}
	out, stderr, err := androidAdb(ctx, "-s", id, "exec-out", "cat", tmp)
	_, _, _ = androidAdb(ctx, "-s", id, "shell", "rm", "-f", tmp)
	if err != nil {
		return nil, fmt.Errorf("read ui dump: %v\n%s", err, truncate(string(stderr), 200))
	}
	return out, nil
}

// uiDumpRetries / uiDumpBackoff control idle-state retries.
var (
	uiDumpRetries = 3
	uiDumpBackoff = 700 * time.Millisecond
)

// UITreeContext dumps and parses the current accessibility hierarchy,
// retrying when uiautomator cannot reach an idle state.
func (a *AndroidAdapter) UITreeContext(ctx context.Context, id string) (UITree, error) {
	if id == "" {
		return UITree{}, errors.New("device identifier is empty")
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return UITree{}, fmt.Errorf("adb not found in PATH: %w", err)
	}
	var lastErr error
	for attempt := 0; attempt < uiDumpRetries; attempt++ {
		if attempt > 0 {
			if err := androidSleepUntil(ctx, uiDumpBackoff); err != nil {
				return UITree{}, err
			}
		}
		raw, err := dumpUITreeOnce(ctx, id)
		if err == nil {
			var t UITree
			t, err = ParseUIAutomatorXML(raw)
			if err == nil {
				return t, nil
			}
		}
		lastErr = err
		if !errors.Is(err, ErrUIIdle) {
			return UITree{}, err
		}
	}
	return UITree{}, fmt.Errorf("%w after %d attempts (screen is animating; pause video/animations and retry)", lastErr, uiDumpRetries)
}

// ---------------------------------------------------------------------
// FLAG_SECURE
// ---------------------------------------------------------------------

// flagSecure is WindowManager.LayoutParams.FLAG_SECURE.
const flagSecure = 0x00002000

// SecureWindow is one window in `dumpsys window` whose flags include SECURE.
type SecureWindow struct {
	Hash string `json:"hash"`
	Name string `json:"name"`
}

// WindowSecureInfo is the parsed result of `dumpsys window`.
type WindowSecureInfo struct {
	FocusedWindow string         `json:"focused_window"`
	FocusedHash   string         `json:"-"`
	Package       string         `json:"package,omitempty"`
	FocusedFlags  string         `json:"focused_flags,omitempty"`
	FocusedSecure bool           `json:"secure_flag"`
	SecureWindows []SecureWindow `json:"secure_windows"`
}

var (
	currentFocusRe = regexp.MustCompile(`mCurrentFocus=Window\{(\S+) \S+ ([^}]*)\}`)
	windowHeaderRe = regexp.MustCompile(`^\s*Window #\d+ Window\{(\S+) \S+ ([^}]*)\}:`)
	flagsLineRe    = regexp.MustCompile(`^\s*fl=(.*)$`)
	hexFlagsRe     = regexp.MustCompile(`^(?:0x)?[0-9a-fA-F]+$`)
)

// flagsSecure interprets an `fl=` value. AOSP prints symbolic names
// ("fl=NOT_FOCUSABLE SECURE ..."); some OEM builds (Samsung One UI)
// print raw hex ("fl=81910100").
func flagsSecure(fl string) bool {
	fl = strings.TrimSpace(fl)
	if hexFlagsRe.MatchString(fl) {
		v, err := strconv.ParseUint(strings.TrimPrefix(fl, "0x"), 16, 64)
		return err == nil && v&flagSecure != 0
	}
	for _, tok := range strings.Fields(fl) {
		if tok == "SECURE" || tok == "FLAG_SECURE" {
			return true
		}
	}
	return false
}

// ParseDumpsysWindowSecure finds the focused window and every window
// whose flags include FLAG_SECURE.
func ParseDumpsysWindowSecure(out string) WindowSecureInfo {
	info := WindowSecureInfo{SecureWindows: []SecureWindow{}}
	if m := currentFocusRe.FindStringSubmatch(out); m != nil {
		info.FocusedHash = m[1]
		info.FocusedWindow = m[2]
		if i := strings.Index(m[2], "/"); i > 0 {
			info.Package = m[2][:i]
		}
	}
	var curHash, curName string
	gotFlags := false
	for _, line := range strings.Split(out, "\n") {
		if m := windowHeaderRe.FindStringSubmatch(line); m != nil {
			curHash, curName, gotFlags = m[1], m[2], false
			continue
		}
		if curHash == "" || gotFlags {
			continue
		}
		if m := flagsLineRe.FindStringSubmatch(line); m != nil {
			gotFlags = true // first fl= in a block is the window's own mAttrs
			fl := strings.TrimSpace(m[1])
			sec := flagsSecure(fl)
			if curHash == info.FocusedHash {
				info.FocusedFlags = fl
				info.FocusedSecure = sec
			}
			if sec {
				info.SecureWindows = append(info.SecureWindows, SecureWindow{Hash: curHash, Name: curName})
			}
		}
	}
	return info
}

// PNGIsBlack reports whether a PNG is (near-)uniformly black, sampling a
// grid of points. Secure surfaces screencap as solid black.
func PNGIsBlack(data []byte) (bool, error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return false, err
	}
	return imageIsBlack(img), nil
}

func imageIsBlack(img image.Image) bool {
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return false
	}
	const grid = 48
	for gy := range grid {
		for gx := range grid {
			x := b.Min.X + (b.Dx()-1)*gx/(grid-1)
			y := b.Min.Y + (b.Dy()-1)*gy/(grid-1)
			r, g, bb, _ := img.At(x, y).RGBA()
			if r>>8 > 12 || g>>8 > 12 || bb>>8 > 12 {
				return false
			}
		}
	}
	return true
}

// ScreenSecureInfo is the screen_secure result.
type ScreenSecureInfo struct {
	WindowSecureInfo
	ScreenAwake     *bool  `json:"screen_awake,omitempty"`
	ScreenshotBlack *bool  `json:"screenshot_black,omitempty"`
	ScreenshotError string `json:"screenshot_error,omitempty"`
	Verdict         string `json:"verdict"` // secure | likely_secure | not_secure | screen_off | unknown
}

var wakefulnessRe = regexp.MustCompile(`mWakefulness=(\w+)`)

// ScreenSecureContext checks FLAG_SECURE on the focused window (dumpsys
// window) and, when screenshot is true, whether a screencap is all black.
func (a *AndroidAdapter) ScreenSecureContext(ctx context.Context, id string, screenshot bool) (ScreenSecureInfo, error) {
	if id == "" {
		return ScreenSecureInfo{}, errors.New("device identifier is empty")
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return ScreenSecureInfo{}, fmt.Errorf("adb not found in PATH: %w", err)
	}
	stdout, stderr, err := androidAdb(ctx, "-s", id, "shell", "dumpsys", "window")
	if err != nil {
		msg := string(stderr)
		if isAndroidDeviceNotConnected(msg) {
			return ScreenSecureInfo{}, fmt.Errorf("device not connected: %s", id)
		}
		return ScreenSecureInfo{}, fmt.Errorf("dumpsys window: %v\n%s", err, truncate(msg, 200))
	}
	info := ScreenSecureInfo{WindowSecureInfo: ParseDumpsysWindowSecure(string(stdout))}

	if pw, _, perr := androidAdb(ctx, "-s", id, "shell", "dumpsys", "power"); perr == nil {
		if m := wakefulnessRe.FindSubmatch(pw); m != nil {
			awake := string(m[1]) == "Awake"
			info.ScreenAwake = &awake
		}
	}
	if screenshot {
		png, _, serr := androidAdb(ctx, "-s", id, "exec-out", "screencap", "-p")
		if serr != nil {
			info.ScreenshotError = truncate(serr.Error(), 200)
		} else if black, berr := PNGIsBlack(png); berr != nil {
			info.ScreenshotError = "decode screencap: " + truncate(berr.Error(), 160)
		} else {
			info.ScreenshotBlack = &black
		}
	}
	info.Verdict = secureVerdict(info)
	return info, nil
}

func secureVerdict(i ScreenSecureInfo) string {
	switch {
	case i.FocusedSecure:
		return "secure"
	case i.ScreenAwake != nil && !*i.ScreenAwake:
		return "screen_off"
	case i.ScreenshotBlack != nil && *i.ScreenshotBlack:
		return "likely_secure"
	case i.FocusedWindow == "":
		return "unknown"
	default:
		return "not_secure"
	}
}

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/marcelocantos/spyder/internal/inventory"
	"github.com/marcelocantos/spyder/internal/network"
)

// DesktopAdapter runs and monitors games as local host processes — the
// simplest launch backend behind spyder's one launcher interface (🎯T85 /
// 🎯T92: a desktop host is a "device factory" whose spawn is local exec, vs
// the sim/emu pool's simctl or a game server's RPC). The app-channel monitor
// surface (state/logs/screenshot/tweaks) needs zero desktop-specific work —
// the launched process dials SPYDER_APP_CHANNEL back like any other app; this
// adapter only owns the launch/lifecycle side.
//
// A desktop "device" is an inventory entry with platform="desktop" and an
// executable_path; that path is the adapter's device id (analogous to
// ios_uuid / android_serial).
type DesktopAdapter struct {
	inv   *inventory.Store
	mu    sync.Mutex
	procs map[procKey]*desktopProc
}

// procKey identifies a launched process by (device id = executable path,
// bundle id). Keying on both mirrors the (id, bundleID) pair every lifecycle
// method receives.
type procKey struct {
	id       string
	bundleID string
}

// desktopLogBufferMax bounds the per-process captured-output ring buffer.
const desktopLogBufferMax = 5000

// desktopProc tracks one launched process: its command, captured stdout/
// stderr as timestamped log lines, live stream subscribers, and exit state.
type desktopProc struct {
	cmd *exec.Cmd
	pid int

	mu     sync.Mutex
	logs   []LogLine
	subs   map[chan<- LogLine]struct{}
	exited bool
}

// NewDesktopAdapter returns a desktop adapter. inv is used by List to
// enumerate platform="desktop" inventory entries (desktop "devices" are
// inventory-defined — there is no hardware to probe).
func NewDesktopAdapter(inv *inventory.Store) *DesktopAdapter {
	return &DesktopAdapter{inv: inv, procs: map[procKey]*desktopProc{}}
}

// List returns the platform="desktop" inventory entries as devices. Unlike
// iOS/Android there is nothing to enumerate on the wire — a desktop target
// exists iff it's in the inventory — so List reflects the inventory directly
// and stamps each entry's alias (the devices handler's AliasFor lookup keys
// on hardware UUIDs, not executable paths, so it can't fill this in for us).
func (a *DesktopAdapter) List() ([]Info, error) {
	if a.inv == nil {
		return nil, nil
	}
	var out []Info
	for _, e := range a.inv.Entries() {
		if e.Platform != "desktop" {
			continue
		}
		out = append(out, Info{
			UUID:     e.ExecutablePath,
			Name:     e.Alias,
			Platform: "desktop",
			Alias:    e.Alias,
			Model:    filepath.Base(e.ExecutablePath),
			OS:       "desktop",
		})
	}
	return out, nil
}

// State reports process-liveness for a desktop target. Battery/thermal/
// foreground don't apply to a host process; the running app (if any) is
// surfaced as ForegroundApp, and the rest is recorded in Notes.
func (a *DesktopAdapter) State(id string) (State, error) {
	if id == "" {
		return State{}, errors.New("device identifier is empty")
	}
	st := State{Notes: []string{"desktop host — battery/thermal not applicable"}}
	a.mu.Lock()
	for k, p := range a.procs {
		if k.id == id && p.alive() {
			st.ForegroundApp = k.bundleID
			break
		}
	}
	a.mu.Unlock()
	return st, nil
}

// LaunchApp starts the executable at id (the desktop device id = executable
// path) as a local process, injecting env (which carries SPYDER_APP_CHANNEL
// so the app dials back exactly as on iOS/Android). stdout/stderr are captured
// into a bounded ring buffer for LogRange/LogStream. The process runs in its
// own group so TerminateApp can signal the whole tree. bundleID is the app
// identity used to key the process (and the app-channel listener).
func (a *DesktopAdapter) LaunchApp(id, bundleID string, env map[string]string) error {
	if id == "" || bundleID == "" {
		return errors.New("device id (executable path) and bundle_id are required")
	}
	if fi, err := os.Stat(id); err != nil {
		return fmt.Errorf("executable %q not found: %w", id, err)
	} else if fi.IsDir() {
		return fmt.Errorf("executable path %q is a directory", id)
	}

	cmd := exec.Command(id)
	cmd.Dir = filepath.Dir(id) // run from the binary's dir so asset-relative games resolve
	if wd := a.workingDirFor(id); wd != "" {
		cmd.Dir = wd
	}
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// Own process group so we can signal children on terminate.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch %q: %w", id, err)
	}

	p := &desktopProc{cmd: cmd, pid: cmd.Process.Pid, subs: map[chan<- LogLine]struct{}{}}
	key := procKey{id: id, bundleID: bundleID}

	a.mu.Lock()
	// Replace any prior (possibly-exited) process under the same key.
	a.procs[key] = p
	a.mu.Unlock()

	stdoutDone := make(chan struct{})
	stderrDone := make(chan struct{})
	go func() { p.scan(stdout, "info"); close(stdoutDone) }()
	go func() { p.scan(stderr, "error"); close(stderrDone) }()
	go func() {
		<-stdoutDone
		<-stderrDone
		_ = cmd.Wait()
		p.markExited()
	}()
	return nil
}

// TerminateApp signals the launched process's group with SIGTERM, then
// SIGKILL if it doesn't exit within a short grace period.
func (a *DesktopAdapter) TerminateApp(id, bundleID string) error {
	if id == "" || bundleID == "" {
		return errors.New("device id and bundle_id are required")
	}
	a.mu.Lock()
	p := a.procs[procKey{id: id, bundleID: bundleID}]
	a.mu.Unlock()
	if p == nil {
		return fmt.Errorf("app not running: %s", bundleID)
	}
	if !p.alive() {
		return nil
	}
	// Negative pid → signal the whole process group.
	_ = syscall.Kill(-p.pid, syscall.SIGTERM)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !p.alive() {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(-p.pid, syscall.SIGKILL)
	return nil
}

// AppPID returns the pid of the running desktop process for bundleID, or an
// error if it isn't running (never launched, or already exited).
func (a *DesktopAdapter) AppPID(id, bundleID string) (int, error) {
	if id == "" || bundleID == "" {
		return 0, errors.New("device id and bundle_id are required")
	}
	a.mu.Lock()
	p := a.procs[procKey{id: id, bundleID: bundleID}]
	a.mu.Unlock()
	if p == nil || !p.alive() {
		return 0, fmt.Errorf("app not running: %s", bundleID)
	}
	return p.pid, nil
}

// ResolveExecutable maps a desktop bundle id to the name that appears in its
// captured logs. Desktop log lines aren't process-tagged (they're this app's
// own stdout/stderr), so the lookup is an identity gated on the app being a
// launched target.
func (a *DesktopAdapter) ResolveExecutable(id, bundleID string) (string, bool, error) {
	if id == "" || bundleID == "" {
		return "", false, errors.New("device id and bundle_id are required")
	}
	return bundleID, true, nil
}

// ListApps has no meaning for desktop (there is no on-device app catalogue);
// it returns an empty list rather than an error so log/state callers that
// probe it don't fail.
func (a *DesktopAdapter) ListApps(id string) ([]AppInfo, error) { return []AppInfo{}, nil }

// LogRange returns captured stdout/stderr lines for the app in [since, until].
// The filter's Process/Tag are ignored (desktop lines carry neither); Regex is
// honoured against the message.
func (a *DesktopAdapter) LogRange(id string, filter LogFilter, since, until time.Time) ([]LogLine, error) {
	if id == "" {
		return nil, errors.New("device identifier is empty")
	}
	re, err := filter.compileRegex()
	if err != nil {
		return nil, err
	}
	var lines []LogLine
	a.mu.Lock()
	procs := make([]*desktopProc, 0, len(a.procs))
	for k, p := range a.procs {
		if k.id == id {
			procs = append(procs, p)
		}
	}
	a.mu.Unlock()
	for _, p := range procs {
		for _, ll := range p.snapshot() {
			if !since.IsZero() && ll.Timestamp.Before(since) {
				continue
			}
			if !until.IsZero() && ll.Timestamp.After(until) {
				continue
			}
			if re != nil && !re.MatchString(ll.Message) {
				continue
			}
			lines = append(lines, ll)
		}
	}
	return lines, nil
}

// LogStream delivers new captured stdout/stderr lines for the app into out
// until ctx is cancelled. Subscribes to every currently-tracked process for id.
func (a *DesktopAdapter) LogStream(ctx context.Context, id string, filter LogFilter, out chan<- LogLine) error {
	if id == "" {
		return errors.New("device identifier is empty")
	}
	re, err := filter.compileRegex()
	if err != nil {
		return err
	}
	ch := make(chan LogLine, 256)
	var subbed []*desktopProc
	a.mu.Lock()
	for k, p := range a.procs {
		if k.id == id {
			p.subscribe(ch)
			subbed = append(subbed, p)
		}
	}
	a.mu.Unlock()
	defer func() {
		for _, p := range subbed {
			p.unsubscribe(ch)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ll := <-ch:
			if re != nil && !re.MatchString(ll.Message) {
				continue
			}
			select {
			case out <- ll:
			case <-ctx.Done():
				return nil
			}
		}
	}
}

// --- unsupported-on-desktop surface ---------------------------------------
//
// These have no desktop analogue; they return a clear error rather than a
// stack trace or a silent no-op (🎯T85).

func (a *DesktopAdapter) Screenshot(id string) ([]byte, error) {
	return nil, notOnDesktop("screenshot (use app_screenshot over the app-channel)")
}
func (a *DesktopAdapter) Rotate(id, orientation string) error { return notOnDesktop("rotate") }
func (a *DesktopAdapter) Crashes(id string, since time.Time, process string) ([]CrashReport, error) {
	return nil, notOnDesktop("crashes")
}
func (a *DesktopAdapter) StartRecording(id, dest string) (func() error, int, error) {
	return nil, 0, notOnDesktop("screen recording")
}
func (a *DesktopAdapter) StopRecording(id string, pid int) error {
	return notOnDesktop("screen recording")
}
func (a *DesktopAdapter) InstallApp(id, path string) error { return notOnDesktop("install_app") }
func (a *DesktopAdapter) UninstallApp(id, bundleID string) error {
	return notOnDesktop("uninstall_app")
}
func (a *DesktopAdapter) ApplyNetwork(id string, profile network.NetworkProfile) error {
	return notOnDesktop("network shaping")
}
func (a *DesktopAdapter) ClearNetwork(id string) error { return notOnDesktop("network shaping") }

func notOnDesktop(op string) error {
	return fmt.Errorf("%s is not supported on desktop targets", op)
}

// --- desktopProc helpers ---------------------------------------------------

func (p *desktopProc) scan(r io.Reader, level string) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		p.appendLog(LogLine{Timestamp: time.Now().UTC(), Level: level, Message: sc.Text()})
	}
}

func (p *desktopProc) appendLog(ll LogLine) {
	p.mu.Lock()
	p.logs = append(p.logs, ll)
	if len(p.logs) > desktopLogBufferMax {
		p.logs = p.logs[len(p.logs)-desktopLogBufferMax:]
	}
	subs := make([]chan<- LogLine, 0, len(p.subs))
	for ch := range p.subs {
		subs = append(subs, ch)
	}
	p.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- ll:
		default: // slow subscriber — drop rather than block the scanner
		}
	}
}

func (p *desktopProc) snapshot() []LogLine {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]LogLine, len(p.logs))
	copy(out, p.logs)
	return out
}

func (p *desktopProc) subscribe(ch chan<- LogLine) {
	p.mu.Lock()
	p.subs[ch] = struct{}{}
	p.mu.Unlock()
}

func (p *desktopProc) unsubscribe(ch chan<- LogLine) {
	p.mu.Lock()
	delete(p.subs, ch)
	p.mu.Unlock()
}

func (p *desktopProc) markExited() {
	p.mu.Lock()
	p.exited = true
	p.mu.Unlock()
}

func (p *desktopProc) alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.exited
}

// workingDirFor returns the configured WorkingDir for the desktop entry whose
// ExecutablePath is execPath, or "" when none is set (caller defaults to the
// binary's directory).
func (a *DesktopAdapter) workingDirFor(execPath string) string {
	if a.inv == nil {
		return ""
	}
	for _, e := range a.inv.Entries() {
		if e.Platform == "desktop" && e.ExecutablePath == execPath {
			return e.WorkingDir
		}
	}
	return ""
}

// compileRegex compiles the filter's message regex, or returns (nil, nil) when
// no regex is set.
func (f LogFilter) compileRegex() (*regexp.Regexp, error) {
	if f.Regex == "" {
		return nil, nil
	}
	re, err := regexp.Compile(f.Regex)
	if err != nil {
		return nil, fmt.Errorf("invalid regex: %w", err)
	}
	return re, nil
}

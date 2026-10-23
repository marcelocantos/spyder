// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"bytes"
	"errors"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/marcelocantos/spyder/internal/goios"
)

// Automation phases published on device state (🎯T167).
const (
	AutomationAbsent   = "absent"
	AutomationStarting = "starting"
	AutomationReady    = "ready"
	AutomationFailed   = "failed"
)

// runwdaMaxAttempts is how many times one reappear will spawn ios runwda
// before it stops. A later disappear/reappear starts a fresh budget.
const runwdaMaxAttempts = 3

// runwdaBackoff is the gap between those attempts.
const runwdaBackoff = time.Second

// runwdaReadySettle is how long a process we spawned must stay up,
// with no startup-failure marker, before it counts as ready.
const runwdaReadySettle = 5 * time.Second

// runwdaAttemptWait bounds one spawn while it either settles or fails.
const runwdaAttemptWait = 25 * time.Second

const runwdaReservedError = "device reserved by another owner"

// RunwdaRunner is the ios runwda process table. Tests supply a fake.
// Production scans the host process list and execs the same invocation.
type RunwdaRunner interface {
	// Live reports the runwda currently associated with udid.
	// ready is false when the process is missing, still starting, or
	// stuck on a startup failure. errText is the last such failure.
	Live(udid string) (bundle string, args []string, ready bool, errText string)
	// Start spawns that invocation. A nil error means it became ready.
	Start(udid, bundle string, args []string) (pid int, err error)
}

// RunwdaBind configures reinstantiation. A nil Runner uses the host
// process table. Now and Wait default to the wall clock.
type RunwdaBind struct {
	Runner    RunwdaRunner
	IOSBinary string
	Reserved  func(udid string) bool
	Now       func() time.Time
	Wait      func(time.Duration)
}

// BindRunwda arms reinstantiation on the tunnel disappear/reappear edge.
func (a *IOSAdapter) BindRunwda(b *RunwdaBind) {
	if a == nil || b == nil {
		return
	}
	h := &runwdaHost{
		byUDID:   map[string]*runwdaDev{},
		reserved: b.Reserved,
		now:      b.Now,
		wait:     b.Wait,
	}
	if h.now == nil {
		h.now = time.Now
	}
	if b.Runner != nil {
		h.runner = b.Runner
	} else {
		h.runner = newProcRunner(b.IOSBinary)
	}
	a.bindRunwdaResolver(h)
	a.mu.Lock()
	a.runwda = h
	a.mu.Unlock()
}

// RunwdaEffectCounts reports input injects, app launches, and app
// terminates performed through this adapter.
func (a *IOSAdapter) RunwdaEffectCounts() (input, launch, terminate uint64) {
	if a == nil {
		return 0, 0, 0
	}
	return a.controlInputs.Load(), a.controlLaunches.Load(), a.controlTerminates.Load()
}

func (a *IOSAdapter) fillAutomation(id string, st *State) {
	if st == nil {
		return
	}
	phase, errText := AutomationAbsent, ""
	if a != nil && a.runwda != nil {
		phase, errText = a.runwda.report(id)
	}
	st.AutomationSession = phase
	st.AutomationError = ""
	if phase == AutomationFailed {
		st.AutomationError = errText
	}
}

func (a *IOSAdapter) runwdaAttached(udid string) {
	if a == nil || a.runwda == nil {
		return
	}
	a.runwda.attached(udid)
}

func (a *IOSAdapter) runwdaDisappeared(udid string) {
	if a == nil || a.runwda == nil {
		return
	}
	a.runwda.disappeared(udid)
}

func (a *IOSAdapter) runwdaReappeared(udid string) {
	if a == nil || a.runwda == nil {
		return
	}
	if a.automation.usesDeviceKit(udid) {
		// The resident-agent supervisor owns this device's DeviceKit
		// runner (🎯T175). A second runwda would race it for the one
		// UI-automation session.
		return
	}
	a.runwda.reappear(udid)
}

type runwdaEdge int

const (
	runwdaEdgeNone runwdaEdge = iota
	runwdaEdgeReappear
	runwdaEdgeTick
)

// runwdaView is the pure decision's input. It does not touch a tunnel
// or a process.
type runwdaView struct {
	Now             time.Time
	HasIdentity     bool
	Edge            runwdaEdge
	ReservedByOther bool
	RunnerReady     bool
	InitializerBusy bool
	Attempts        int
	LastError       string
	NextDue         time.Time
	DeviceUp        bool
}

// runwdaPlan is what the reappear loop should do next.
type runwdaPlan struct {
	Start    bool
	Phase    string
	Error    string
	NextDue  time.Time
	Attempts int
	Stop     bool
}

// decideRunwda chooses whether to spawn, which phase to publish, and
// when the next attempt is due. It does not spawn, lock, or sleep.
func decideRunwda(v *runwdaView) *runwdaPlan {
	if v == nil || !v.HasIdentity {
		return &runwdaPlan{Phase: AutomationAbsent, Stop: true}
	}
	if !v.DeviceUp {
		return &runwdaPlan{Phase: AutomationAbsent, Stop: true}
	}
	if v.RunnerReady {
		return &runwdaPlan{Phase: AutomationReady, Stop: true}
	}
	if v.ReservedByOther {
		return &runwdaPlan{Phase: AutomationFailed, Error: runwdaReservedError, Stop: true}
	}
	if v.Attempts >= runwdaMaxAttempts {
		errText := v.LastError
		if errText == "" {
			errText = "runner did not become ready"
		}
		return &runwdaPlan{Phase: AutomationFailed, Error: errText, Attempts: v.Attempts, Stop: true}
	}
	due := v.NextDue
	if v.InitializerBusy {
		if due.IsZero() || !due.After(v.Now) {
			due = v.Now.Add(runwdaBackoff)
		}
		return &runwdaPlan{Phase: AutomationStarting, NextDue: due, Attempts: v.Attempts}
	}
	if !due.IsZero() && v.Now.Before(due) {
		return &runwdaPlan{Phase: AutomationStarting, NextDue: due, Attempts: v.Attempts}
	}
	if v.Edge != runwdaEdgeReappear && v.Edge != runwdaEdgeTick {
		return &runwdaPlan{Phase: AutomationAbsent, Stop: true}
	}
	return &runwdaPlan{
		Start:    true,
		Phase:    AutomationStarting,
		NextDue:  v.Now.Add(runwdaBackoff),
		Attempts: v.Attempts + 1,
	}
}

type runwdaIdentity struct {
	UDID   string
	Bundle string
	Args   []string
	Binary string
}

type runwdaDev struct {
	id       runwdaIdentity
	phase    string
	err      string
	attempts int
	nextDue  time.Time
	deviceUp bool
	looping  bool
	gen      int
}

type runwdaHost struct {
	mu           sync.Mutex
	byUDID       map[string]*runwdaDev
	runner       RunwdaRunner
	reserved     func(string) bool
	now          func() time.Time
	wait         func(time.Duration)
	lockResolver func() *goios.Resolver
}

func (h *runwdaHost) clock() time.Time {
	if h == nil || h.now == nil {
		return time.Now()
	}
	return h.now()
}

func (h *runwdaHost) sleep(d time.Duration) {
	if d <= 0 {
		return
	}
	if h != nil && h.wait != nil {
		h.wait(d)
		return
	}
	time.Sleep(d)
}

func (h *runwdaHost) live(udid string) (bundle string, args []string, ready bool, errText string) {
	if h == nil || h.runner == nil {
		return "", nil, false, ""
	}
	return h.runner.Live(udid)
}

func (h *runwdaHost) ensure(udid string) *runwdaDev {
	d := h.byUDID[udid]
	if d == nil {
		d = &runwdaDev{phase: AutomationAbsent}
		h.byUDID[udid] = d
	}
	return d
}

func (h *runwdaHost) attached(udid string) {
	bundle, args, ready, _ := h.live(udid)
	h.mu.Lock()
	defer h.mu.Unlock()
	d := h.ensure(udid)
	d.deviceUp = true
	if bundle == "" {
		if d.id.Bundle == "" {
			d.phase = AutomationAbsent
		}
		return
	}
	d.id = runwdaIdentity{UDID: udid, Bundle: bundle, Args: append([]string(nil), args...)}
	if ready {
		d.phase = AutomationReady
		d.err = ""
		d.attempts = 0
		d.nextDue = time.Time{}
	}
}

func (h *runwdaHost) disappeared(udid string) {
	bundle, args, _, _ := h.live(udid)
	h.mu.Lock()
	defer h.mu.Unlock()
	d := h.ensure(udid)
	if d.id.Bundle == "" && bundle != "" {
		d.id = runwdaIdentity{UDID: udid, Bundle: bundle, Args: append([]string(nil), args...)}
	}
	d.deviceUp = false
	d.phase = AutomationAbsent
	d.err = ""
	d.gen++
	d.looping = false
}

func (h *runwdaHost) reappear(udid string) {
	bundle, args, ready, _ := h.live(udid)
	h.mu.Lock()
	d := h.byUDID[udid]
	if (d == nil || d.id.Bundle == "") && bundle == "" {
		if d != nil {
			d.deviceUp = true
			d.phase = AutomationAbsent
		}
		h.mu.Unlock()
		return
	}
	if d == nil {
		d = h.ensure(udid)
	}
	d.deviceUp = true
	if d.id.Bundle == "" {
		d.id = runwdaIdentity{UDID: udid, Bundle: bundle, Args: append([]string(nil), args...)}
	}
	d.gen++
	gen := d.gen
	d.attempts = 0
	d.nextDue = time.Time{}
	d.err = ""
	if ready {
		d.phase = AutomationReady
		d.looping = false
		h.mu.Unlock()
		return
	}
	d.looping = true
	d.phase = AutomationStarting
	h.mu.Unlock()
	go h.loop(udid, gen)
}

func (h *runwdaHost) current(udid string, gen int) (*runwdaDev, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	d := h.byUDID[udid]
	if d == nil || d.gen != gen || !d.deviceUp {
		return nil, false
	}
	return d, true
}

func (h *runwdaHost) loop(udid string, gen int) {
	defer func() {
		h.mu.Lock()
		if d := h.byUDID[udid]; d != nil && d.gen == gen {
			d.looping = false
		}
		h.mu.Unlock()
	}()
	edge := runwdaEdgeReappear
	for {
		if _, ok := h.current(udid, gen); !ok {
			return
		}
		plan, release := h.plan(udid, gen, edge)
		if plan == nil {
			if release != nil {
				release()
			}
			return
		}
		if plan.Stop && !plan.Start {
			if release != nil {
				release()
			}
			if plan.Phase == AutomationFailed {
				slog.Warn("runwda reinstantiate stopped", "udid", udid, "error", plan.Error, "attempts", plan.Attempts)
			}
			return
		}
		var startErr error
		if plan.Start {
			bundle, args := h.identity(udid)
			_, startErr = h.runner.Start(udid, bundle, args)
			if release != nil {
				release()
				release = nil
			}
			if startErr != nil {
				slog.Warn("runwda attempt failed", "udid", udid, "errText", startErr.Error())
			}
			if _, _, ready, errText := h.live(udid); ready && startErr == nil {
				h.publish(udid, gen, AutomationReady, "")
				slog.Info("runwda ready", "udid", udid, "bundle", bundle)
				return
			} else if startErr == nil {
				if errText == "" {
					errText = "runner did not become ready"
				}
				startErr = errors.New(errText)
			}
			h.noteErr(udid, gen, startErr)
		} else if release != nil {
			release()
		}
		edge = runwdaEdgeTick
		if !h.waitDue(udid, gen, plan.NextDue) {
			return
		}
	}
}

func (h *runwdaHost) identity(udid string) (string, []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	d := h.byUDID[udid]
	if d == nil {
		return "", nil
	}
	return d.id.Bundle, append([]string(nil), d.id.Args...)
}

func (h *runwdaHost) plan(udid string, gen int, edge runwdaEdge) (*runwdaPlan, func()) {
	_, _, ready, _ := h.live(udid)
	var release func()
	busy := false
	if h.adapterGoios() != nil {
		unlock, ok := h.adapterGoios().TryDeviceInitializer(udid)
		if ok {
			release = unlock
		} else {
			busy = true
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	d := h.byUDID[udid]
	if d == nil || d.gen != gen || !d.deviceUp {
		return nil, release
	}
	reserved := false
	if h.reserved != nil && d.id.Bundle != "" {
		reserved = h.reserved(udid)
	}
	view := &runwdaView{
		Now:             h.clock(),
		HasIdentity:     d.id.Bundle != "",
		Edge:            edge,
		ReservedByOther: reserved,
		RunnerReady:     ready,
		InitializerBusy: busy,
		Attempts:        d.attempts,
		LastError:       d.err,
		NextDue:         d.nextDue,
		DeviceUp:        d.deviceUp,
	}
	plan := decideRunwda(view)
	d.phase = plan.Phase
	d.attempts = plan.Attempts
	d.nextDue = plan.NextDue
	if plan.Phase == AutomationReady {
		d.err = ""
	} else if plan.Error != "" {
		d.err = plan.Error
	}
	return plan, release
}

func (h *runwdaHost) adapterGoios() *goios.Resolver {
	// The host is only reachable from its adapter. The lock lives on
	// the adapter's resolver so resolve and this loop share it.
	// Bind stores the adapter indirectly: callers set it via the
	// closure below. A nil adapter means the test supplied no resolver.
	if h == nil || h.lockResolver == nil {
		return nil
	}
	return h.lockResolver()
}

func (h *runwdaHost) noteErr(udid string, gen int, err error) {
	if err == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	d := h.byUDID[udid]
	if d == nil || d.gen != gen {
		return
	}
	d.err = err.Error()
}

func (h *runwdaHost) publish(udid string, gen int, phase, errText string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	d := h.byUDID[udid]
	if d == nil || d.gen != gen {
		return
	}
	d.phase = phase
	d.err = errText
}

func (h *runwdaHost) waitDue(udid string, gen int, due time.Time) bool {
	if _, ok := h.current(udid, gen); !ok {
		return false
	}
	remain := due.Sub(h.clock())
	h.sleep(remain)
	_, ok := h.current(udid, gen)
	return ok
}

func (h *runwdaHost) report(udid string) (string, string) {
	bundle, args, ready, errText := h.live(udid)
	h.mu.Lock()
	defer h.mu.Unlock()
	d := h.byUDID[udid]
	if d == nil {
		if bundle != "" && ready {
			h.byUDID[udid] = &runwdaDev{
				id: runwdaIdentity{
					UDID:   udid,
					Bundle: bundle,
					Args:   append([]string(nil), args...),
				},
				phase:    AutomationReady,
				deviceUp: true,
			}
			return AutomationReady, ""
		}
		return AutomationAbsent, ""
	}
	if bundle != "" && d.id.Bundle == "" {
		d.id.UDID = udid
		d.id.Bundle = bundle
	}
	if len(args) > 0 && len(d.id.Args) == 0 {
		d.id.Args = append([]string(nil), args...)
	}
	// Leave "starting" to the reappear loop. Any other phase follows a
	// runner that is already up, including one that appeared after the
	// daemon's startup attach.
	if ready && d.deviceUp && d.phase != AutomationStarting {
		d.phase = AutomationReady
		d.err = ""
		return AutomationReady, ""
	}
	if d.id.Bundle == "" {
		return AutomationAbsent, ""
	}
	if d.phase == AutomationReady && !ready {
		if errText == "" {
			errText = "runner exited"
		}
		d.phase = AutomationFailed
		d.err = errText
		return AutomationFailed, errText
	}
	if d.phase == AutomationFailed {
		if d.err == "" {
			d.err = errText
		}
		return AutomationFailed, d.err
	}
	if d.phase == "" || !d.deviceUp {
		return AutomationAbsent, ""
	}
	return d.phase, d.err
}

// lockResolver is set by BindRunwda so the loop can see a resolver that
// the test swaps in after NewIOSAdapter.
func (a *IOSAdapter) bindRunwdaResolver(h *runwdaHost) {
	h.lockResolver = func() *goios.Resolver {
		if a == nil {
			return nil
		}
		return a.goios
	}
}

// procRunner is the production ios runwda process table.
type procRunner struct {
	mu     sync.Mutex
	iosBin string
	owned  map[string]*ownedRunwda
	seen   map[string]runwdaIdentity
}

type ownedRunwda struct {
	cmd     *exec.Cmd
	out     *tailBuf
	bundle  string
	args    []string
	started time.Time
	exited  bool
	exitErr error
	pid     int
}

type tailBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *tailBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, _ = b.buf.Write(p)
	const max = 64 << 10
	if b.buf.Len() > max {
		rest := append([]byte(nil), b.buf.Bytes()[b.buf.Len()-max:]...)
		b.buf.Reset()
		_, _ = b.buf.Write(rest)
	}
	return len(p), nil
}

func (b *tailBuf) string() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newProcRunner(iosBin string) *procRunner {
	return &procRunner{
		iosBin: iosBin,
		owned:  map[string]*ownedRunwda{},
		seen:   map[string]runwdaIdentity{},
	}
}

func (p *procRunner) Live(udid string) (string, []string, bool, string) {
	if p == nil || udid == "" {
		return "", nil, false, ""
	}
	if bundle, args, ready, errText, ok := p.ownedLive(udid); ok {
		return bundle, args, ready, errText
	}
	id, ok := scanRunwda(udid)
	if !ok {
		return "", nil, false, ""
	}
	p.mu.Lock()
	p.seen[udid] = id
	p.mu.Unlock()
	return id.Bundle, append([]string(nil), id.Args...), true, ""
}

func (p *procRunner) ownedLive(udid string) (bundle string, args []string, ready bool, errText string, ok bool) {
	p.mu.Lock()
	o := p.owned[udid]
	if o == nil {
		p.mu.Unlock()
		return "", nil, false, "", false
	}
	exited := o.exited
	exitErr := o.exitErr
	bundle = o.bundle
	args = append([]string(nil), o.args...)
	started := o.started
	out := o.out
	p.mu.Unlock()
	text := ""
	if out != nil {
		text = out.string()
	}
	if marker := runwdaFailure(text); marker != "" {
		return bundle, args, false, marker, true
	}
	if exited {
		errText = lastLine(text)
		if errText == "" && exitErr != nil {
			errText = exitErr.Error()
		}
		if errText == "" {
			errText = "runner exited"
		}
		return bundle, args, false, errText, true
	}
	if !started.IsZero() && time.Since(started) >= runwdaReadySettle {
		return bundle, args, true, "", true
	}
	return bundle, args, false, "", true
}

func (p *procRunner) Start(udid, bundle string, args []string) (int, error) {
	if p == nil {
		return 0, errors.New("runwda runner is not configured")
	}
	// The previous attempt's stderr lives only in its tail. Record the
	// error and whether the child has exited before this slot is replaced.
	p.logAttemptBeforeReplace(udid)
	p.stopOwned(udid)
	if len(args) == 0 {
		args = defaultRunwdaArgs(udid, bundle)
	}
	bin := p.iosBin
	p.mu.Lock()
	if seen, ok := p.seen[udid]; ok && seen.Binary != "" {
		bin = seen.Binary
	}
	p.mu.Unlock()
	if bin == "" {
		return 0, errors.New("ios binary not found")
	}
	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	buf := &tailBuf{}
	cmd.Stdout = buf
	cmd.Stderr = buf
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	owned := &ownedRunwda{
		cmd:     cmd,
		out:     buf,
		bundle:  bundle,
		args:    append([]string(nil), args...),
		started: time.Now(),
		pid:     cmd.Process.Pid,
	}
	p.mu.Lock()
	p.owned[udid] = owned
	p.mu.Unlock()
	slog.Info("runwda reinstantiate", "udid", udid, "bundle", bundle, "pid", owned.pid)
	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		if p.owned[udid] == owned {
			owned.exited = true
			owned.exitErr = err
		}
		p.mu.Unlock()
	}()
	deadline := time.Now().Add(runwdaAttemptWait)
	for {
		_, _, ready, errText, _ := p.ownedLive(udid)
		if ready {
			return owned.pid, nil
		}
		if errText != "" {
			return owned.pid, errors.New(errText)
		}
		if time.Now().After(deadline) {
			return owned.pid, errors.New("runner did not become ready")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (p *procRunner) logAttemptBeforeReplace(udid string) {
	if p == nil || udid == "" {
		return
	}
	p.mu.Lock()
	o := p.owned[udid]
	if o == nil {
		p.mu.Unlock()
		return
	}
	pid := o.pid
	exited := o.exited
	exitText := ""
	if o.exitErr != nil {
		exitText = o.exitErr.Error()
	}
	p.mu.Unlock()
	_, _, _, errText, _ := p.ownedLive(udid)
	slog.Warn("runwda attempt ended",
		"udid", udid,
		"pid", pid,
		"exited", exited,
		"exit", exitText,
		"errText", errText,
	)
}

func (p *procRunner) stopOwned(udid string) {
	p.mu.Lock()
	o := p.owned[udid]
	var pid int
	if o != nil && !o.exited && o.cmd != nil && o.cmd.Process != nil {
		pid = o.cmd.Process.Pid
	}
	p.mu.Unlock()
	if pid == 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
}

func defaultRunwdaArgs(udid, bundle string) []string {
	return []string{
		"runwda",
		"--udid=" + udid,
		"--bundleid=" + bundle,
		"--testrunnerbundleid=" + bundle,
		"--xctestconfig=devicekit-iosUITests.xctest",
	}
}

func scanRunwda(udid string) (runwdaIdentity, bool) {
	out, err := exec.Command("ps", "-axww", "-o", "pid=,command=").Output()
	if err != nil {
		return runwdaIdentity{}, false
	}
	for _, line := range strings.Split(string(out), "\n") {
		id, ok := parseRunwdaPS(line)
		if ok && id.UDID == udid {
			return id, true
		}
	}
	return runwdaIdentity{}, false
}

func parseRunwdaPS(line string) (runwdaIdentity, bool) {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return runwdaIdentity{}, false
	}
	if _, err := strconv.Atoi(fields[0]); err != nil {
		return runwdaIdentity{}, false
	}
	idx := -1
	for i, field := range fields {
		if field == "runwda" {
			idx = i
			break
		}
	}
	if idx < 1 {
		return runwdaIdentity{}, false
	}
	args := append([]string(nil), fields[idx:]...)
	udid := flagValue(args, "--udid")
	bundle := flagValue(args, "--bundleid")
	if udid == "" || bundle == "" {
		return runwdaIdentity{}, false
	}
	return runwdaIdentity{
		UDID:   udid,
		Bundle: bundle,
		Args:   args,
		Binary: fields[idx-1],
	}, true
}

func flagValue(args []string, name string) string {
	prefix := name + "="
	for i, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			return strings.TrimPrefix(arg, prefix)
		}
		if arg == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func runwdaFailure(text string) string {
	if text == "" {
		return ""
	}
	lower := strings.ToLower(text)
	markers := []string{
		"stream_closed",
		"enabling automation",
		"enable ui automation",
		"failed running wda",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return clipLine(text, marker)
		}
	}
	return ""
}

func clipLine(text, marker string) string {
	lower := strings.ToLower(text)
	at := strings.Index(lower, strings.ToLower(marker))
	if at < 0 {
		return marker
	}
	start := strings.LastIndex(text[:at], "\n") + 1
	end := strings.Index(text[at:], "\n")
	line := text[start:]
	if end >= 0 {
		line = text[start : at+end]
	}
	line = strings.TrimSpace(line)
	if len(line) > 400 {
		line = line[:400]
	}
	if line == "" {
		return marker
	}
	return line
}

func lastLine(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if i := strings.LastIndex(text, "\n"); i >= 0 {
		text = strings.TrimSpace(text[i+1:])
	}
	if len(text) > 400 {
		text = text[len(text)-400:]
	}
	return text
}

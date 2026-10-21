// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/goios"
)

// TestRunwdaReappear drives the shipped usbmux disappear/reappear edge
// with a fake tunnel and a fake runner. The recovery decision is the
// production one.
func TestRunwdaReappear(t *testing.T) {
	const bundle = "com.mobilenext.devicekit-iosUITests.xctrunner"

	t.Run("one restart then leave a ready runner alone", func(t *testing.T) {
		fx := newRunwdaFixture(t, nil)
		const udid = "phone-ready"
		args := runwdaArgs(udid, bundle)
		var phaseAtStart atomic.Value
		fx.fake.onStart = func(id string) {
			phase, _, _ := readAutomation(fx.a, id)
			phaseAtStart.Store(phase)
		}
		fx.fake.set(udid, bundle, args, true)
		fx.play(goios.DeviceEvent{DeviceID: 1, UDID: udid, Attached: true})
		if phase, _ := automationOf(t, fx.a, udid); phase != AutomationReady || fx.fake.startCount() != 0 {
			t.Fatalf("live session before disappear: phase %s starts %d", phase, fx.fake.startCount())
		}
		fx.play(goios.DeviceEvent{DeviceID: 1, Attached: false})
		fx.fake.set(udid, bundle, args, false)
		fx.play(goios.DeviceEvent{DeviceID: 2, UDID: udid, Attached: true})
		waitRunwda(t, fx, udid, func() bool {
			return fx.fake.startCount() == 1 && phaseIs(t, fx.a, udid, AutomationReady)
		})
		if phase, _ := phaseAtStart.Load().(string); phase != AutomationStarting {
			t.Fatalf("phase at start %q", phase)
		}
		if got := fx.fake.startedBundles(); len(got) != 1 || got[0] != bundle {
			t.Fatalf("restart bundle %v", got)
		}
		fx.play(goios.DeviceEvent{DeviceID: 2, Attached: false})
		fx.play(goios.DeviceEvent{DeviceID: 3, UDID: udid, Attached: true})
		if fx.fake.startCount() != 1 {
			t.Fatalf("restarted a runner that was already up: starts %d", fx.fake.startCount())
		}
		if phase, errText := automationOf(t, fx.a, udid); phase != AutomationReady || errText != "" {
			t.Fatalf("after second reappear phase %s err %q", phase, errText)
		}
		assertNoControlEffects(t, fx.a)
	})

	t.Run("bounded backoff then stop", func(t *testing.T) {
		fx := newRunwdaFixture(t, nil)
		fx.fake.neverReady = true
		const udid = "phone-never"
		args := runwdaArgs(udid, bundle)
		fx.fake.set(udid, bundle, args, true)
		fx.play(goios.DeviceEvent{DeviceID: 1, UDID: udid, Attached: true})
		fx.play(goios.DeviceEvent{DeviceID: 1, Attached: false})
		fx.fake.set(udid, bundle, args, false)
		fx.play(goios.DeviceEvent{DeviceID: 2, UDID: udid, Attached: true})
		waitRunwda(t, fx, udid, func() bool {
			return fx.fake.startCount() == 1 && fx.clock.Waiting() == 1
		})
		fx.clock.Advance(runwdaBackoff - time.Millisecond)
		if fx.fake.startCount() != 1 {
			t.Fatalf("attempt started before backoff: %d", fx.fake.startCount())
		}
		fx.clock.Advance(time.Millisecond)
		waitRunwda(t, fx, udid, func() bool { return fx.fake.startCount() == 2 && fx.clock.Waiting() == 1 })
		fx.clock.Advance(runwdaBackoff)
		waitRunwda(t, fx, udid, func() bool { return fx.fake.startCount() == 3 && fx.clock.Waiting() == 1 })
		fx.clock.Advance(runwdaBackoff)
		waitRunwda(t, fx, udid, func() bool {
			phase, errText := automationOf(t, fx.a, udid)
			return fx.fake.startCount() == runwdaMaxAttempts && phase == AutomationFailed && strings.Contains(errText, "runner did not become ready")
		})
		fx.clock.Advance(runwdaBackoff)
		time.Sleep(20 * time.Millisecond)
		if fx.fake.startCount() != runwdaMaxAttempts {
			t.Fatalf("attempts continued after the bound: %d", fx.fake.startCount())
		}
		times := fx.fake.startTimes()
		if len(times) != runwdaMaxAttempts {
			t.Fatalf("start times %d", len(times))
		}
		for i := 1; i < len(times); i++ {
			if times[i].Sub(times[i-1]) != runwdaBackoff {
				t.Fatalf("attempt gap %s, want %s", times[i].Sub(times[i-1]), runwdaBackoff)
			}
		}
		assertNoControlEffects(t, fx.a)
	})

	t.Run("does not overlap an in-flight initializer", func(t *testing.T) {
		fx := newRunwdaFixture(t, nil)
		const udid = "phone-lock"
		args := runwdaArgs(udid, bundle)
		var hold atomic.Int32
		held := make(chan struct{})
		release := make(chan struct{})
		freed := make(chan struct{})
		go func() {
			_ = fx.a.goios.WithDeviceInitializer(udid, func() error {
				hold.Add(1)
				close(held)
				<-release
				hold.Add(-1)
				return nil
			})
			close(freed)
		}()
		select {
		case <-held:
		case <-time.After(2 * time.Second):
			t.Fatal("initializer did not acquire the lock")
		}
		fx.fake.onStart = func(string) {
			if hold.Load() != 0 {
				fx.fake.overlapped.Store(true)
			}
		}
		fx.fake.set(udid, bundle, args, true)
		fx.play(goios.DeviceEvent{DeviceID: 1, UDID: udid, Attached: true})
		fx.play(goios.DeviceEvent{DeviceID: 1, Attached: false})
		fx.fake.set(udid, bundle, args, false)
		fx.play(goios.DeviceEvent{DeviceID: 2, UDID: udid, Attached: true})
		waitRunwda(t, fx, udid, func() bool { return fx.clock.Waiting() >= 1 })
		if fx.fake.startCount() != 0 {
			t.Fatalf("start overlapped the initializer: starts %d", fx.fake.startCount())
		}
		if phase, _ := automationOf(t, fx.a, udid); phase != AutomationStarting {
			t.Fatalf("phase while initializer is in flight: %s", phase)
		}
		close(release)
		select {
		case <-freed:
		case <-time.After(2 * time.Second):
			t.Fatal("initializer did not release the lock")
		}
		fx.clock.Advance(runwdaBackoff)
		waitRunwda(t, fx, udid, func() bool {
			return fx.fake.startCount() == 1 && phaseIs(t, fx.a, udid, AutomationReady)
		})
		if fx.fake.overlapped.Load() {
			t.Fatal("reinstantiation overlapped the RSD/XPC initializer")
		}
		assertNoControlEffects(t, fx.a)
	})

	t.Run("reserved by someone else", func(t *testing.T) {
		fx := newRunwdaFixture(t, func(string) bool { return true })
		const udid = "phone-held"
		args := runwdaArgs(udid, bundle)
		fx.fake.set(udid, bundle, args, true)
		fx.play(goios.DeviceEvent{DeviceID: 1, UDID: udid, Attached: true})
		fx.play(goios.DeviceEvent{DeviceID: 1, Attached: false})
		fx.fake.set(udid, bundle, args, false)
		fx.play(goios.DeviceEvent{DeviceID: 2, UDID: udid, Attached: true})
		waitRunwda(t, fx, udid, func() bool {
			phase, errText := automationOf(t, fx.a, udid)
			return phase == AutomationFailed && strings.Contains(errText, "reserved")
		})
		if fx.fake.startCount() != 0 {
			t.Fatalf("reserved device started %d runners", fx.fake.startCount())
		}
		assertNoControlEffects(t, fx.a)
	})

	t.Run("no prior session", func(t *testing.T) {
		fx := newRunwdaFixture(t, nil)
		const udid = "phone-new"
		fx.play(goios.DeviceEvent{DeviceID: 1, UDID: udid, Attached: true})
		fx.play(goios.DeviceEvent{DeviceID: 1, Attached: false})
		fx.play(goios.DeviceEvent{DeviceID: 2, UDID: udid, Attached: true})
		if fx.fake.startCount() != 0 {
			t.Fatalf("started a device with no prior session: %d", fx.fake.startCount())
		}
		if phase, errText := automationOf(t, fx.a, udid); phase != AutomationAbsent || errText != "" {
			t.Fatalf("phase %s err %q", phase, errText)
		}
		assertNoControlEffects(t, fx.a)
	})
}

func TestParseRunwdaPS(t *testing.T) {
	line := "  4242 /Users/marcelo/go/libexec/spyder/ios runwda --udid=00008110-0014182E0AC2801E --bundleid=com.mobilenext.devicekit-iosUITests.xctrunner --testrunnerbundleid=com.mobilenext.devicekit-iosUITests.xctrunner --xctestconfig=devicekit-iosUITests.xctest"
	id, ok := parseRunwdaPS(line)
	if !ok {
		t.Fatal("parse failed")
	}
	if id.UDID != "00008110-0014182E0AC2801E" || id.Bundle != "com.mobilenext.devicekit-iosUITests.xctrunner" {
		t.Fatalf("identity %+v", id)
	}
	if id.Binary != "/Users/marcelo/go/libexec/spyder/ios" || flagValue(id.Args, "--xctestconfig") != "devicekit-iosUITests.xctest" {
		t.Fatalf("argv %+v", id)
	}
}

type runwdaFixture struct {
	a     *IOSAdapter
	fake  *fakeRunwda
	clock *fakeClock
	l     *goios.Listener
}

func newRunwdaFixture(t *testing.T, reserved func(string) bool) *runwdaFixture {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"address":"::1","rsdPort":1,"udid":"phone"}`)
	}))
	t.Cleanup(srv.Close)
	a := tunnelAdapter(t, srv)
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	fake := &fakeRunwda{clock: clock, live: map[string]fakeLive{}}
	a.BindRunwda(&RunwdaBind{
		Runner:   fake,
		Reserved: reserved,
		Now:      clock.Now,
		Wait:     clock.Wait,
	})
	return &runwdaFixture{
		a:     a,
		fake:  fake,
		clock: clock,
		l:     goios.NewListener(iosTunnelRecovery{a}),
	}
}

func (f *runwdaFixture) play(ev goios.DeviceEvent) {
	f.l.RunSync(context.Background(), &goios.ScriptedSource{Events: []goios.DeviceEvent{ev}})
}

func runwdaArgs(udid, bundle string) []string {
	return []string{
		"runwda",
		"--udid=" + udid,
		"--bundleid=" + bundle,
		"--testrunnerbundleid=" + bundle,
		"--xctestconfig=devicekit-iosUITests.xctest",
	}
}

func readAutomation(a *IOSAdapter, id string) (string, string, error) {
	a.mu.Lock()
	if a.cache == nil {
		a.cache = map[string]cachedState{}
	}
	a.cache[id] = cachedState{state: State{}, at: time.Now()}
	a.mu.Unlock()
	st, err := a.State(id)
	if err != nil {
		return "", "", err
	}
	return st.AutomationSession, st.AutomationError, nil
}

func automationOf(t *testing.T, a *IOSAdapter, id string) (string, string) {
	t.Helper()
	phase, errText, err := readAutomation(a, id)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	return phase, errText
}

func phaseIs(t *testing.T, a *IOSAdapter, id, want string) bool {
	t.Helper()
	phase, _ := automationOf(t, a, id)
	return phase == want
}

func waitRunwda(t *testing.T, fx *runwdaFixture, udid string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	phase, errText := automationOf(t, fx.a, udid)
	t.Fatalf("timed out: phase %s err %q starts %d waiting %d", phase, errText, fx.fake.startCount(), fx.clock.Waiting())
}

func assertNoControlEffects(t *testing.T, a *IOSAdapter) {
	t.Helper()
	in, launch, term := a.RunwdaEffectCounts()
	if in != 0 || launch != 0 || term != 0 {
		t.Fatalf("input %d launch %d terminate %d", in, launch, term)
	}
}

type fakeLive struct {
	bundle string
	args   []string
	ready  bool
}

type fakeRunwda struct {
	mu         sync.Mutex
	clock      *fakeClock
	live       map[string]fakeLive
	starts     []time.Time
	bundles    []string
	onStart    func(string)
	neverReady bool
	overlapped atomic.Bool
}

func (f *fakeRunwda) set(udid, bundle string, args []string, ready bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.live[udid] = fakeLive{bundle: bundle, args: append([]string(nil), args...), ready: ready}
}

func (f *fakeRunwda) Live(udid string) (string, []string, bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	live, ok := f.live[udid]
	if !ok || live.bundle == "" {
		return "", nil, false, ""
	}
	errText := ""
	if !live.ready {
		errText = "runner did not become ready"
	}
	return live.bundle, append([]string(nil), live.args...), live.ready, errText
}

func (f *fakeRunwda) Start(udid, bundle string, args []string) (int, error) {
	f.mu.Lock()
	f.starts = append(f.starts, f.clock.Now())
	f.bundles = append(f.bundles, bundle)
	onStart := f.onStart
	n := len(f.starts)
	never := f.neverReady
	f.mu.Unlock()
	if onStart != nil {
		onStart(udid)
	}
	f.mu.Lock()
	if !never {
		f.live[udid] = fakeLive{bundle: bundle, args: append([]string(nil), args...), ready: true}
	}
	f.mu.Unlock()
	if never {
		return n, errRunnerNotReady
	}
	return n, nil
}

func (f *fakeRunwda) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.starts)
}

func (f *fakeRunwda) startTimes() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.starts...)
}

func (f *fakeRunwda) startedBundles() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.bundles...)
}

var errRunnerNotReady = errString("runner did not become ready")

type errString string

func (e errString) Error() string { return string(e) }

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWait
}

type fakeWait struct {
	deadline time.Time
	ch       chan struct{}
}

func newFakeClock(now time.Time) *fakeClock {
	return &fakeClock{now: now}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Wait(d time.Duration) {
	if d <= 0 {
		return
	}
	c.mu.Lock()
	ch := make(chan struct{})
	c.waiters = append(c.waiters, fakeWait{deadline: c.now.Add(d), ch: ch})
	c.mu.Unlock()
	<-ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	keep := make([]fakeWait, 0, len(c.waiters))
	for _, w := range c.waiters {
		if !c.now.Before(w.deadline) {
			close(w.ch)
			continue
		}
		keep = append(keep, w)
	}
	c.waiters = keep
}

func (c *fakeClock) Waiting() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package pool

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// --------------------------------------------------------------------------
// Fake executor
// --------------------------------------------------------------------------

type fakeExec struct {
	mu      sync.Mutex
	sims    map[string]string // udid -> state ("Shutdown"|"Booted")
	avds    map[string]string // name -> path
	booted  map[string]string // avd name -> serial
	counter int
	errs    map[string]error // method -> error to inject
}

func newFakeExec() *fakeExec {
	return &fakeExec{
		sims:   map[string]string{},
		avds:   map[string]string{},
		booted: map[string]string{},
		errs:   map[string]error{},
	}
}

func (f *fakeExec) SimCreate(name, _, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs["SimCreate"]; err != nil {
		return "", err
	}
	f.counter++
	udid := fmt.Sprintf("SIM-%04d", f.counter)
	f.sims[udid] = "Shutdown"
	return udid, nil
}

func (f *fakeExec) SimBoot(udid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs["SimBoot"]; err != nil {
		return err
	}
	if _, ok := f.sims[udid]; !ok {
		return fmt.Errorf("sim %s not found", udid)
	}
	f.sims[udid] = "Booted"
	return nil
}

func (f *fakeExec) SimShutdown(udid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sims[udid] = "Shutdown"
	return nil
}

func (f *fakeExec) SimDelete(udid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs["SimDelete"]; err != nil {
		return err
	}
	delete(f.sims, udid)
	return nil
}

func (f *fakeExec) SimList() ([]SimInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []SimInfo
	for udid, state := range f.sims {
		out = append(out, SimInfo{UDID: udid, Name: "sim-" + udid, State: state})
	}
	return out, nil
}

func (f *fakeExec) AVDClone(templateName, newName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs["AVDClone"]; err != nil {
		return err
	}
	f.avds[newName] = "/home/.android/avd/" + newName + ".avd"
	return nil
}

func (f *fakeExec) AVDBoot(name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs["AVDBoot"]; err != nil {
		return "", err
	}
	f.counter++
	serial := fmt.Sprintf("emulator-%d", 5554+(f.counter-1)*2)
	f.booted[name] = serial
	return serial, nil
}

func (f *fakeExec) AVDShutdown(serial string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for name, s := range f.booted {
		if s == serial {
			delete(f.booted, name)
			return nil
		}
	}
	return nil
}

func (f *fakeExec) AVDDelete(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs["AVDDelete"]; err != nil {
		return err
	}
	delete(f.avds, name)
	delete(f.booted, name)
	return nil
}

func (f *fakeExec) AVDList() ([]AVDInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []AVDInfo
	for name, path := range f.avds {
		out = append(out, AVDInfo{Name: name, Path: path})
	}
	return out, nil
}

// --------------------------------------------------------------------------
// Fake clock
// --------------------------------------------------------------------------

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	fc      *fakeClock
	fireAt  time.Time
	f       func()
	stopped bool
}

func (t *fakeTimer) Stop() bool {
	t.fc.mu.Lock()
	defer t.fc.mu.Unlock()
	if t.stopped {
		return false
	}
	t.stopped = true
	return true
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (fc *fakeClock) Now() time.Time {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.now
}

func (fc *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	t := &fakeTimer{
		fc:     fc,
		fireAt: fc.now.Add(d),
		f:      f,
	}
	fc.timers = append(fc.timers, t)
	return t
}

// Advance moves the clock forward and fires all expired timers.
func (fc *fakeClock) Advance(d time.Duration) {
	fc.mu.Lock()
	fc.now = fc.now.Add(d)
	var toFire []*fakeTimer
	for _, t := range fc.timers {
		if !t.stopped && !fc.now.Before(t.fireAt) {
			t.stopped = true
			toFire = append(toFire, t)
		}
	}
	fc.mu.Unlock()
	for _, t := range toFire {
		t.f()
	}
}

// --------------------------------------------------------------------------
// Config helpers
// --------------------------------------------------------------------------

func iOSTemplate(name string) TemplateConfig {
	return TemplateConfig{
		Name:                 name,
		Platform:             "ios",
		DeviceType:           "com.apple.CoreSimulator.SimDeviceType.iPhone-16",
		RuntimeOrSystemImage: "com.apple.CoreSimulator.SimRuntime.iOS-18-3",
		AvailableMin:         1,
		AvailableMax:         3,
		RunningWarm:          1,
		LingerSeconds:        60,
	}
}

func androidTemplate(name string) TemplateConfig {
	return TemplateConfig{
		Name:                 name,
		Platform:             "android",
		DeviceType:           "pixel_9_pro",
		RuntimeOrSystemImage: "system-images;android-35;google_apis;arm64-v8a",
		AvailableMin:         1,
		AvailableMax:         2,
		RunningWarm:          0,
		LingerSeconds:        30,
	}
}

// --------------------------------------------------------------------------
// Tests
// --------------------------------------------------------------------------

func TestConfigYAMLRoundTrip(t *testing.T) {
	// Exercise LoadConfig with a real YAML file.
	yamlContent := `
templates:
  - name: iphone16
    platform: ios
    device_type: com.apple.CoreSimulator.SimDeviceType.iPhone-16
    runtime_or_system_image: com.apple.CoreSimulator.SimRuntime.iOS-18-3
    available_min: 1
    available_max: 3
    running_warm: 1
    linger_seconds: 60
  - name: pixel9
    platform: android
    device_type: pixel_9_pro
    runtime_or_system_image: "system-images;android-35;google_apis;arm64-v8a"
    available_min: 0
    available_max: 2
    running_warm: 0
`
	tmp := t.TempDir()
	path := tmp + "/pool.yaml"
	if err := writeFile(path, yamlContent); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Templates) != 2 {
		t.Fatalf("expected 2 templates, got %d", len(cfg.Templates))
	}
	if cfg.Templates[0].Name != "iphone16" {
		t.Errorf("template[0].Name = %q, want iphone16", cfg.Templates[0].Name)
	}
	if cfg.Templates[1].Platform != "android" {
		t.Errorf("template[1].Platform = %q, want android", cfg.Templates[1].Platform)
	}
	if cfg.Templates[0].LingerSeconds != 60 {
		t.Errorf("template[0].LingerSeconds = %d, want 60", cfg.Templates[0].LingerSeconds)
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "duplicate name",
			yaml: `
templates:
  - name: foo
    platform: ios
    device_type: dt
    runtime_or_system_image: rt
    available_max: 1
  - name: foo
    platform: ios
    device_type: dt
    runtime_or_system_image: rt
    available_max: 1
`,
			wantErr: "duplicate name",
		},
		{
			name: "bad platform",
			yaml: `
templates:
  - name: foo
    platform: windows
    device_type: dt
    runtime_or_system_image: rt
    available_max: 1
`,
			wantErr: "platform must be ios or android",
		},
		{
			name: "max < min",
			yaml: `
templates:
  - name: foo
    platform: ios
    device_type: dt
    runtime_or_system_image: rt
    available_min: 3
    available_max: 1
`,
			wantErr: "available_max",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			path := tmp + "/pool.yaml"
			if err := writeFile(path, tc.yaml); err != nil {
				t.Fatal(err)
			}
			_, err := LoadConfig(path)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestReconcileCreatesAvailable(t *testing.T) {
	cfg := &Config{Templates: []TemplateConfig{iOSTemplate("t1")}}
	exec := newFakeExec()
	clk := newFakeClock(time.Now())
	p := newWithClock(cfg, exec, clk)

	p.Reconcile(context.Background())

	// AvailableMin=1, RunningWarm=1 → 1 created (available tier) then
	// 1 pre-booted → 1 running.
	st := p.Status()
	if len(st) != 1 {
		t.Fatalf("want 1 template status, got %d", len(st))
	}
	if st[0].Running != 1 {
		t.Errorf("running = %d, want 1", st[0].Running)
	}
	if st[0].Available != 0 {
		t.Errorf("available = %d, want 0", st[0].Available)
	}
}

func TestAcquireRunningTierFirst(t *testing.T) {
	cfg := &Config{Templates: []TemplateConfig{iOSTemplate("t1")}}
	exec := newFakeExec()
	clk := newFakeClock(time.Now())
	p := newWithClock(cfg, exec, clk)
	p.Reconcile(context.Background())

	inst, err := p.Acquire("t1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if inst.Tier != TierReserved {
		t.Errorf("tier = %q, want reserved", inst.Tier)
	}
	if inst.Template != "t1" {
		t.Errorf("template = %q, want t1", inst.Template)
	}
	// Running tier should now be empty.
	st := p.Status()
	if st[0].Running != 0 {
		t.Errorf("running after acquire = %d, want 0", st[0].Running)
	}
	if st[0].Reserved != 1 {
		t.Errorf("reserved after acquire = %d, want 1", st[0].Reserved)
	}
}

func TestLingerKeepsRunningTierUntilExpiry(t *testing.T) {
	cfg := &Config{Templates: []TemplateConfig{iOSTemplate("t1")}}
	exec := newFakeExec()
	clk := newFakeClock(time.Now())
	p := newWithClock(cfg, exec, clk)
	p.Reconcile(context.Background())

	inst, err := p.Acquire("t1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// Release — linger timer starts (60s).
	if err := p.Release(inst.ID); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// Immediately after release: still running.
	st := p.Status()
	if st[0].Running != 1 {
		t.Errorf("running right after release = %d, want 1", st[0].Running)
	}

	// Advance past linger.
	clk.Advance(61 * time.Second)

	// Now should be available (shutdown but disk still present).
	st = p.Status()
	if st[0].Running != 0 {
		t.Errorf("running after linger = %d, want 0", st[0].Running)
	}
	if st[0].Available != 1 {
		t.Errorf("available after linger = %d, want 1", st[0].Available)
	}
}

func TestLingerReacquireBeforeExpiry(t *testing.T) {
	cfg := &Config{Templates: []TemplateConfig{iOSTemplate("t1")}}
	exec := newFakeExec()
	clk := newFakeClock(time.Now())
	p := newWithClock(cfg, exec, clk)
	p.Reconcile(context.Background())

	inst, _ := p.Acquire("t1")
	_ = p.Release(inst.ID)

	// Re-acquire before linger expires — should get the same running instance.
	inst2, err := p.Acquire("t1")
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	if inst2.ID != inst.ID {
		t.Errorf("got different instance on re-acquire: %q vs %q", inst2.ID, inst.ID)
	}

	// Advance past original linger period — timer should have been cancelled.
	clk.Advance(61 * time.Second)

	// Still reserved (not transitioned to available).
	st := p.Status()
	if st[0].Reserved != 1 {
		t.Errorf("reserved = %d, want 1", st[0].Reserved)
	}
}

func TestPoolCapEnforcementDeletesOnRelease(t *testing.T) {
	tmpl := iOSTemplate("t1")
	tmpl.AvailableMin = 0
	tmpl.AvailableMax = 0 // no available slots — always delete on linger expiry
	tmpl.RunningWarm = 0
	cfg := &Config{Templates: []TemplateConfig{tmpl}}
	exec := newFakeExec()
	clk := newFakeClock(time.Now())
	p := newWithClock(cfg, exec, clk)

	inst, err := p.Acquire("t1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := p.Release(inst.ID); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// Advance past linger — available cap=0 so instance should be deleted.
	clk.Advance(61 * time.Second)

	st := p.Status()
	total := st[0].Available + st[0].Running + st[0].Reserved
	if total != 0 {
		t.Errorf("total instances = %d, want 0 (cap enforced delete)", total)
	}
}

func TestForceShutdownDrainsTemplate(t *testing.T) {
	cfg := &Config{Templates: []TemplateConfig{iOSTemplate("t1")}}
	exec := newFakeExec()
	clk := newFakeClock(time.Now())
	p := newWithClock(cfg, exec, clk)
	p.Reconcile(context.Background())

	if err := p.ForceShutdown("t1"); err != nil {
		t.Fatalf("ForceShutdown: %v", err)
	}

	st := p.Status()
	total := st[0].Available + st[0].Running + st[0].Reserved
	if total != 0 {
		t.Errorf("total after drain = %d, want 0", total)
	}
}

func TestWarmPreBootsInstances(t *testing.T) {
	tmpl := iOSTemplate("t1")
	tmpl.AvailableMin = 3
	tmpl.RunningWarm = 0
	cfg := &Config{Templates: []TemplateConfig{tmpl}}
	exec := newFakeExec()
	clk := newFakeClock(time.Now())
	p := newWithClock(cfg, exec, clk)
	p.Reconcile(context.Background()) // creates 3 available, boots 0

	if err := p.Warm("t1", 2); err != nil {
		t.Fatalf("Warm: %v", err)
	}

	st := p.Status()
	if st[0].Running != 2 {
		t.Errorf("running after Warm(2) = %d, want 2", st[0].Running)
	}
}

func TestForSelectorFiltering(t *testing.T) {
	cfg := &Config{
		Templates: []TemplateConfig{
			{
				Name: "iphone", Platform: "ios",
				DeviceType:           "dt",
				RuntimeOrSystemImage: "rt",
				Tags:                 []string{"ci", "ios"},
				AvailableMax:         1,
			},
			{
				Name: "pixel", Platform: "android",
				DeviceType:           "dt",
				RuntimeOrSystemImage: "rt",
				Tags:                 []string{"ci", "android"},
				AvailableMax:         1,
			},
		},
	}
	p := newWithClock(cfg, newFakeExec(), newFakeClock(time.Now()))

	got := p.ForSelector([]string{"ci"})
	if len(got) != 2 {
		t.Errorf("ForSelector(ci): got %d, want 2", len(got))
	}

	got = p.ForSelector([]string{"ios"})
	if len(got) != 1 || got[0].Name != "iphone" {
		t.Errorf("ForSelector(ios): got %v, want [iphone]", got)
	}

	got = p.ForSelector([]string{"android"})
	if len(got) != 1 || got[0].Name != "pixel" {
		t.Errorf("ForSelector(android): got %v, want [pixel]", got)
	}

	got = p.ForSelector([]string{})
	if len(got) != 2 {
		t.Errorf("ForSelector(empty): got %d, want 2", len(got))
	}
}

func TestAcquireUnknownTemplate(t *testing.T) {
	cfg := &Config{Templates: []TemplateConfig{iOSTemplate("t1")}}
	p := newWithClock(cfg, newFakeExec(), newFakeClock(time.Now()))
	_, err := p.Acquire("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown template")
	}
}

func TestAndroidAcquireReleaseCycle(t *testing.T) {
	tmpl := androidTemplate("pixel")
	tmpl.AvailableMin = 1
	tmpl.RunningWarm = 0
	cfg := &Config{Templates: []TemplateConfig{tmpl}}
	exec := newFakeExec()
	clk := newFakeClock(time.Now())
	p := newWithClock(cfg, exec, clk)
	p.Reconcile(context.Background()) // creates 1 available

	inst, err := p.Acquire("pixel")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if inst.Serial == "" {
		t.Error("Android instance should have a serial after Acquire")
	}
	if err := p.Release(inst.ID); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// Linger keeps it running.
	st := p.Status()
	if st[0].Running != 1 {
		t.Errorf("running after release = %d, want 1", st[0].Running)
	}

	// After linger (30s), transitions to available.
	clk.Advance(31 * time.Second)
	st = p.Status()
	if st[0].Available != 1 {
		t.Errorf("available after linger = %d, want 1", st[0].Available)
	}
}

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

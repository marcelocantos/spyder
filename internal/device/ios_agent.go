// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	goios_ios "github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/house_arrest"
	"github.com/danielpaulus/go-ios/ios/testmanagerd"

	"github.com/marcelocantos/spyder/internal/agent"
)

// agentHoldSettle is how long a new runner must stay alive before
// supervision treats the launch as a resident process.
const agentHoldSettle = 2 * time.Second

// agentLaunchBudget bounds one resident-runner start, including Apple's
// consent prompt failing the testmanagerd handshake.
const agentLaunchBudget = 45 * time.Second

// agentProbeBudget bounds one process or install lookup. A stuck DTX
// read must not pin supervision; cancellation closes that connection.
const agentProbeBudget = 15 * time.Second

// TestBouncingBallBundleID is the fixture app at ios/BouncingBall.
// Live journeys may launch it. Production idle stops the bundle
// launch_app recorded, not this constant.
const TestBouncingBallBundleID = "com.marcelocantos.spyder.BouncingBall"

// lockProbeBudget bounds one springboard getLockState read. The
// connection stays open until the reply or this deadline. Closing
// earlier turns a live read into EOF and looks like an unlock.
const lockProbeBudget = 5 * time.Second

// AutomationHost is the iOS side of agent.Supervisor: tether, lock,
// runner process, and the resident XCTest session. It does not decide
// transitions; the supervisor does.
type AutomationHost struct {
	IOS *IOSAdapter
	// AUT is an explicit override. Production leaves it empty;
	// NoteLaunched records the bundle launch_app actually started.
	AUT string

	mu             sync.Mutex
	holds          map[string]*runnerHold
	consent        map[string]string
	leases         map[string]bool
	clientSessions map[string]int
	launched       map[string]string
	installs       int
	launches       int
	hostDrops      int
	leaseDrops     int
	lockDetail     map[string]string
	probeNote      map[string]string
	installGen     map[string]string

	// lockRead, lockExec, and bootSource feed Probe in hermetic tests.
	// Production leaves them nil. iOS 17+ lock state comes from
	// devicectl; older devices use springboard getLockState. The boot
	// id is the device's RSD BootSessionUUID.
	lockRead   func(id string) (map[string]any, error)
	lockExec   func(ctx context.Context, udid string) ([]byte, error)
	bootSource func(id string) string
}

type runnerHold struct {
	cancel context.CancelFunc
	done   chan struct{}
	pid    int
}

// NewAutomationHost binds an iOS adapter to the XCTest agent bundles.
func NewAutomationHost(ios *IOSAdapter) *AutomationHost {
	h := &AutomationHost{
		IOS:            ios,
		holds:          map[string]*runnerHold{},
		consent:        map[string]string{},
		leases:         map[string]bool{},
		clientSessions: map[string]int{},
		lockDetail:     map[string]string{},
		probeNote:      map[string]string{},
		installGen:     map[string]string{},
	}
	if ios != nil {
		ios.automation = h
	}
	return h
}

// Effects are the side effects the supervisor is allowed to perform.
func (h *AutomationHost) Effects() agent.Effects {
	return agent.Effects{
		Launch:          h.Launch,
		Install:         h.Install,
		StopAUT:         h.StopAUT,
		DropHostSession: h.DropHostSession,
		DropLease:       h.DropLease,
	}
}

// InstallCount is how many times a bundle was actually copied to a device.
func (h *AutomationHost) InstallCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.installs
}

// LaunchCount is how many resident XCTest sessions were started.
func (h *AutomationHost) LaunchCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.launches
}

// HostDrops is how many times idle dropped a client build/test session.
func (h *AutomationHost) HostDrops() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hostDrops
}

// LeaseDrops is how many times idle dropped the build lease flag.
func (h *AutomationHost) LeaseDrops() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.leaseDrops
}

// LockDetail is the last lock probe for a device (raw state or error).
func (h *AutomationHost) LockDetail(id string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lockDetail[id]
}

// ProbeNote is the last runner-process lookup for a device.
func (h *AutomationHost) ProbeNote(id string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.probeNote[id]
}

// ClientSessions is the number of host xcodebuild/testmanagerd sessions
// held for a client build. The resident agent channel is not one of them.
func (h *AutomationHost) ClientSessions(id string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clientSessions[id]
}

// ChannelOpen reports whether this process is holding the resident
// XCTest session that keeps the runner alive.
func (h *AutomationHost) ChannelOpen(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.holds[id]
	return ok
}

// NoteClientSession records a host build/test session the supervisor
// must drop on idle. The resident runner channel is not recorded here.
func (h *AutomationHost) NoteClientSession(id string) {
	h.mu.Lock()
	h.clientSessions[id]++
	h.mu.Unlock()
}

// NoteLaunched records the bundle a successful launch_app started.
// Idle terminates that bundle. The fixture app is not a default.
func (h *AutomationHost) NoteLaunched(id, bundle string) {
	if h == nil || id == "" || bundle == "" {
		return
	}
	if bundle == alertRunnerBundle || bundle == alertHostBundle {
		return
	}
	h.mu.Lock()
	if h.launched == nil {
		h.launched = map[string]string{}
	}
	h.launched[id] = bundle
	h.mu.Unlock()
}

// LaunchedBundle is the bundle NoteLaunched recorded for id.
func (h *AutomationHost) LaunchedBundle(id string) string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.launched[id]
}

func (h *AutomationHost) autBundle(id string) string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	noted := h.launched[id]
	h.mu.Unlock()
	if noted != "" {
		return noted
	}
	return h.AUT
}

// NoteLease records that a build/Unity lease is held. Spyder has no
// Unity lease object; callers set this only when they actually hold one.
func (h *AutomationHost) NoteLease(id string, held bool) {
	h.mu.Lock()
	h.leases[id] = held
	h.mu.Unlock()
}

// Probe reads tether, lock, runner, and the app under test.
func (h *AutomationHost) Probe(id string) (agent.Observation, error) {
	if h.IOS == nil {
		return agent.Observation{}, errors.New("ios adapter is nil")
	}
	tethered := h.tethered(id)
	locked, lockKnown, detail := h.locked(id)
	h.mu.Lock()
	h.lockDetail[id] = detail
	consent := h.consent[id]
	hostSession := h.clientSessions[id] > 0
	lease := h.leases[id]
	h.mu.Unlock()

	installed, gen, instErr := h.installed(id)
	pid, alive, detail, runErr := h.observeRunner(id)
	h.mu.Lock()
	h.probeNote[id] = detail
	h.mu.Unlock()
	if strings.Contains(detail, "reusing open hold") {
		slog.Warn("runner process probe failed", "device", id, "detail", detail)
	}
	if runErr != nil {
		return agent.Observation{}, runErr
	}
	if instErr != nil {
		return agent.Observation{}, instErr
	}
	aut := h.autBundle(id)
	autRunning := false
	if aut != "" {
		if p, err := h.appPID(id, aut); err == nil && p > 0 {
			autRunning = true
		}
	}
	return agent.Observation{
		Tethered:          tethered,
		Locked:            locked,
		LockKnown:         lockKnown,
		BootID:            h.bootID(id),
		RunnerPID:         pid,
		RunnerAlive:       alive,
		Authorized:        alive && consent == "",
		ConsentRequired:   consent != "",
		ConsentActionable: consent != "",
		ConsentMessage:    consent,
		Installed:         installed,
		InstallGen:        gen,
		AUTBundle:         aut,
		AUTRunning:        autRunning,
		HostSession:       hostSession,
		LeaseHeld:         lease,
	}, nil
}

func (h *AutomationHost) processSeam() bool {
	return h != nil && h.IOS != nil && h.IOS.pidProbe != nil
}

func (h *AutomationHost) tethered(id string) bool {
	// Hermetic AppPID tests have no usbmux. Production adapters leave pidProbe nil.
	if h.processSeam() {
		return true
	}
	list, err := h.IOS.List()
	if err != nil {
		return false
	}
	for _, info := range list {
		if info.UUID == id {
			return true
		}
	}
	return false
}

func (h *AutomationHost) locked(id string) (locked, known bool, detail string) {
	if h.lockRead != nil {
		resp, err := h.lockRead(id)
		return interpretLock(resp, err)
	}
	// Hermetic process probes have no device. A test that supplies
	// lockExec is driving the devicectl parser, so it must not take
	// this shortcut.
	if h.processSeam() && h.lockExec == nil {
		return false, true, "process probe seam"
	}
	locked, known, detail = h.lockViaDevicectl(id)
	if known || h.lockExec != nil {
		return locked, known, detail
	}
	if h.supportsRSD(id) {
		// iOS 17+ springboard getLockState connects and the device
		// closes the socket. That EOF is not an unlock.
		return false, false, detail
	}
	sbLocked, sbKnown, sbDetail := h.lockViaSpringboard(id)
	if sbKnown {
		return sbLocked, sbKnown, sbDetail
	}
	if detail == "" {
		return false, false, sbDetail
	}
	return false, false, detail + "; " + sbDetail
}

// lockViaDevicectl runs `xcrun devicectl device info lockState`. A
// command or parse failure is not a known unlock.
func (h *AutomationHost) lockViaDevicectl(id string) (bool, bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), agentProbeBudget)
	defer cancel()
	var raw []byte
	var err error
	if h.lockExec != nil {
		raw, err = h.lockExec(ctx, id)
	} else {
		raw, err = devicectlLockState(ctx, id)
	}
	if err != nil {
		return false, false, err.Error()
	}
	return interpretDevicectlLock(raw)
}

func devicectlLockState(ctx context.Context, udid string) ([]byte, error) {
	if strings.TrimSpace(udid) == "" {
		return nil, errors.New("devicectl lockState requires a udid")
	}
	timeout := int(agentProbeBudget / time.Second)
	cmd := exec.CommandContext(ctx, "xcrun", "devicectl", "device", "info", "lockState",
		"--device", udid,
		"--timeout", fmt.Sprintf("%d", timeout),
		"--json-output", "-",
		"--quiet")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return nil, fmt.Errorf("devicectl lockState: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("devicectl lockState: %w", err)
	}
	return out, nil
}

// interpretDevicectlLock reads the lockState JSON document.
// passcodeRequired means the screen is locked. unlockedSinceBoot false
// means the device has not been unlocked since boot, which is also locked.
// A missing or failed document is not known.
func interpretDevicectlLock(raw []byte) (locked, known bool, detail string) {
	var doc struct {
		Info struct {
			Outcome string `json:"outcome"`
		} `json:"info"`
		Result struct {
			PasscodeRequired  *bool `json:"passcodeRequired"`
			UnlockedSinceBoot *bool `json:"unlockedSinceBoot"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return false, false, "devicectl lockState unreadable: " + err.Error()
	}
	if doc.Info.Outcome != "success" {
		if doc.Info.Outcome == "" {
			return false, false, "devicectl lockState missing outcome"
		}
		return false, false, "devicectl lockState outcome=" + doc.Info.Outcome
	}
	if doc.Result.PasscodeRequired == nil {
		return false, false, "devicectl lockState missing passcodeRequired"
	}
	if *doc.Result.PasscodeRequired {
		return true, true, "devicectl passcodeRequired=true"
	}
	if doc.Result.UnlockedSinceBoot != nil && !*doc.Result.UnlockedSinceBoot {
		return true, true, "devicectl unlockedSinceBoot=false"
	}
	return false, true, "devicectl passcodeRequired=false"
}

func (h *AutomationHost) supportsRSD(id string) bool {
	if h == nil || h.IOS == nil || h.IOS.goios == nil || id == "" {
		return false
	}
	dev, err := h.IOS.goios.Session(id)
	if err != nil {
		return false
	}
	return dev.SupportsRsd()
}

func (h *AutomationHost) lockViaSpringboard(id string) (bool, bool, string) {
	if h.IOS == nil || h.IOS.goios == nil {
		return false, false, "ios session is not configured"
	}
	dev, err := h.IOS.goios.Session(id)
	if err != nil {
		return false, false, err.Error()
	}
	type result struct {
		locked bool
		known  bool
		detail string
	}
	ch := make(chan result, 1)
	box := &lockConn{}
	go func() {
		c, err := connectSpringboard(dev)
		if err != nil {
			ch <- result{detail: err.Error()}
			return
		}
		if !box.store(c) {
			return
		}
		defer box.close()
		codec := goios_ios.NewPlistCodecReadWriter(c.Reader(), c.Writer())
		if err := codec.Write(map[string]any{"command": "getLockState"}); err != nil {
			ch <- result{detail: err.Error()}
			return
		}
		var resp map[string]any
		if err := codec.Read(&resp); err != nil {
			ch <- result{detail: err.Error()}
			return
		}
		locked, known, detail := interpretLock(resp, nil)
		ch <- result{locked: locked, known: known, detail: detail}
	}()
	timer := time.NewTimer(lockProbeBudget)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.locked, r.known, r.detail
	case <-timer.C:
		box.close()
		return false, false, "lock probe timed out"
	}
}

// lockConn closes a springboard connection exactly once. store after
// close closes the new connection immediately so a late dial cannot
// outlive the probe budget.
type lockConn struct {
	mu     sync.Mutex
	c      goios_ios.DeviceConnectionInterface
	closed bool
}

func (b *lockConn) store(c goios_ios.DeviceConnectionInterface) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		c.Close()
		return false
	}
	b.c = c
	return true
}

func (b *lockConn) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	if b.c != nil {
		b.c.Close()
		b.c = nil
	}
}

// connectSpringboard opens getLockState on the lockdown service. iOS 17+
// does not use this path: the device closes that socket, and the RSD
// shim resets during checkin. Those devices use devicectl lockState.
func connectSpringboard(dev goios_ios.DeviceEntry) (goios_ios.DeviceConnectionInterface, error) {
	return goios_ios.ConnectToService(dev, "com.apple.springboardservices")
}

// interpretLock turns one getLockState result into the supervision bit.
// A read error or an unreadable state is not locked and not known, so
// the supervisor must not report ready. state != 0 is locked.
func interpretLock(resp map[string]any, err error) (locked, known bool, detail string) {
	if err != nil {
		return false, false, err.Error()
	}
	if resp == nil {
		return false, false, "lock probe returned no state"
	}
	state, ok := lockStateValue(resp["state"])
	if !ok {
		return false, false, fmt.Sprintf("lock probe state unreadable: %#v", resp["state"])
	}
	return state != 0, true, fmt.Sprintf("springboard state=%d", state)
}

func lockStateValue(v any) (uint64, bool) {
	switch n := v.(type) {
	case uint64:
		return n, true
	case uint32:
		return uint64(n), true
	case int:
		if n < 0 {
			return 0, false
		}
		return uint64(n), true
	case int64:
		if n < 0 {
			return 0, false
		}
		return uint64(n), true
	case float64:
		if n < 0 {
			return 0, false
		}
		return uint64(n), true
	default:
		return 0, false
	}
}

func (h *AutomationHost) bootID(id string) string {
	if h.bootSource != nil {
		return h.bootSource(id)
	}
	if h.IOS == nil || h.IOS.goios == nil || id == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentProbeBudget)
	defer cancel()
	got, err := h.IOS.goios.BootSessionUUID(ctx, id)
	if err != nil || got == "" {
		slog.Warn("boot session id unavailable", "device", id, "error", err)
		return ""
	}
	return got
}

func (h *AutomationHost) appPID(id, bundle string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), agentProbeBudget)
	defer cancel()
	return h.IOS.AppPIDContext(ctx, id, bundle)
}

func (h *AutomationHost) installed(id string) (bool, string, error) {
	if h.processSeam() {
		return true, "1.0+1.0", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentProbeBudget)
	defer cancel()
	apps, err := h.IOS.ListAppsContext(ctx, id)
	if err != nil {
		if gen, ok := h.cachedGen(id); ok {
			return true, gen, nil
		}
		return false, "", err
	}
	var hostVer, runnerVer string
	hostOK, runnerOK := false, false
	for _, app := range apps {
		switch app.BundleID {
		case alertHostBundle:
			hostOK = true
			hostVer = app.Version
		case alertRunnerBundle:
			runnerOK = true
			runnerVer = app.Version
		}
	}
	if !hostOK || !runnerOK {
		return false, "", nil
	}
	if runnerVer == "" {
		runnerVer = "unknown"
	}
	gen := hostVer + "+" + runnerVer
	h.rememberGen(id, gen)
	return true, gen, nil
}

func (h *AutomationHost) rememberGen(id, gen string) {
	h.mu.Lock()
	h.installGen[id] = gen
	h.mu.Unlock()
}

func (h *AutomationHost) cachedGen(id string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	gen := h.installGen[id]
	return gen, gen != ""
}

func appAbsent(err error) bool {
	return err != nil && strings.Contains(err.Error(), "app not running")
}

func (h *AutomationHost) heldPID(id string) (int, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	hold := h.holds[id]
	if hold == nil || hold.pid <= 0 {
		return 0, false
	}
	return hold.pid, true
}

// observeRunner reads the supervised xctrunner. A stuck process list is
// not a dead runner: one retry, then the pid of a hold this process
// still owns. Only "app not running" means the runner is gone.
func (h *AutomationHost) observeRunner(id string) (int, bool, string, error) {
	pid, err := h.appPID(id, alertRunnerBundle)
	if err == nil && pid > 0 {
		return pid, true, fmt.Sprintf("runner pid %d", pid), nil
	}
	if appAbsent(err) {
		return 0, false, err.Error(), nil
	}
	first := err
	if err != nil {
		pid, err = h.appPID(id, alertRunnerBundle)
		if err == nil && pid > 0 {
			return pid, true, fmt.Sprintf("runner pid %d after retry (%v)", pid, first), nil
		}
		if appAbsent(err) {
			return 0, false, err.Error(), nil
		}
	}
	if hold, ok := h.heldPID(id); ok {
		return hold, true, fmt.Sprintf("process list failed (%v); reusing open hold pid %d", err, hold), nil
	}
	if err == nil {
		return 0, false, "runner pid 0", nil
	}
	return 0, false, err.Error(), err
}

// Launch replaces an unhealthy runner with one resident XCTest session.
// The supervisor calls it only for a dead or unauthorized runner.
func (h *AutomationHost) Launch(id string) (int, error) {
	if pid, alive, _, err := h.observeRunner(id); err == nil && alive && pid > 0 {
		return pid, nil
	}
	h.stopHold(id)
	ctx, cancel := context.WithTimeout(context.Background(), agentProbeBudget)
	_ = h.IOS.TerminateAppContext(ctx, id, alertRunnerBundle)
	cancel()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pid, err := h.appPID(id, alertRunnerBundle)
		if err != nil || pid <= 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	pid, err := h.startHold(id)
	if err != nil {
		return 0, err
	}
	h.mu.Lock()
	h.probeNote[id] = fmt.Sprintf("launched runner pid %d", pid)
	h.mu.Unlock()
	return pid, nil
}

func (h *AutomationHost) startHold(id string) (int, error) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		defer close(done)
		errCh <- h.runHold(ctx, id)
	}()
	timer := time.NewTimer(agentLaunchBudget)
	defer timer.Stop()
	var stableSince time.Time
	for {
		select {
		case err := <-errCh:
			cancel()
			<-done
			if consentish(err) {
				h.mu.Lock()
				h.consent[id] = err.Error()
				h.mu.Unlock()
				return 0, fmt.Errorf("%w: %s", agent.ErrConsent, err.Error())
			}
			if err == nil {
				err = errors.New("resident XCTest ended")
			}
			return 0, err
		case <-timer.C:
			cancel()
			<-done
			return 0, fmt.Errorf("resident XCTest runner did not stay up on %s", id)
		case <-time.After(200 * time.Millisecond):
			pid, err := h.appPID(id, alertRunnerBundle)
			if err != nil || pid <= 0 {
				stableSince = time.Time{}
				continue
			}
			if stableSince.IsZero() {
				stableSince = time.Now()
				continue
			}
			if time.Since(stableSince) < agentHoldSettle {
				continue
			}
			h.mu.Lock()
			h.holds[id] = &runnerHold{cancel: cancel, done: done, pid: pid}
			delete(h.consent, id)
			h.launches++
			h.mu.Unlock()
			return pid, nil
		}
	}
}

func (h *AutomationHost) runHold(ctx context.Context, id string) error {
	dev, err := h.IOS.goios.Session(id)
	if err != nil {
		return fmt.Errorf("agent session on %s: %w", id, err)
	}
	listener := testmanagerd.NewTestListener(io.Discard, io.Discard, os.TempDir())
	_, err = testmanagerd.RunTestWithConfig(ctx, testmanagerd.TestConfig{
		BundleId:           alertHostBundle,
		TestRunnerBundleId: alertRunnerBundle,
		XctestConfigName:   alertRunnerConfig,
		TestsToRun:         []string{agent.HoldTest},
		TestsToSkip:        nil,
		Device:             dev,
		Listener:           listener,
	})
	if err != nil && consentish(err) {
		return err
	}
	if err != nil {
		return fmt.Errorf("resident XCTest on %s: %w", id, err)
	}
	return nil
}

func (h *AutomationHost) stopHold(id string) {
	h.mu.Lock()
	hold := h.holds[id]
	delete(h.holds, id)
	h.mu.Unlock()
	if hold == nil {
		return
	}
	hold.cancel()
	select {
	case <-hold.done:
	case <-time.After(8 * time.Second):
	}
}

// Install copies the runner only when it is absent. A present bundle
// is left untouched and is not counted as an install.
func (h *AutomationHost) Install(id string) (string, error) {
	ok, gen, err := h.installed(id)
	if err != nil {
		return "", err
	}
	if ok {
		return gen, nil
	}
	hostApp, runnerApp, err := alertRunnerProducts()
	if err != nil {
		return "", err
	}
	if err := h.IOS.InstallApp(id, hostApp); err != nil {
		return "", err
	}
	if err := h.IOS.InstallApp(id, runnerApp); err != nil {
		return "", err
	}
	h.mu.Lock()
	h.installs++
	h.mu.Unlock()
	_, gen, err = h.installed(id)
	return gen, err
}

// agentStopAttempts is the bounded retry when a process-list read sticks
// during idle. The second call runs only after the first context cancel
// has closed that DTX connection.
const agentStopAttempts = 2

// StopAUT terminates the app under test and leaves the runner alone.
// The bundle launch_app recorded wins over the supervisor's last
// observation so idle stops that app even when the machine has not
// probed since the launch.
func (h *AutomationHost) StopAUT(id, bundle string) error {
	if noted := h.autBundle(id); noted != "" {
		bundle = noted
	}
	if bundle == "" || bundle == alertRunnerBundle || bundle == alertHostBundle {
		return nil
	}
	var err error
	for attempt := 0; attempt < agentStopAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), agentProbeBudget)
		err = h.IOS.TerminateAppContext(ctx, id, bundle)
		cancel()
		if err == nil {
			return nil
		}
	}
	return err
}

// DropHostSession closes client build/test sessions. It does not cancel
// the resident XCTest channel: that channel is the runner's process,
// and idle is allowed to keep the runner.
func (h *AutomationHost) DropHostSession(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clientSessions[id] = 0
	h.hostDrops++
	return nil
}

// DropLease clears the build/Unity lease flag Spyder itself recorded.
func (h *AutomationHost) DropLease(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.leases[id] = false
	h.leaseDrops++
	return nil
}

// HealthyPID is the resident runner process, when this host is holding
// it. A one-shot alert tap must not start a second runner in that case.
func (h *AutomationHost) HealthyPID(id string) (int, bool) {
	if _, ok := h.heldPID(id); !ok {
		return 0, false
	}
	pid, alive, _, err := h.observeRunner(id)
	if err != nil || !alive || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// Shutdown cancels every resident XCTest session. Idle does not call
// this; process shutdown does.
func (h *AutomationHost) Shutdown() {
	if h == nil {
		return
	}
	h.mu.Lock()
	ids := make([]string, 0, len(h.holds))
	for id := range h.holds {
		ids = append(ids, id)
	}
	h.mu.Unlock()
	for _, id := range ids {
		h.stopHold(id)
	}
}

// TapButton asks the resident runner to press a SpringBoard button.
func (h *AutomationHost) TapButton(id, button string) error {
	if h.IOS == nil || h.IOS.goios == nil {
		return errors.New("ios session is not configured")
	}
	dev, err := h.IOS.goios.Session(id)
	if err != nil {
		return err
	}
	afc, err := house_arrest.New(dev, alertRunnerBundle)
	if err != nil {
		return fmt.Errorf("resident agent documents: %w", err)
	}
	defer afc.Close()
	_ = afc.MkDir("Documents")
	if err := afc.WriteToFile(strings.NewReader("wait\n"), "Documents/spyder-agent-result.txt"); err != nil {
		return err
	}
	if err := afc.WriteToFile(strings.NewReader(button+"\n"), "Documents/spyder-agent-cmd.txt"); err != nil {
		return err
	}
	deadline := time.Now().Add(12 * time.Second)
	tmp := filepath.Join(os.TempDir(), "spyder-agent-result-"+id+".txt")
	for time.Now().Before(deadline) {
		if err := afc.PullSingleFile("Documents/spyder-agent-result.txt", tmp); err == nil {
			body, readErr := os.ReadFile(tmp)
			if readErr == nil {
				switch strings.TrimSpace(string(body)) {
				case "tapped":
					return nil
				case "missing":
					return fmt.Errorf("resident agent found no system alert button %q", button)
				}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("resident agent did not tap %q", button)
}

func consentish(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "enabling automation") ||
		strings.Contains(s, "ui automation") ||
		strings.Contains(s, "passcode") ||
		strings.Contains(s, "touch id")
}

func alertRunnerProducts() (hostApp, runnerApp string, err error) {
	roots := []string{}
	if dir := os.Getenv("SPYDER_ALERT_RUNNER_DIR"); dir != "" {
		roots = append(roots, dir)
	}
	roots = append(roots,
		filepath.Join("ios", "AlertRunner", "build", "Build", "Products", "Debug-iphoneos"),
		filepath.Join("AlertRunner", "build", "Build", "Products", "Debug-iphoneos"),
	)
	for _, root := range roots {
		host := filepath.Join(root, "AlertHost.app")
		runner := filepath.Join(root, "AlertRunner-Runner.app")
		if st, err := os.Stat(host); err == nil && st.IsDir() {
			if st, err := os.Stat(runner); err == nil && st.IsDir() {
				return host, runner, nil
			}
		}
	}
	return "", "", errors.New("alert runner products not found; run make alert-runner")
}

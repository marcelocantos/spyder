// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	goios_ios "github.com/danielpaulus/go-ios/ios"

	"github.com/marcelocantos/spyder/internal/agent"
)

// DeviceKit is the game-input agent Stock Cars and 🎯T167 already speak
// to: a JSON-RPC server on device port 12004 inside an XCUITest runner.
// The daemon can keep it resident instead of AlertRunner (🎯T175).
const (
	DeviceKitRunnerBundle = "com.mobilenext.devicekit-iosUITests.xctrunner"
	deviceKitHostBundle   = "com.mobilenext.devicekit-ios"
	deviceKitPort         = 12004
	deviceKitProbeBudget  = 2 * time.Second
	deviceKitMaxResponse  = 64 * 1024
	deviceKitHealthID     = "spyder-agent-health"
)

// Environment that selects the backend. Daemon configuration only;
// no client request names a binary or a bundle.
const (
	EnvIOSAgentBackend = "SPYDER_IOS_AGENT_BACKEND"
	EnvIOSAgentDevices = "SPYDER_IOS_AGENT_DEVICES"
	// EnvIOSAgentConsentWindow bounds how long a live launch waits on
	// the owner's Enable UI Automation approval (Go duration; default
	// 10m). Past it the launch is stopped and consent is recorded.
	EnvIOSAgentConsentWindow = "SPYDER_IOS_AGENT_CONSENT_WINDOW"
)

// DefaultConsentWindow is the consent window when the env is unset.
const DefaultConsentWindow = 10 * time.Minute

var hardwareUDID = regexp.MustCompile(`^([0-9A-Fa-f]{8}-[0-9A-Fa-f]{16}|[0-9A-Fa-f]{40})$`)

// DeviceKitAgent is the DeviceKit backend configuration. CLI is the
// go-ios the daemon already uses for tunnels and 🎯T167 (the bundled
// libexec/spyder/ios, or SPYDER_IOS_TUNNEL_BINARY).
type DeviceKitAgent struct {
	CLI string
	// ConsentWindow is how long a launch may wait for approval. Zero
	// means DefaultConsentWindow.
	ConsentWindow time.Duration
	all           bool
	devices       map[string]bool
	err           error
}

// DeviceKitAgentFromEnv reads SPYDER_IOS_AGENT_BACKEND and
// SPYDER_IOS_AGENT_DEVICES. Unset or "alertrunner" keeps the AlertRunner
// default and returns nil. "devicekit" needs an explicit device scope:
// a comma-separated list of hardware UDIDs, or "*" for every tethered
// device. Anything else is returned with Err set; supervision then
// fails closed instead of silently picking a backend.
func DeviceKitAgentFromEnv(iosBin string) *DeviceKitAgent {
	p := deviceKitAgentFrom(os.Getenv(EnvIOSAgentBackend), os.Getenv(EnvIOSAgentDevices), iosBin)
	if p != nil && p.err == nil {
		p.ConsentWindow, p.err = parseConsentWindow(os.Getenv(EnvIOSAgentConsentWindow))
	}
	return p
}

func parseConsentWindow(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return DefaultConsentWindow, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < time.Minute || d > 2*time.Hour {
		return 0, fmt.Errorf("%s=%q: want a Go duration between 1m and 2h", EnvIOSAgentConsentWindow, v)
	}
	return d, nil
}

func (p *DeviceKitAgent) consentWindow() time.Duration {
	if p == nil || p.ConsentWindow <= 0 {
		return DefaultConsentWindow
	}
	return p.ConsentWindow
}

func deviceKitAgentFrom(backend, devices, iosBin string) *DeviceKitAgent {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "", "alertrunner":
		return nil
	case "devicekit":
	default:
		return &DeviceKitAgent{err: fmt.Errorf("%s=%q: want devicekit or alertrunner", EnvIOSAgentBackend, backend)}
	}
	p := &DeviceKitAgent{CLI: iosBin, devices: map[string]bool{}}
	devices = strings.TrimSpace(devices)
	if devices == "*" {
		p.all = true
		return p
	}
	for _, id := range strings.Split(devices, ",") {
		id = strings.TrimSpace(id)
		if !hardwareUDID.MatchString(id) {
			p.err = fmt.Errorf("%s needs hardware UDIDs or *; got %q", EnvIOSAgentDevices, id)
			return p
		}
		p.devices[id] = true
	}
	return p
}

// Err is the configuration error, if any.
func (p *DeviceKitAgent) Err() error {
	if p == nil {
		return nil
	}
	return p.err
}

// Selects reports whether id runs the DeviceKit backend.
func (p *DeviceKitAgent) Selects(id string) bool {
	return p != nil && p.err == nil && id != "" && (p.all || p.devices[id])
}

// cli checks the launcher at use time. It is the same binary the
// daemon trusts for tunnels, so there is no separate pin.
func (p *DeviceKitAgent) cli() (string, error) {
	if p.CLI == "" {
		return "", errors.New("bundled go-ios (libexec/spyder/ios) not found; DeviceKit runner cannot start")
	}
	path := p.CLI
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		path = abs
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%s is not an executable file", path)
	}
	return path, nil
}

// usesDeviceKit is true for devices the DeviceKit backend supervises.
func (h *AutomationHost) usesDeviceKit(id string) bool {
	return h != nil && h.DeviceKit.Selects(id)
}

// RunnerBundleFor is the supervised runner for id.
func (h *AutomationHost) RunnerBundleFor(id string) string {
	if h.usesDeviceKit(id) {
		return DeviceKitRunnerBundle
	}
	return alertRunnerBundle
}

func isAgentBundle(bundle string) bool {
	switch bundle {
	case alertRunnerBundle, alertHostBundle, DeviceKitRunnerBundle, deviceKitHostBundle:
		return true
	}
	return false
}

// launchDeviceKit starts the resident DeviceKit runner. Ownership is
// the launch this process spawned (🎯T175):
//   - a live launch of ours that is still launching or waiting on
//     consent is never replaced; Launch reports it pending;
//   - our established runner that stopped answering is replaced;
//   - a runner on the device with no live host on this Mac is stale:
//     it is terminated on the device (process kill, no reinstall);
//   - an orphaned runwda left by a previous spyder daemon (spyder's own
//     bundled ios, parent gone) is terminated, then relaunched;
//   - any other host (Stock Cars' harness, other tools, a live spyder
//     daemon) is foreign and left alone.
func (h *AutomationHost) launchDeviceKit(id string) (int, error) {
	if _, err := h.DeviceKit.cli(); err != nil {
		return 0, err
	}
	h.mu.Lock()
	hold := h.liveHold(id)
	var waiting bool
	var msg string
	if hold != nil {
		waiting, msg = hold.launching || hold.pending, hold.msg
	}
	h.mu.Unlock()
	if waiting {
		if msg == "" {
			msg = fmt.Sprintf("DeviceKit launch on %s is in progress", id)
		}
		return 0, fmt.Errorf("%w: %s", agent.ErrConsentPending, msg)
	}
	pid, err := h.appPID(id, DeviceKitRunnerBundle)
	if err != nil && !appAbsent(err) {
		return 0, fmt.Errorf("DeviceKit runner lookup on %s: %w", id, err)
	}
	alive := err == nil && pid > 0
	if hold != nil {
		if alive && h.ready(id, pid) {
			h.mu.Lock()
			hold.pid = pid
			h.mu.Unlock()
			return pid, nil
		}
		// Our own runner stopped answering. Replace it.
		h.stopHold(id)
	} else {
		orphans, err := h.unownedHosts(id)
		if err != nil {
			return 0, err
		}
		for _, p := range orphans {
			h.stopOrphan(id, p)
		}
		if alive {
			slog.Warn("terminating stale DeviceKit runner with no live host", "device", id, "runner_pid", pid)
		}
	}
	if alive {
		ctx, cancel := context.WithTimeout(context.Background(), agentProbeBudget)
		if err := h.IOS.TerminateAppContext(ctx, id, DeviceKitRunnerBundle); err != nil {
			slog.Warn("terminate DeviceKit runner", "device", id, "runner_pid", pid, "error", err)
		}
		cancel()
		h.waitRunnerGone(id, DeviceKitRunnerBundle)
	}
	pid, err = h.startHold(id)
	if err != nil {
		return 0, err
	}
	h.mu.Lock()
	h.probeNote[id] = fmt.Sprintf("launched DeviceKit runner pid %d", pid)
	h.mu.Unlock()
	return pid, nil
}

// dkProbe is Probe's DeviceKit ownership and readiness verdict.
type dkProbe struct {
	authorized, pending, approved, expired bool
	msg                                    string
	hostPID                                int
}

// probeDeviceKit decides ownership by the live launch, not by the
// device pid, so a runner that restarts under approval stays ours.
func (h *AutomationHost) probeDeviceKit(id string, pid int, alive bool) (dkProbe, error) {
	h.mu.Lock()
	hold := h.liveHold(id)
	var st dkProbe
	var launching, pending bool
	var since time.Time
	if hold != nil {
		if alive {
			hold.pid = pid
		}
		st.hostPID, st.msg = hold.hostPID, hold.msg
		launching, pending, since = hold.launching, hold.pending, hold.since
	}
	h.mu.Unlock()
	switch {
	case hold != nil && launching:
		st.pending = true
		if st.msg == "" {
			st.msg = fmt.Sprintf("DeviceKit launch on %s is in progress", id)
		}
		return st, nil
	case hold != nil && pending:
		if alive && h.ready(id, pid) {
			h.approve(id, hold, pid)
			st.authorized, st.approved, st.msg = true, true, ""
			return st, nil
		}
		if time.Since(since) > h.DeviceKit.consentWindow() {
			h.expirePending(id, hold)
			st.expired = true
			return st, nil
		}
		st.pending = true
		return st, nil
	case hold != nil:
		st.authorized = alive && h.readyTwice(id, pid)
		return st, nil
	}
	// No launch of ours. A host on this Mac that is not a spyder orphan
	// is foreign; otherwise an alive runner is stale and is replaced.
	if _, err := h.unownedHosts(id); err != nil {
		return st, err
	}
	if alive {
		h.mu.Lock()
		h.probeNote[id] = fmt.Sprintf("stale DeviceKit runner pid %d with no live host; it is terminated before the next launch", pid)
		h.mu.Unlock()
	}
	return st, nil
}

// approve marks a pending launch ready: the owner approved and the
// runner answered on this same launch.
func (h *AutomationHost) approve(id string, hold *runnerHold, pid int) {
	h.mu.Lock()
	if !hold.pending {
		h.mu.Unlock()
		return
	}
	hold.pending = false
	hold.pid = pid
	hold.msg = ""
	delete(h.consent, id)
	h.launches++
	h.probeNote[id] = fmt.Sprintf("DeviceKit runner pid %d approved and answering", pid)
	hostPID := hold.hostPID
	h.mu.Unlock()
	slog.Info("resident agent consent approved; runner answering", "device", id, "host_pid", hostPID, "runner_pid", pid)
}

// expirePending stops a launch whose consent window passed and records
// consent, so the next prompt waits for an attended retry or reboot.
func (h *AutomationHost) expirePending(id string, hold *runnerHold) {
	window := h.DeviceKit.consentWindow()
	h.mu.Lock()
	if !hold.pending {
		h.mu.Unlock()
		return
	}
	hold.expired = true
	hostPID := hold.hostPID
	msg := fmt.Sprintf("no Enable UI Automation approval within %s of launching (host pid %d); launch stopped. Use agent_ensure retry=true with someone at the device.", window, hostPID)
	h.consent[id] = msg
	if h.holds[id] == hold {
		delete(h.holds, id)
	}
	h.mu.Unlock()
	slog.Warn("resident agent consent window elapsed", "device", id, "host_pid", hostPID, "window", window)
	hold.cancel()
	select {
	case <-hold.done:
	case <-time.After(8 * time.Second):
	}
}

// watchPending keeps polling readiness on a launch waiting for consent
// until the runner answers, the launch exits, or the window passes.
func (h *AutomationHost) watchPending(id string, hold *runnerHold) {
	every := h.pendingPoll
	if every <= 0 {
		every = 3 * time.Second
	}
	window := h.DeviceKit.consentWindow()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-hold.done:
			return
		case <-t.C:
		}
		h.mu.Lock()
		pending, since := hold.pending, hold.since
		h.mu.Unlock()
		if !pending {
			return
		}
		if time.Since(since) > window {
			h.expirePending(id, hold)
			return
		}
		pid, err := h.appPID(id, DeviceKitRunnerBundle)
		if err != nil || pid <= 0 {
			continue
		}
		h.mu.Lock()
		hold.pid = pid
		h.mu.Unlock()
		if h.ready(id, pid) {
			h.approve(id, hold, pid)
			return
		}
	}
}

// runwdaProc is one `ios runwda` process on this Mac.
type runwdaProc struct {
	PID, PPID int
	Binary    string
	Bundle    string
}

func (h *AutomationHost) scanHosts(id string) []runwdaProc {
	if h.hostScan != nil {
		return h.hostScan(id)
	}
	out, err := exec.Command("ps", "-axww", "-o", "pid=,ppid=,command=").Output()
	if err != nil {
		return nil
	}
	return parseRunwdaProcs(string(out), id)
}

func parseRunwdaProcs(ps, udid string) []runwdaProc {
	var procs []runwdaProc
	for _, line := range strings.Split(ps, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		ident, ok := parseRunwdaPS(fields[0] + " " + strings.Join(fields[2:], " "))
		if !ok || ident.UDID != udid {
			continue
		}
		procs = append(procs, runwdaProc{PID: pid, PPID: ppid, Binary: ident.Binary, Bundle: ident.Bundle})
	}
	return procs
}

func (h *AutomationHost) pidAlive(pid int) bool {
	if h.hostAlive != nil {
		return h.hostAlive(pid)
	}
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func (h *AutomationHost) signalHost(pid int, sig syscall.Signal) error {
	if h.hostSignal != nil {
		return h.hostSignal(pid, sig)
	}
	return syscall.Kill(pid, sig)
}

// spyderIOS is spyder's own bundled go-ios: this daemon's, or another
// installed spyder version's libexec/spyder/ios.
func (h *AutomationHost) spyderIOS(bin string) bool {
	if bin == "" {
		return false
	}
	if h.DeviceKit != nil && h.DeviceKit.CLI != "" && filepath.Clean(bin) == filepath.Clean(h.DeviceKit.CLI) {
		return true
	}
	return strings.HasSuffix(filepath.ToSlash(bin), "/libexec/spyder/ios")
}

// unownedHosts sorts runwda hosts for id that this process did not
// spawn. Orphans of a previous spyder daemon (spyder's bundled ios,
// DeviceKit bundle, parent gone) are returned for termination. Any
// other host is foreign and is an error: spyder never touches it.
func (h *AutomationHost) unownedHosts(id string) ([]runwdaProc, error) {
	var orphans []runwdaProc
	for _, p := range h.scanHosts(id) {
		if h.ownsHost(p.PID) {
			continue
		}
		if p.Bundle == DeviceKitRunnerBundle && h.spyderIOS(p.Binary) && (p.PPID <= 1 || !h.pidAlive(p.PPID)) {
			orphans = append(orphans, p)
			continue
		}
		return nil, fmt.Errorf("DeviceKit runner on %s was not started by this daemon; leaving it alone (host: pid %d %s runwda, parent pid %d; stop that harness or daemon, then agent_ensure)", id, p.PID, p.Binary, p.PPID)
	}
	return orphans, nil
}

func (h *AutomationHost) ownsHost(pid int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, hold := range h.holds {
		if hold.hostPID == pid {
			return true
		}
	}
	return false
}

// stopOrphan ends a previous spyder daemon's runwda: SIGTERM to its
// process group, then SIGKILL after a grace period.
func (h *AutomationHost) stopOrphan(id string, p runwdaProc) {
	slog.Warn("terminating orphaned spyder runwda from a previous daemon", "device", id, "host_pid", p.PID, "parent_pid", p.PPID, "binary", p.Binary)
	_ = h.signalHost(p.PID, syscall.SIGTERM)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !h.pidAlive(p.PID) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = h.signalHost(p.PID, syscall.SIGKILL)
}

func (h *AutomationHost) waitRunnerGone(id, bundle string) {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pid, err := h.appPID(id, bundle)
		if err != nil || pid <= 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// runDeviceKit runs `ios runwda` for the installed DeviceKit runner
// until ctx ends. There is no per-client or hourly lifetime: the
// daemon's hold ends it, and agent_idle does not. started receives the
// host pid, which is what ownership keys on.
func (h *AutomationHost) runDeviceKit(ctx context.Context, id string, out io.Writer, started func(int)) error {
	bin, err := h.DeviceKit.cli()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, defaultRunwdaArgs(id, DeviceKitRunnerBundle)...)
	// Own process group: a terminal ^C on the daemon's group must not
	// reach the runner before Shutdown stops it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 8 * time.Second
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("DeviceKit runwda on %s: start: %w", id, err)
	}
	if started != nil {
		started(cmd.Process.Pid)
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return nil
	}
	text := ""
	if tb, ok := out.(*tailBuf); ok {
		text = tb.string()
	}
	status := "exit status 0"
	if err != nil {
		status = err.Error()
	}
	if marker := runwdaFailure(text); marker != "" {
		return fmt.Errorf("DeviceKit runwda on %s: %s (%s)", id, marker, status)
	}
	if err == nil {
		return fmt.Errorf("DeviceKit runwda on %s exited (%s): %s", id, status, lastLine(text))
	}
	return fmt.Errorf("DeviceKit runwda on %s: %w (%s)", id, err, lastLine(text))
}

// ready is the readiness gate for a DeviceKit runner pid. Tests inject
// readyProbe; production asks the runner over that device's USB.
func (h *AutomationHost) ready(id string, pid int) bool {
	if h.readyProbe != nil {
		return h.readyProbe(id, pid)
	}
	return h.deviceKitReady(id, pid)
}

// deviceKitReady asks the runner over this exact USB device. A live
// runner pid alone also exists while iOS waits for the owner's
// Touch ID or passcode, so only a JSON-RPC answer counts.
func (h *AutomationHost) deviceKitReady(id string, pid int) bool {
	if h.IOS == nil || h.IOS.goios == nil {
		return false
	}
	dev, err := h.IOS.goios.Session(id)
	if err != nil {
		return false
	}
	network, address := goios_ios.GetSocketTypeAndAddress(goios_ios.GetUsbmuxdSocket())
	conn, err := net.DialTimeout(network, address, deviceKitProbeBudget)
	if err != nil {
		return false
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(deviceKitProbeBudget)); err != nil {
		return false
	}
	mux := goios_ios.NewUsbMuxConnection(goios_ios.NewDeviceConnectionWithConn(conn))
	if err := mux.Connect(dev.DeviceID, deviceKitPort); err != nil {
		return false
	}
	body := `{"jsonrpc":"2.0","id":"` + deviceKitHealthID + `","method":"device.apps.state","params":{"bundleId":"` + DeviceKitRunnerBundle + `"}}`
	if _, err := fmt.Fprintf(conn, "POST /rpc HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body); err != nil {
		return false
	}
	return deviceKitStateReady(conn, pid)
}

// deviceKitStateReady accepts only this request's answer about the
// runner itself: background with pid 0, or foreground with the pid the
// probe saw.
func deviceKitStateReady(reader io.Reader, pid int) bool {
	response, err := http.ReadResponse(bufio.NewReader(io.LimitReader(reader, deviceKitMaxResponse)), nil)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	var value struct {
		ID      string          `json:"id"`
		Version string          `json:"jsonrpc"`
		Error   json.RawMessage `json:"error"`
		Result  struct {
			Bundle string `json:"bundleId"`
			State  string `json:"state"`
			PID    *int   `json:"pid"`
		} `json:"result"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&value) != nil {
		return false
	}
	if value.ID != deviceKitHealthID || value.Version != "2.0" || len(value.Error) != 0 ||
		value.Result.Bundle != DeviceKitRunnerBundle || value.Result.PID == nil {
		return false
	}
	switch value.Result.State {
	case "runningBackground":
		return *value.Result.PID == 0
	case "runningForeground":
		return *value.Result.PID == pid
	}
	return false
}

// initGate holds this process's RSD/XPC initializer lock for one device
// while `ios runwda` performs its own handshake (🎯T161, 🎯T167). The
// startup poll waits for it, so no lookup of ours overlaps that child.
type initGate struct {
	once   sync.Once
	ch     chan struct{}
	unlock func()
}

func (g *initGate) release() {
	g.once.Do(func() {
		if g.unlock != nil {
			g.unlock()
		}
		close(g.ch)
	})
}

func (g *initGate) done() bool {
	select {
	case <-g.ch:
		return true
	default:
		return false
	}
}

func (h *AutomationHost) holdInitializer(id string, deviceKit bool) *initGate {
	g := &initGate{ch: make(chan struct{})}
	if !deviceKit || h.holdRun != nil || h.IOS == nil || h.IOS.goios == nil {
		g.release()
		return g
	}
	// Resolve first: resolve takes the same lock, so it cannot run
	// while we hold it.
	_, _ = h.IOS.goios.Session(id)
	acquired := make(chan struct{})
	held := make(chan struct{})
	go func() {
		_ = h.IOS.goios.WithDeviceInitializer(id, func() error {
			close(acquired)
			<-held
			return nil
		})
	}()
	<-acquired
	g.unlock = func() { close(held) }
	time.AfterFunc(runwdaReadySettle, g.release)
	return g
}

// readyTwice tolerates one slow answer before a runner this daemon owns
// is called unready, because unready means a restart.
func (h *AutomationHost) readyTwice(id string, pid int) bool {
	if h.ready(id, pid) {
		return true
	}
	time.Sleep(500 * time.Millisecond)
	return h.ready(id, pid)
}

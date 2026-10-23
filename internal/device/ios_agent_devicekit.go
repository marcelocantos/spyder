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
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	goios_ios "github.com/danielpaulus/go-ios/ios"
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
)

var hardwareUDID = regexp.MustCompile(`^([0-9A-Fa-f]{8}-[0-9A-Fa-f]{16}|[0-9A-Fa-f]{40})$`)

// DeviceKitAgent is the DeviceKit backend configuration. CLI is the
// go-ios the daemon already uses for tunnels and 🎯T167 (the bundled
// libexec/spyder/ios, or SPYDER_IOS_TUNNEL_BINARY).
type DeviceKitAgent struct {
	CLI     string
	all     bool
	devices map[string]bool
	err     error
}

// DeviceKitAgentFromEnv reads SPYDER_IOS_AGENT_BACKEND and
// SPYDER_IOS_AGENT_DEVICES. Unset or "alertrunner" keeps the AlertRunner
// default and returns nil. "devicekit" needs an explicit device scope:
// a comma-separated list of hardware UDIDs, or "*" for every tethered
// device. Anything else is returned with Err set; supervision then
// fails closed instead of silently picking a backend.
func DeviceKitAgentFromEnv(iosBin string) *DeviceKitAgent {
	return deviceKitAgentFrom(os.Getenv(EnvIOSAgentBackend), os.Getenv(EnvIOSAgentDevices), iosBin)
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

// launchDeviceKit starts the resident DeviceKit runner. It never adopts
// a runner this process did not start: a live foreign runner (another
// harness, a previous daemon) is reported and left alone.
func (h *AutomationHost) launchDeviceKit(id string) (int, error) {
	if _, err := h.DeviceKit.cli(); err != nil {
		return 0, err
	}
	pid, err := h.appPID(id, DeviceKitRunnerBundle)
	switch {
	case err == nil && pid > 0:
		owned, ok := h.heldPID(id)
		if !ok || owned != pid {
			return 0, foreignRunner(id, pid)
		}
		if h.ready(id, pid) {
			return pid, nil
		}
		// Our own runner stopped answering. Replace it.
		h.stopHold(id)
		ctx, cancel := context.WithTimeout(context.Background(), agentProbeBudget)
		_ = h.IOS.TerminateAppContext(ctx, id, DeviceKitRunnerBundle)
		cancel()
		h.waitRunnerGone(id, DeviceKitRunnerBundle)
	case err != nil && !appAbsent(err):
		return 0, fmt.Errorf("DeviceKit runner lookup on %s: %w", id, err)
	}
	h.stopHold(id)
	pid, err = h.startHold(id)
	if err != nil {
		return 0, err
	}
	h.mu.Lock()
	h.probeNote[id] = fmt.Sprintf("launched DeviceKit runner pid %d", pid)
	h.mu.Unlock()
	return pid, nil
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
// daemon's hold ends it, and agent_idle does not.
func (h *AutomationHost) runDeviceKit(ctx context.Context, id string, out io.Writer) error {
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
	err = cmd.Run()
	if ctx.Err() != nil {
		return nil
	}
	text := ""
	if tb, ok := out.(*tailBuf); ok {
		text = tb.string()
	}
	if marker := runwdaFailure(text); marker != "" {
		return fmt.Errorf("DeviceKit runwda on %s: %s", id, marker)
	}
	if err == nil {
		return fmt.Errorf("DeviceKit runwda on %s exited: %s", id, lastLine(text))
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

// foreignRunner names a DeviceKit runner this daemon does not own, with
// the host runwda driving it when one is visible, so the owner knows
// what to stop. Spyder never kills or adopts it.
func foreignRunner(id string, pid int) error {
	msg := fmt.Sprintf("DeviceKit runner pid %d on %s was not started by this daemon; leaving it alone", pid, id)
	if host, ok := scanRunwda(id); ok && host.Binary != "" {
		msg += fmt.Sprintf(" (host: %s runwda; stop that harness or daemon, then agent_ensure)", host.Binary)
	}
	return errors.New(msg)
}

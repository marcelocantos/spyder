// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"time"

	ios "github.com/danielpaulus/go-ios/ios"
)

const (
	DeviceKitRunnerBundle = "com.mobilenext.devicekit-iosUITests.xctrunner"
	deviceKitPort         = 12004
	deviceKitProbeBudget  = 2 * time.Second
	deviceKitMaxResponse  = 64 * 1024
)

// DeviceKitAgent selects the already-installed game-input agent. Selection is
// daemon configuration, never a command or executable supplied by a test client.
type DeviceKitAgent struct {
	CLI, SHA256 string
	Devices     map[string]bool
	err         error
}

// DeviceKitAgentFromEnvironment keeps the existing AlertRunner default. An
// invalid opt-in stays invalid rather than silently starting another agent.
func DeviceKitAgentFromEnvironment() *DeviceKitAgent {
	backend := os.Getenv("SPYDER_IOS_AGENT_BACKEND")
	if backend == "" {
		return nil
	}
	p := &DeviceKitAgent{CLI: os.Getenv("SPYDER_IOS_AGENT_CLI"), SHA256: os.Getenv("SPYDER_IOS_AGENT_CLI_SHA256"), Devices: map[string]bool{}}
	if backend != "devicekit" {
		p.err = errors.New("unknown configured iOS agent backend")
		return p
	}
	udid := regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{16}$`)
	for _, id := range strings.Split(os.Getenv("SPYDER_IOS_AGENT_DEVICES"), ",") {
		if !udid.MatchString(id) {
			p.err = errors.New("explicit hardware UDID automation scope required")
			return p
		}
		p.Devices[id] = true
	}
	p.err = p.verify()
	return p
}

func (p *DeviceKitAgent) verify() error {
	if p.err != nil {
		return p.err
	}
	if !filepath.IsAbs(p.CLI) {
		return errors.New("absolute pinned DeviceKit launcher required")
	}
	info, err := os.Lstat(p.CLI)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return errors.New("DeviceKit launcher must be a regular executable")
	}
	want, err := hex.DecodeString(p.SHA256)
	if err != nil || len(want) != sha256.Size {
		return errors.New("DeviceKit launcher SHA256 required")
	}
	body, err := os.ReadFile(p.CLI)
	if err != nil {
		return err
	}
	got := sha256.Sum256(body)
	if hex.EncodeToString(got[:]) != strings.ToLower(p.SHA256) {
		return errors.New("DeviceKit launcher SHA256 mismatch")
	}
	return nil
}

func (h *AutomationHost) RunnerBundle() string {
	if h.DeviceKit != nil {
		return DeviceKitRunnerBundle
	}
	return alertRunnerBundle
}

func (h *AutomationHost) hostBundle() string {
	if h.DeviceKit != nil {
		return "com.mobilenext.devicekit-ios"
	}
	return alertHostBundle
}

func (h *AutomationHost) launchDeviceKit(id string) (int, error) {
	if !h.DeviceKit.Devices[id] {
		return 0, errors.New("device is outside the configured automation scope")
	}
	if err := h.DeviceKit.verify(); err != nil {
		return 0, err
	}
	pid, err := h.appPID(id, DeviceKitRunnerBundle)
	if err == nil && pid > 0 {
		if owned, ok := h.heldPID(id); ok && owned == pid {
			if h.deviceKitReady(id, pid) {
				return pid, nil
			}
			return 0, errors.New("owned DeviceKit endpoint unavailable; session retained without relaunch")
		}
		return 0, errors.New("DeviceKit runner is already owned by another host session")
	}
	if !appAbsent(err) {
		return 0, fmt.Errorf("DeviceKit native absence unavailable: %w", err)
	}
	h.stopHold(id)
	return h.startHold(id)
}

func (h *AutomationHost) runDeviceKit(ctx context.Context, id string) error {
	if err := h.DeviceKit.verify(); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "spyder-devicekit-agent-")
	if err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(dir, "launcher.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	// No per-client or hourly lifetime: the daemon's owned context ends this
	// resident test. Client idle releases ownership without cancelling it.
	cmd := exec.CommandContext(ctx, h.DeviceKit.CLI, "runwda", "--udid="+id,
		"--bundleid="+DeviceKitRunnerBundle, "--testrunnerbundleid="+DeviceKitRunnerBundle,
		"--xctestconfig=devicekit-iosUITests.xctest", "--log-output="+filepath.Join(dir, "xctest.log"))
	cmd.Stdout, cmd.Stderr = log, log
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 8 * time.Second
	return cmd.Run()
}

// deviceKitReady reads the installed agent over this exact USB device. A live
// runner PID alone also exists while Apple is waiting for the owner's passcode.
func (h *AutomationHost) deviceKitReady(id string, pid int) bool {
	dev, err := h.IOS.goios.Session(id)
	if err != nil {
		return false
	}
	network, address := ios.GetSocketTypeAndAddress(ios.GetUsbmuxdSocket())
	conn, err := net.DialTimeout(network, address, deviceKitProbeBudget)
	if err != nil {
		return false
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(deviceKitProbeBudget)); err != nil {
		return false
	}
	mux := ios.NewUsbMuxConnection(ios.NewDeviceConnectionWithConn(conn))
	if err := mux.Connect(dev.DeviceID, deviceKitPort); err != nil {
		return false
	}
	body := `{"jsonrpc":"2.0","id":"spyder-agent-health","method":"device.apps.state","params":{"bundleId":"` + DeviceKitRunnerBundle + `"}}`
	if _, err := fmt.Fprintf(conn, "POST /rpc HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body); err != nil {
		return false
	}
	return deviceKitStateReady(conn, pid)
}

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
	return value.ID == "spyder-agent-health" && value.Version == "2.0" && len(value.Error) == 0 && value.Result.Bundle == DeviceKitRunnerBundle && value.Result.PID != nil &&
		(value.Result.State == "runningBackground" && *value.Result.PID == 0 || value.Result.State == "runningForeground" && *value.Result.PID == pid)
}

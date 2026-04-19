// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// SimulatorAdapter talks to iOS simulators via xcrun simctl. It satisfies
// the Adapter interface. Operations not applicable to simulators (State,
// KeepAwake, etc.) are stubs that return clear errors.
type SimulatorAdapter struct{}

// NewSimulatorAdapter returns a new iOS simulator adapter.
func NewSimulatorAdapter() *SimulatorAdapter { return &SimulatorAdapter{} }

func (a *SimulatorAdapter) List() ([]Info, error) {
	return nil, errors.New("simctl: List() is not supported via SimulatorAdapter — use IOSAdapter.List for physical devices")
}

func (a *SimulatorAdapter) State(id string) (State, error) {
	return State{}, errors.New("simctl: State() not implemented for simulators")
}

func (a *SimulatorAdapter) LaunchKeepAwake(id string) error {
	return errors.New("simctl: KeepAwake is not applicable to simulators")
}

func (a *SimulatorAdapter) Screenshot(id string) ([]byte, error) {
	tmp, err := os.CreateTemp("", "spyder-simshot-*.png")
	if err != nil {
		return nil, fmt.Errorf("simctl screenshot: create temp: %w", err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	out, err := exec.Command("xcrun", "simctl", "io", id, "screenshot", tmp.Name()).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("simctl screenshot: %w\n%s", err, truncate(string(out), 200))
	}
	return os.ReadFile(tmp.Name())
}

func (a *SimulatorAdapter) ListApps(id string) ([]AppInfo, error) {
	return nil, errors.New("simctl: ListApps() not yet implemented for simulators")
}

func (a *SimulatorAdapter) LaunchApp(id, bundleID string) error {
	out, err := exec.Command("xcrun", "simctl", "launch", id, bundleID).CombinedOutput()
	if err != nil {
		return fmt.Errorf("simctl launch: %w\n%s", err, truncate(string(out), 200))
	}
	return nil
}

func (a *SimulatorAdapter) TerminateApp(id, bundleID string) error {
	out, err := exec.Command("xcrun", "simctl", "terminate", id, bundleID).CombinedOutput()
	if err != nil {
		return fmt.Errorf("simctl terminate: %w\n%s", err, truncate(string(out), 200))
	}
	return nil
}

// StartRecording starts `xcrun simctl io <id> recordVideo <dest>` in the
// background. The process runs until StopRecording sends it SIGINT.
func (a *SimulatorAdapter) StartRecording(id, dest string) (func() error, int, error) {
	if id == "" {
		return nil, 0, errors.New("simctl: device identifier is empty")
	}
	if dest == "" {
		return nil, 0, errors.New("simctl: dest path is required")
	}

	// Remove the file if it already exists so simctl doesn't complain.
	_ = os.Remove(dest)

	cmd := exec.Command("xcrun", "simctl", "io", id, "recordVideo", dest)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, 0, fmt.Errorf("simctl recordVideo: %w", err)
	}

	pid := cmd.Process.Pid
	stopFn := func() error {
		// SIGINT causes simctl to flush and close the mp4 cleanly.
		if err := cmd.Process.Signal(syscall.SIGINT); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		// Wait up to 10 s for clean shutdown so the caller can rely on the file.
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		case <-done:
		}
		return nil
	}

	return stopFn, pid, nil
}

// StopRecording sends SIGINT to the simctl process and waits for it
// to flush and exit. pid is the value returned by StartRecording.
func (a *SimulatorAdapter) StopRecording(id string, pid int) error {
	if pid <= 0 {
		return fmt.Errorf("simctl StopRecording: invalid pid %d", pid)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("simctl StopRecording: find process %d: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGINT); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("simctl StopRecording: signal %d: %w", pid, err)
	}
	return nil
}

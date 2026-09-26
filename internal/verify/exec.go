// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// StepRequest is what a command/script executor receives.
type StepRequest struct {
	Step    Step
	Cwd     string
	Env     map[string]string
	Timeout time.Duration
	Emit    func(string)
}

// ExecResult is a process-style outcome.
type ExecResult struct {
	Code     int
	Output   string
	TimedOut bool
}

// StepRunner runs a shell or spyder_script step.
type StepRunner func(ctx context.Context, req StepRequest) ExecResult

// DefaultShell runs command or argv on the host. Used for `shell` steps.
func DefaultShell(ctx context.Context, req StepRequest) ExecResult {
	var cmd *exec.Cmd
	if len(req.Step.Argv) > 0 {
		cmd = exec.Command(req.Step.Argv[0], req.Step.Argv[1:]...)
	} else {
		cmd = exec.Command("/bin/sh", "-c", req.Step.Command)
	}
	cmd.Dir = req.Cwd
	cmd.Env = os.Environ()
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ExecResult{Code: 1, Output: err.Error()}
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return ExecResult{Code: 1, Output: err.Error()}
	}

	var stored []byte
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		r := bufio.NewReader(stdout)
		for {
			line, rerr := r.ReadString('\n')
			if line != "" {
				stored = append(stored, line...)
				if req.Emit != nil {
					req.Emit(trimNL(line))
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	var waitErr error
	timedOut := false
	select {
	case waitErr = <-waitDone:
	case <-ctx.Done():
		timedOut = true
		killProcessGroup(cmd)
		waitErr = <-waitDone
	}
	<-readDone
	out := string(stored)
	if timedOut {
		return ExecResult{Code: 124, Output: out + "\n[timed out]\n", TimedOut: true}
	}
	if waitErr == nil {
		return ExecResult{Code: 0, Output: out}
	}
	if ee, ok := waitErr.(*exec.ExitError); ok {
		return ExecResult{Code: ee.ExitCode(), Output: out}
	}
	return ExecResult{Code: 1, Output: out + waitErr.Error()}
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		time.Sleep(50 * time.Millisecond)
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return
	}
	_ = cmd.Process.Kill()
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

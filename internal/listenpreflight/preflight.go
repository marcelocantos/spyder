// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package listenpreflight detects TCP listeners on the spyder daemon port
// (IPv4 and IPv6). ENT-008 / 🎯T132.8: CLI auto-start must not spawn a
// second `spyder serve` when anything already owns :3030.
package listenpreflight

import (
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// DaemonPort is the default HTTP port for the shipped brew service and the
// loopback default in main.defaultAddr.
const DaemonPort = 3030

// ProbeFunc reports whether any process is listening on TCP port.
type ProbeFunc func(port int) (bool, error)

// Hook, when non-nil, replaces the production probe (tests only).
var Hook ProbeFunc

// AnyTCPListener reports whether any TCP socket is in LISTEN on port,
// on either IPv4 or IPv6.
func AnyTCPListener(port int) (bool, error) {
	if Hook != nil {
		return Hook(port)
	}
	if inUse, err := probeWithLsof(port); err == nil || inUse {
		return inUse, err
	}
	return probeWithBind(port)
}

func probeWithLsof(port int) (bool, error) {
	lsof, err := exec.LookPath("lsof")
	if err != nil {
		return false, err
	}
	spec := fmt.Sprintf("-iTCP:%d", port)
	out, err := exec.Command(lsof, "-nP", spec, "-sTCP:LISTEN", "-t").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && len(out) == 0 {
			return false, nil
		}
		return false, fmt.Errorf("lsof %s: %w", spec, err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

func probeWithBind(port int) (bool, error) {
	portStr := strconv.Itoa(port)
	candidates := []string{
		":" + portStr,
		"127.0.0.1:" + portStr,
		"[::]:" + portStr,
	}
	var bindErr error
	for _, addr := range candidates {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			if isAddrInUse(err) {
				return true, nil
			}
			bindErr = err
			continue
		}
		ln.Close()
	}
	if bindErr != nil {
		return false, bindErr
	}
	return false, nil
}

func isAddrInUse(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		var errno syscall.Errno
		if errors.As(opErr.Err, &errno) {
			return errno == syscall.EADDRINUSE
		}
	}
	return strings.Contains(strings.ToLower(err.Error()), "address already in use")
}

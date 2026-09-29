// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielpaulus/go-ios/ios/accessibility"
	"github.com/danielpaulus/go-ios/ios/instruments"
	"github.com/danielpaulus/go-ios/ios/testmanagerd"
)

// 🎯T154 — iOS system alerts. Reading uses the accessibility inspector
// service (AXAuditDaemon over DTX, what Xcode's Accessibility Inspector
// uses): moving its focus reads each element's spoken description and owning
// process, with no test runner. Its "Activate" action cannot press
// SpringBoard's buttons (it works only on debuggable processes), so tapping
// runs Spyder's alert runner, a one-test XCUITest bundle (ios/AlertRunner),
// in-process through go-ios testmanagerd.

const (
	// axWalkLimit bounds the focus walk. A system alert is modal, so its
	// focus cycle closes within a handful of elements; a screen that does
	// not close within the limit is not an alert.
	axWalkLimit = 30
	// axMoveTimeout bounds one focus step.
	axMoveTimeout = 3 * time.Second
	// axSessionTimeout bounds a whole accessibility session. go-ios waits
	// for some device events without a timeout, so the session runs under
	// this watchdog, which closes the connection when it expires.
	axSessionTimeout = 45 * time.Second
	// axSettle is how long a tapped alert gets to disappear.
	axSettle = 1500 * time.Millisecond
)

// The alert runner (ios/AlertRunner, installed with `make alert-runner`).
const (
	alertRunnerBundle  = "com.marcelocantos.spyder.AlertRunner.xctrunner"
	alertHostBundle    = "com.marcelocantos.spyder.AlertHost"
	alertRunnerConfig  = "AlertRunner.xctest"
	alertRunnerTest    = "AlertRunner.SystemAlertTests/testTapSystemAlertButton"
	alertRunnerTimeout = 120 * time.Second
	// alertButtonWait is how long the runner looks for the button; the
	// alert was already read, so it only covers a slow accessibility tree.
	alertButtonWait = 10
)

// iosSystemProcesses own system alerts, by executable name.
var iosSystemProcesses = []string{"SpringBoard"}

// axMu keeps one accessibility session per device at a time: two sessions
// would move the same inspector focus.
var axMu sync.Map // udid -> *sync.Mutex

type axQuiet struct{}

func (axQuiet) HostAppStateChanged(accessibility.Notification)               {}
func (axQuiet) HostInspectorNotificationReceived(accessibility.Notification) {}

// SystemAlert reports the system alert showing on an iOS device, if any.
func (a *IOSAdapter) SystemAlert(id string) (SystemAlert, error) {
	var alert SystemAlert
	err := a.withAccessibility(id, func(ax *accessibility.ControlInterface, owners map[int]string) error {
		elems, closed, err := axWalk(ax)
		if err != nil {
			return err
		}
		alert = classifyAlert(elems, closed, owners)
		return nil
	})
	return alert, err
}

// TapSystemAlertButton presses the named button of the system alert that is
// showing, then confirms that alert went away (a different alert may follow).
func (a *IOSAdapter) TapSystemAlertButton(id, button string) (SystemAlert, SystemAlert, error) {
	var before SystemAlert
	err := a.withAccessibility(id, func(ax *accessibility.ControlInterface, owners map[int]string) error {
		elems, closed, err := axWalk(ax)
		if err != nil {
			return err
		}
		before = classifyAlert(elems, closed, owners)
		if !before.Showing {
			return ErrNoSystemAlert
		}
		target, ok := findButton(elems, owners, button)
		if !ok {
			return fmt.Errorf("system alert %q has no button %q (buttons: %s)", before.Title, button, strings.Join(before.Buttons, ", "))
		}
		button = target.text() // the exact label, e.g. with iOS's typographic apostrophe
		return nil
	})
	if err != nil {
		return before, SystemAlert{}, err
	}
	if err := a.runAlertRunner(id, button); err != nil {
		return before, SystemAlert{}, err
	}
	time.Sleep(axSettle)
	after, err := a.SystemAlert(id)
	if err != nil {
		return before, after, fmt.Errorf("tapped %q, but reading the screen afterwards failed: %w", button, err)
	}
	if after.Showing && after.Title == before.Title && slices.Equal(after.Buttons, before.Buttons) {
		return before, after, fmt.Errorf("tapped %q, but the alert %q is still showing", button, before.Title)
	}
	return before, after, nil
}

// runAlertRunner taps a SpringBoard button through the installed alert
// runner. iOS must allow UI automation: the first run after a reboot asks the
// owner to authorise it on the device (Touch ID or passcode) unless
// Settings > Developer > Enable UI Automation is on.
func (a *IOSAdapter) runAlertRunner(id, button string) error {
	apps, err := a.ListApps(id)
	if err != nil {
		return fmt.Errorf("list apps on %s: %w", id, err)
	}
	installed := map[string]bool{}
	for _, app := range apps {
		installed[app.BundleID] = true
	}
	if !installed[alertRunnerBundle] || !installed[alertHostBundle] {
		return fmt.Errorf("the alert runner is not installed on %s: run `make alert-runner DEVICE=%s TEAM=<team id>` in the spyder repo", id, id)
	}
	dev, err := a.goios.Session(id)
	if err != nil {
		return fmt.Errorf("system alert tap on %s: %w", id, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), alertRunnerTimeout)
	defer cancel()
	listener := testmanagerd.NewTestListener(io.Discard, io.Discard, os.TempDir())
	suites, err := testmanagerd.RunTestWithConfig(ctx, testmanagerd.TestConfig{
		BundleId:           alertHostBundle,
		TestRunnerBundleId: alertRunnerBundle,
		XctestConfigName:   alertRunnerConfig,
		Env:                map[string]any{"SPYDER_ALERT_BUTTON": button, "SPYDER_ALERT_WAIT_SEC": strconv.Itoa(alertButtonWait)},
		TestsToRun:         []string{alertRunnerTest},
		Device:             dev,
		Listener:           listener,
	})
	if err != nil {
		if strings.Contains(err.Error(), "enabling automation mode") {
			return fmt.Errorf("iOS did not allow UI automation on %s: authorise it on the device (Touch ID or passcode prompt), or turn on Settings > Developer > Enable UI Automation, then retry", id)
		}
		return fmt.Errorf("alert runner on %s: %w", id, err)
	}
	for _, suite := range suites {
		for _, tc := range suite.TestCases {
			if tc.Status != testmanagerd.StatusPassed {
				return fmt.Errorf("alert runner could not tap %q: %s %s", button, tc.Status, tc.Err.Message)
			}
			return nil
		}
	}
	return fmt.Errorf("alert runner on %s reported no result", id)
}

// withAccessibility opens an accessibility inspector session on the device,
// runs fn, and turns the inspector off again, all under axSessionTimeout.
// owners maps the pids of system processes to their names.
func (a *IOSAdapter) withAccessibility(id string, fn func(*accessibility.ControlInterface, map[int]string) error) error {
	if id == "" {
		return errors.New("device identifier is empty")
	}
	lock, _ := axMu.LoadOrStore(id, &sync.Mutex{})
	lock.(*sync.Mutex).Lock()
	defer lock.(*sync.Mutex).Unlock()

	owners, err := a.systemProcesses(id)
	if err != nil {
		return err
	}
	dev, err := a.goios.Session(id)
	if err != nil {
		return fmt.Errorf("system alert on %s: %w", id, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var conn *accessibility.ControlInterface
	done := make(chan error, 1)
	go func() {
		ax, err := accessibility.New(ctx, dev, axQuiet{})
		if err != nil {
			done <- fmt.Errorf("accessibility service on %s: %w", id, wrapMissingDDI(err))
			return
		}
		mu.Lock()
		conn = ax
		mu.Unlock()
		ax.SwitchToDevice()
		ax.EnableSelectionMode()
		err = fn(ax, owners)
		ax.TurnOff() // hide the inspector's focus highlight
		done <- err
	}()
	select {
	case err = <-done:
	case <-time.After(axSessionTimeout):
		err = fmt.Errorf("accessibility session on %s timed out after %s", id, axSessionTimeout)
	}
	mu.Lock()
	if conn != nil {
		_ = conn.Close()
	}
	mu.Unlock()
	return err
}

// axWalk steps the inspector focus forward until the cycle returns to its
// first element (closed) or axWalkLimit is reached.
func axWalk(ax *accessibility.ControlInterface) ([]alertElement, bool, error) {
	var elems []alertElement
	seen := map[string]bool{}
	for range axWalkLimit {
		ctx, cancel := context.WithTimeout(context.Background(), axMoveTimeout)
		ax.Move(accessibility.DirectionNext)
		el, err := ax.AwaitElementChanged(ctx)
		cancel()
		if err != nil {
			// Focus stops moving on screens with no accessible elements,
			// such as a game's full-screen view: nothing modal is showing.
			return elems, false, nil
		}
		if seen[el.PlatformElementValue] {
			return elems, true, nil
		}
		seen[el.PlatformElementValue] = true
		label, _ := ax.QueryLabelValue(context.Background(), el.PlatformElementValue)
		elems = append(elems, alertElement{
			Description: el.SpokenDescription,
			Label:       label,
			PID:         elementPID(el.PlatformElementValue),
			Handle:      el.PlatformElementValue,
		})
	}
	return elems, false, nil
}

// elementPID reads the owning process from an element handle: its first
// four bytes are the pid, little-endian.
func elementPID(handle string) int {
	raw, err := base64.StdEncoding.DecodeString(handle)
	if err != nil || len(raw) < 4 {
		return -1
	}
	return int(binary.LittleEndian.Uint32(raw[:4]))
}

// systemProcesses maps the pids of the processes that own system alerts to
// their names.
func (a *IOSAdapter) systemProcesses(id string) (map[int]string, error) {
	owners := map[int]string{}
	match := func(pid int, path string) {
		base := path[strings.LastIndex(path, "/")+1:]
		if slices.Contains(iosSystemProcesses, base) {
			owners[pid] = base
		}
	}
	_, major, err := a.goios.SessionWithVersion(id)
	if err != nil {
		return nil, fmt.Errorf("system alert on %s: %w", id, err)
	}
	if major != 0 && major < 17 {
		dev, err := a.goios.Session(id)
		if err != nil {
			return nil, err
		}
		di, err := instruments.NewDeviceInfoService(dev)
		if err != nil {
			a.goios.Invalidate(id)
			return nil, wrapMissingDDI(fmt.Errorf("list processes on %s: %w", id, err))
		}
		defer di.Close()
		procs, err := di.ProcessList()
		if err != nil {
			return nil, fmt.Errorf("list processes on %s: %w", id, err)
		}
		for _, p := range procs {
			match(int(p.Pid), p.Name)
		}
	} else {
		conn, release, err := a.asPool.Acquire(id)
		if err != nil {
			a.goios.Invalidate(id)
			return nil, fmt.Errorf("appservice on %s: %w", id, err)
		}
		defer release()
		procs, err := conn.ListProcesses()
		if err != nil {
			a.asPool.Invalidate(id)
			return nil, fmt.Errorf("list processes on %s: %w", id, err)
		}
		for _, p := range procs {
			match(p.Pid, p.Path)
		}
	}
	if len(owners) == 0 {
		return nil, fmt.Errorf("no %s process found on %s", strings.Join(iosSystemProcesses, " or "), id)
	}
	return owners, nil
}

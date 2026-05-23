// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package wedge

import (
	"fmt"
	"os/exec"
	"regexp"

	goios_ios "github.com/danielpaulus/go-ios/ios"
)

// supportsIPhoneOSRE matches an ioreg property line of the shape
//
//	"SupportsIPhoneOS" = Yes
//
// emitted once per iOS-shaped USB device under -p IOUSB. The kernel
// surfaces this property only when a connected USB device's class
// descriptors declare an iOS protocol stack, so counting matches
// gives the host's ground-truth count of attached iOS devices.
var supportsIPhoneOSRE = regexp.MustCompile(`"SupportsIPhoneOS"\s*=\s*Yes`)

// USBAttachedIOSCount returns the number of iOS devices the kernel
// sees on USB. The ground truth that usbmuxd's third-party view
// should match — when usbmux's count is lower, a wedge has occurred
// (usbmuxd internally interpreted a USB data event as a disconnect
// while the kernel kept the device registered).
//
// Exported so `spyder doctor` can include the IOUSB count alongside
// the usbmux/devicectl counts in its diagnostic report.
func USBAttachedIOSCount() (int, error) {
	out, err := exec.Command("ioreg", "-p", "IOUSB", "-w0", "-c", "IOUSBHostDevice").Output()
	if err != nil {
		return 0, fmt.Errorf("ioreg: %w", err)
	}
	return parseUSBIOSCount(out), nil
}

// parseUSBIOSCount counts iOS-shaped devices in ioreg output. Pure
// function — separated from the exec call so tests can feed canned
// output without shelling out.
func parseUSBIOSCount(out []byte) int {
	return len(supportsIPhoneOSRE.FindAll(out, -1))
}

// usbmuxAttachedCount returns the number of iOS devices usbmuxd's
// third-party API currently reports, via go-ios's in-process call.
func usbmuxAttachedCount() (int, error) {
	list, err := goios_ios.ListDevices()
	if err != nil {
		return 0, fmt.Errorf("goios.ListDevices: %w", err)
	}
	return len(list.DeviceList), nil
}

// IsWedged compares IOUSB ground truth to usbmuxd's view. Returns
// wedged=true when the kernel sees more iOS devices than usbmuxd
// does (the diagnostic signature of the third-party-table desync).
// Both counts are returned for callers that want to log them.
func IsWedged() (wedged bool, iousb, usbmux int, err error) {
	iousb, err = USBAttachedIOSCount()
	if err != nil {
		return false, 0, 0, err
	}
	usbmux, err = usbmuxAttachedCount()
	if err != nil {
		return false, iousb, 0, err
	}
	return iousb > usbmux, iousb, usbmux, nil
}

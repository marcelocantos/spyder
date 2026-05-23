// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package wedge

import "testing"

// TestParseUSBIOSCount feeds canned ioreg output through the parser
// and checks that only "SupportsIPhoneOS = Yes" entries are counted —
// other USB devices (which also appear under -p IOUSB but don't
// declare an iOS protocol stack) must not inflate the count.
func TestParseUSBIOSCount(t *testing.T) {
	sample := []byte(`
+-o Root  <class IORegistryEntry, id 0x100000100>
  +-o AppleT8132USBXHCI  <class AppleT8132USBXHCI>
  | +-o iPhone@00143130  <class IOUSBHostDevice, id 0x1000166e1>
  |     "kUSBProductString" = "iPhone"
  |     "USB Product Name" = "iPhone"
  |     "SupportsIPhoneOS" = Yes
  | +-o iPad@00143110  <class IOUSBHostDevice, id 0x100016701>
  |     "kUSBProductString" = "iPad"
  |     "USB Product Name" = "iPad"
  |     "SupportsIPhoneOS" = Yes
  | +-o USB-Hub@00100000  <class IOUSBHostDevice>
  |     "USB Product Name" = "USB 3.1 Hub"
  | +-o WebCam@00200000  <class IOUSBHostDevice>
  |     "USB Product Name" = "Studio Display Webcam"
  |     "SupportsIPhoneOS" = No
`)
	if got := parseUSBIOSCount(sample); got != 2 {
		t.Errorf("parseUSBIOSCount = %d; want 2", got)
	}
}

func TestParseUSBIOSCount_NoDevices(t *testing.T) {
	sample := []byte(`+-o Root  <class IORegistryEntry>
  +-o Mouse  "SupportsIPhoneOS" = No
`)
	if got := parseUSBIOSCount(sample); got != 0 {
		t.Errorf("parseUSBIOSCount = %d; want 0", got)
	}
}

func TestParseUSBIOSCount_WhitespaceVariants(t *testing.T) {
	// ioreg output uses various whitespace between key and value —
	// the regex should be tolerant.
	for _, sample := range [][]byte{
		[]byte(`"SupportsIPhoneOS" = Yes`),
		[]byte(`"SupportsIPhoneOS"= Yes`),
		[]byte(`"SupportsIPhoneOS"  =  Yes`),
		[]byte("\"SupportsIPhoneOS\"\t=\tYes"),
	} {
		if got := parseUSBIOSCount(sample); got != 1 {
			t.Errorf("parseUSBIOSCount(%q) = %d; want 1", sample, got)
		}
	}
}

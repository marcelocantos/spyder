// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package usbspeed reports the IOKit negotiated USB Device Speed of
// USB-attached physical phones and tablets, and remembers the highest
// speed observed per serial (🎯T131, 🎯T131.1).
package usbspeed

import (
	"bytes"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// kUSBDeviceSpeed values from IOKit/usb/USB.h. Call sites use these
// names rather than raw integers.
const (
	USBDeviceSpeedLow       = 0 // 1.5 Mb/s
	USBDeviceSpeedFull      = 1 // 12 Mb/s
	USBDeviceSpeedHigh      = 2 // 480 Mb/s
	USBDeviceSpeedSuper     = 3 // 5 Gb/s
	USBDeviceSpeedSuperPlus = 4 // 10 Gb/s
)

var usbDeviceSpeedLabel = map[int]string{
	USBDeviceSpeedLow:       "1.5 Mb/s",
	USBDeviceSpeedFull:      "12 Mb/s",
	USBDeviceSpeedHigh:      "480 Mb/s",
	USBDeviceSpeedSuper:     "5 Gb/s",
	USBDeviceSpeedSuperPlus: "10 Gb/s",
}

var (
	reUSBSerial  = regexp.MustCompile(`"USB Serial Number"\s*=\s*"([^"]*)"`)
	reDeviceSpd  = regexp.MustCompile(`"Device Speed"\s*=\s*(\d+)`)
	reDevClass   = regexp.MustCompile(`"bDeviceClass"\s*=\s*(\d+)`)
	reUSBProduct = regexp.MustCompile(`"USB Product Name"\s*=\s*"([^"]*)"`)
)

const usbHubClass = 9

// Label maps a kUSBDeviceSpeed value to the string devices() reports.
// Unknown values return false so callers omit the field rather than
// inventing a speed.
func Label(speed int) (string, bool) {
	s, ok := usbDeviceSpeedLabel[speed]
	return s, ok
}

// ParseLabel maps a devices() speed string back to kUSBDeviceSpeed.
func ParseLabel(label string) (int, bool) {
	for speed, s := range usbDeviceSpeedLabel {
		if s == label {
			return speed, true
		}
	}
	return 0, false
}

// CanonicalID normalizes a USB serial / iOS UDID for census join and
// ceiling keys: dashes stripped, uppercased. iOS uuid equals the USB
// Serial Number with the 8-16 dash restored or stripped.
func CanonicalID(id string) string {
	return strings.ToUpper(strings.ReplaceAll(id, "-", ""))
}

// ReadCensus runs one `ioreg -p IOUSB` snapshot of IOUSBHostDevice
// nodes. Callers parse the bytes; a failure here is degrade-to-omit.
func ReadCensus() ([]byte, error) {
	out, err := exec.Command("ioreg", "-p", "IOUSB", "-w0", "-c", "IOUSBHostDevice").Output()
	if err != nil {
		return nil, fmt.Errorf("ioreg: %w", err)
	}
	return out, nil
}

// ParseCensus returns serial → max kUSBDeviceSpeed for IOUSBHostDevice
// nodes that look like attached peripherals. Hubs and billboards are
// ignored. Dual USB2/USB3 companion nodes for the same serial keep the
// highest Device Speed.
func ParseCensus(out []byte) map[string]int {
	speeds := map[string]int{}
	for _, block := range hostDeviceBlocks(out) {
		serial, speed, ok := parseHostDevice(block)
		if !ok {
			continue
		}
		key := CanonicalID(serial)
		if prev, exists := speeds[key]; !exists || speed > prev {
			speeds[key] = speed
		}
	}
	return speeds
}

// SpeedForID looks up a device uuid / serial in a parsed census.
func SpeedForID(id string, census map[string]int) (int, bool) {
	if id == "" || census == nil {
		return 0, false
	}
	speed, ok := census[CanonicalID(id)]
	return speed, ok
}

func parseHostDevice(block []byte) (serial string, speed int, ok bool) {
	if ignoredHostDevice(block) {
		return "", 0, false
	}
	sm := reUSBSerial.FindSubmatch(block)
	if sm == nil || len(sm[1]) == 0 {
		return "", 0, false
	}
	dm := reDeviceSpd.FindSubmatch(block)
	if dm == nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(string(dm[1]))
	if err != nil {
		return "", 0, false
	}
	return string(sm[1]), n, true
}

func ignoredHostDevice(block []byte) bool {
	if cm := reDevClass.FindSubmatch(block); cm != nil {
		if n, err := strconv.Atoi(string(cm[1])); err == nil && n == usbHubClass {
			return true
		}
	}
	if pm := reUSBProduct.FindSubmatch(block); pm != nil {
		p := strings.ToLower(string(pm[1]))
		if strings.Contains(p, "hub") || strings.Contains(p, "billboard") {
			return true
		}
	}
	return false
}

func hostDeviceBlocks(out []byte) [][]byte {
	var blocks [][]byte
	s := out
	marker := []byte("<class IOUSBHostDevice")
	for {
		i := bytes.Index(s, marker)
		if i < 0 {
			break
		}
		s = s[i+len(marker):]
		j := bytes.IndexByte(s, '{')
		if j < 0 {
			break
		}
		block, rest, found := matchBrace(s[j:])
		if !found {
			break
		}
		blocks = append(blocks, block)
		s = rest
	}
	return blocks
}

func matchBrace(s []byte) (block, rest []byte, ok bool) {
	if len(s) == 0 || s[0] != '{' {
		return nil, s, false
	}
	depth := 0
	for i, c := range s {
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[:i+1], s[i+1:], true
			}
		}
	}
	return nil, s, false
}

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// IOSUSBNetworkHost returns the Mac-side IPv4 address of the USB network
// (NCM) link to the physical iOS device udid, e.g. "169.254.251.223".
//
// 🎯T176: a physical iOS app dialling spyder's app channel over Wi-Fi
// depends on the phone and Mac sharing a LAN that permits peer traffic
// (client isolation, a different SSID, or a VPN silently drops the SYNs).
// iOS 17+ devices always expose a link-local NCM interface over the cable,
// and spyder's LAN listener binds the wildcard address, so the cable path
// is the one that works whenever the device is attached at all.
func IOSUSBNetworkHost(udid string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("usb network lookup: unsupported on %s", runtime.GOOS)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ioreg", "-p", "IOService", "-w0", "-l", "-r", "-c", "IOUSBHostDevice").Output()
	if err != nil {
		return "", fmt.Errorf("ioreg: %w", err)
	}
	ifaces := usbNetworkInterfacesForSerial(string(out), udid)
	if len(ifaces) == 0 {
		return "", fmt.Errorf("usb network lookup: no USB network interface for %s", udid)
	}
	for _, name := range ifaces {
		ifc, err := net.InterfaceByName(name)
		if err != nil || ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		if host := firstIPv4(addrs); host != "" {
			return host, nil
		}
	}
	return "", fmt.Errorf("usb network lookup: no IPv4 address on %v for %s", ifaces, udid)
}

func firstIPv4(addrs []net.Addr) string {
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip4 := ip.To4(); ip4 != nil && !ip4.IsLoopback() && !ip4.IsUnspecified() {
			return ip4.String()
		}
	}
	return ""
}

var (
	ioregObjectRe  = regexp.MustCompile(`\+-o (.+?)\s+<class (\w+)`)
	ioregSerialRe  = regexp.MustCompile(`"USB Serial Number" = "([^"]*)"`)
	ioregBSDNameRe = regexp.MustCompile(`"BSD Name" = "([^"]*)"`)
)

// usbNetworkInterfacesForSerial parses `ioreg -p IOService -l` tree output
// and returns the BSD names of the IOEthernetInterface objects beneath the
// IOUSBHostDevice whose USB serial number matches udid. Apple devices
// report the UDID without its dash as their USB serial number.
func usbNetworkInterfacesForSerial(ioreg, udid string) []string {
	want := normaliseUSBSerial(udid)
	if want == "" {
		return nil
	}
	type node struct {
		col    int
		class  string
		serial string
	}
	var stack []*node
	var out []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(ioreg))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if m := ioregObjectRe.FindStringSubmatchIndex(line); m != nil {
			col := m[0]
			for len(stack) > 0 && stack[len(stack)-1].col >= col {
				stack = stack[:len(stack)-1]
			}
			stack = append(stack, &node{col: col, class: line[m[4]:m[5]]})
			continue
		}
		if len(stack) == 0 {
			continue
		}
		top := stack[len(stack)-1]
		if m := ioregSerialRe.FindStringSubmatch(line); m != nil && top.class == "IOUSBHostDevice" {
			top.serial = normaliseUSBSerial(m[1])
			continue
		}
		if m := ioregBSDNameRe.FindStringSubmatch(line); m != nil && top.class == "IOEthernetInterface" {
			// The nearest enclosing USB device owns this interface; a hub
			// further up must not claim its children's interfaces.
			for i := len(stack) - 2; i >= 0; i-- {
				if stack[i].class != "IOUSBHostDevice" {
					continue
				}
				if stack[i].serial == want && !seen[m[1]] {
					seen[m[1]] = true
					out = append(out, m[1])
				}
				break
			}
		}
	}
	return out
}

func normaliseUSBSerial(s string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), "-", ""))
}

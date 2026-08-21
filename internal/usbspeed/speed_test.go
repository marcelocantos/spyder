// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package usbspeed

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/marcelocantos/spyder/internal/device"
)

// ioregNode is a canned IOUSBHostDevice property block in the same
// shape `ioreg -p IOUSB -w0 -c IOUSBHostDevice` emits on macOS.
func ioregNode(name, product, serial string, class, speed int) string {
	serialLine := ""
	if serial != "" {
		serialLine = fmt.Sprintf("          \"USB Serial Number\" = \"%s\"\n", serial)
	}
	return fmt.Sprintf(`  +-o %s  <class IOUSBHostDevice, id 0x100000001>
        {
          "USB Product Name" = "%s"
          "bDeviceClass" = %d
          "Device Speed" = %d
%s          "IOPowerManagement" = {"CurrentPowerState"=2,"MaxPowerState"=2}
        }
`, name, product, class, speed, serialLine)
}

func census(nodes ...string) []byte {
	body := `+-o Root  <class IORegistryEntry, id 0x100000100, retain 35>
  +-o AppleT8132USBXHCI@01000000  <class AppleT8132USBXHCI, id 0x1000004aa>
`
	for _, n := range nodes {
		body += n
	}
	return []byte(body)
}

func TestParseCensus_AndroidSerialAndIOSDashStripped(t *testing.T) {
	raw := census(
		ioregNode("SAMSUNG_Android@01221000", "SAMSUNG_Android", "RFCX20VKMAR", 0, USBDeviceSpeedSuper),
		ioregNode("iPad@01222000", "iPad", "000081300009702E1110001C", 0, USBDeviceSpeedSuperPlus),
	)
	got := ParseCensus(raw)
	if got[CanonicalID("RFCX20VKMAR")] != USBDeviceSpeedSuper {
		t.Errorf("Android serial = %v; want speed %d", got, USBDeviceSpeedSuper)
	}
	if got[CanonicalID("00008130-0009702E1110001C")] != USBDeviceSpeedSuperPlus {
		t.Errorf("iOS dash-stripped = %v; want speed %d", got, USBDeviceSpeedSuperPlus)
	}
}

func TestParseCensus_DualCompanionsTakeMax(t *testing.T) {
	raw := census(
		ioregNode("SAMSUNG_Android@01100000", "SAMSUNG_Android", "RFCX20VKMAR", 0, USBDeviceSpeedHigh),
		ioregNode("SAMSUNG_Android@01200000", "SAMSUNG_Android", "RFCX20VKMAR", 0, USBDeviceSpeedSuper),
	)
	got := ParseCensus(raw)
	if got[CanonicalID("RFCX20VKMAR")] != USBDeviceSpeedSuper {
		t.Errorf("dual companions = %d; want max %d", got[CanonicalID("RFCX20VKMAR")], USBDeviceSpeedSuper)
	}
}

func TestParseCensus_HubsAndBillboardsIgnored(t *testing.T) {
	raw := census(
		ioregNode("USB3.1 Hub@01200000", "USB3.1 Hub", "HUBSERIAL", usbHubClass, USBDeviceSpeedSuperPlus),
		ioregNode("USB Billboard Device@01150000", "USB Billboard Device", "BILLBOARD1", 0, USBDeviceSpeedHigh),
		ioregNode("USB Billboard Device@01114500", "USB Billboard Device", "0000000000000001", 17, USBDeviceSpeedHigh),
		ioregNode("SAMSUNG_Android@01221000", "SAMSUNG_Android", "RFCX20VKMAR", 0, USBDeviceSpeedSuper),
	)
	got := ParseCensus(raw)
	if _, ok := got[CanonicalID("HUBSERIAL")]; ok {
		t.Errorf("hub serial should be ignored; got %v", got)
	}
	if _, ok := got[CanonicalID("BILLBOARD1")]; ok {
		t.Errorf("billboard serial should be ignored; got %v", got)
	}
	if _, ok := got[CanonicalID("0000000000000001")]; ok {
		t.Errorf("billboard serial should be ignored; got %v", got)
	}
	if got[CanonicalID("RFCX20VKMAR")] != USBDeviceSpeedSuper {
		t.Errorf("phone still present = %v", got)
	}
}

func TestLabel_kUSBDeviceSpeed(t *testing.T) {
	cases := []struct {
		speed int
		want  string
	}{
		{USBDeviceSpeedHigh, "480 Mb/s"},
		{USBDeviceSpeedSuper, "5 Gb/s"},
		{USBDeviceSpeedSuperPlus, "10 Gb/s"},
		{USBDeviceSpeedLow, "1.5 Mb/s"},
		{USBDeviceSpeedFull, "12 Mb/s"},
	}
	for _, tc := range cases {
		got, ok := Label(tc.speed)
		if !ok || got != tc.want {
			t.Errorf("Label(%d) = %q, %v; want %q, true", tc.speed, got, ok, tc.want)
		}
	}
	if _, ok := Label(99); ok {
		t.Error("Label(99) should be unknown")
	}
}

func findUUID(devices []device.Info, uuid string) device.Info {
	for _, d := range devices {
		if d.UUID == uuid {
			return d
		}
	}
	return device.Info{}
}

func TestEnrich_JoinAndOmit(t *testing.T) {
	const (
		androidSerial = "RFCX20VKMAR"
		iosUDID       = "00008130-0009702E1110001C"
		hubSerial     = "HUBSERIAL"
		missing       = "NOT-IN-CENSUS"
	)
	raw := census(
		ioregNode("USB3.1 Hub@01200000", "USB3.1 Hub", hubSerial, usbHubClass, USBDeviceSpeedSuperPlus),
		ioregNode("USB Billboard Device@01150000", "USB Billboard Device", "BILLBOARD1", 0, USBDeviceSpeedHigh),
		ioregNode("SAMSUNG_Android@01100000", "SAMSUNG_Android", androidSerial, 0, USBDeviceSpeedHigh),
		ioregNode("SAMSUNG_Android@01200000", "SAMSUNG_Android", androidSerial, 0, USBDeviceSpeedSuper),
		ioregNode("iPad@01222000", "iPad", "000081300009702E1110001C", 0, USBDeviceSpeedSuperPlus),
		ioregNode("SAMSUNG_Android@01140000", "SAMSUNG_Android", "emulator-5554", 0, USBDeviceSpeedSuper),
		ioregNode("iPhone@01114400", "iPhone", "C6F6FA50-30B5-4E4C-B7A1-8E0F5D1E1FA8", 0, USBDeviceSpeedHigh),
	)
	devices := []device.Info{
		{UUID: androidSerial, Platform: "android", Name: "S24"},
		{UUID: iosUDID, Platform: "ios", Name: "Jevons"},
		{UUID: hubSerial, Platform: "android", Name: "fake-hub-match"},
		{UUID: missing, Platform: "android", Name: "ghost"},
		{UUID: "emulator-5554", Platform: "android", Name: "emu"},
		{UUID: "192.168.1.8:5555", Platform: "android", Name: "wireless"},
		{UUID: "C6F6FA50-30B5-4E4C-B7A1-8E0F5D1E1FA8", Platform: "ios", Name: "sim"},
		{UUID: "/Applications/Foo.app", Platform: "desktop", Name: "desktop"},
	}

	Enrich(devices, raw, nil, nil)

	s24 := findUUID(devices, androidSerial)
	if s24.USBSpeed != "5 Gb/s" {
		t.Errorf("Android serial usb_speed = %q; want 5 Gb/s (dual companion max)", s24.USBSpeed)
	}
	jevons := findUUID(devices, iosUDID)
	if jevons.USBSpeed != "10 Gb/s" {
		t.Errorf("iOS dash-stripped usb_speed = %q; want 10 Gb/s", jevons.USBSpeed)
	}
	for _, uuid := range []string{hubSerial, missing, "emulator-5554", "192.168.1.8:5555", "C6F6FA50-30B5-4E4C-B7A1-8E0F5D1E1FA8", "/Applications/Foo.app"} {
		d := findUUID(devices, uuid)
		if d.USBSpeed != "" || d.USBCeiling != "" || d.USBAnomaly {
			t.Errorf("%s should omit usb fields; got speed=%q ceiling=%q anomaly=%v", uuid, d.USBSpeed, d.USBCeiling, d.USBAnomaly)
		}
	}
	if len(devices) != 8 {
		t.Errorf("len(devices) = %d; join must not drop entries", len(devices))
	}
}

func TestEnrich_IoregGarbageStillReturnsList(t *testing.T) {
	devices := []device.Info{
		{UUID: "RFCX20VKMAR", Platform: "android"},
		{UUID: "00008130-0009702E1110001C", Platform: "ios"},
	}
	Enrich(devices, []byte("not ioreg at all"), nil, nil)
	if devices[0].UUID != "RFCX20VKMAR" || devices[1].UUID != "00008130-0009702E1110001C" {
		t.Fatalf("devices dropped after garbage census: %+v", devices)
	}
	if devices[0].USBSpeed != "" || devices[1].USBSpeed != "" {
		t.Errorf("garbage census must omit usb_speed; got %+v", devices)
	}
}

func TestEnrich_CeilingRatchetAndAnomaly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usb-speed.json")
	store := Open(path)
	raw5 := census(ioregNode("SAMSUNG_Android@1", "SAMSUNG_Android", "RFCX20VKMAR", 0, USBDeviceSpeedSuper))
	devices := []device.Info{{UUID: "RFCX20VKMAR", Platform: "android"}}

	Enrich(devices, raw5, store, nil)
	if devices[0].USBSpeed != "5 Gb/s" {
		t.Fatalf("first live = %q; want 5 Gb/s", devices[0].USBSpeed)
	}
	if devices[0].USBCeiling != "5 Gb/s" {
		t.Fatalf("first ceiling = %q; want 5 Gb/s (seeded)", devices[0].USBCeiling)
	}
	if devices[0].USBAnomaly {
		t.Fatal("first observation must not be an anomaly")
	}

	raw10 := census(ioregNode("SAMSUNG_Android@1", "SAMSUNG_Android", "RFCX20VKMAR", 0, USBDeviceSpeedSuperPlus))
	devices = []device.Info{{UUID: "RFCX20VKMAR", Platform: "android"}}
	Enrich(devices, raw10, store, nil)
	if devices[0].USBCeiling != "10 Gb/s" {
		t.Fatalf("ratchet ceiling = %q; want 10 Gb/s", devices[0].USBCeiling)
	}
	if devices[0].USBAnomaly {
		t.Fatal("higher observation is not an anomaly")
	}

	raw480 := census(ioregNode("SAMSUNG_Android@1", "SAMSUNG_Android", "RFCX20VKMAR", 0, USBDeviceSpeedHigh))
	devices = []device.Info{{UUID: "RFCX20VKMAR", Platform: "android"}}
	Enrich(devices, raw480, store, nil)
	if devices[0].USBSpeed != "480 Mb/s" {
		t.Fatalf("lower live = %q; want 480 Mb/s", devices[0].USBSpeed)
	}
	if devices[0].USBCeiling != "10 Gb/s" {
		t.Fatalf("ceiling after slower plug = %q; want 10 Gb/s", devices[0].USBCeiling)
	}
	if !devices[0].USBAnomaly {
		t.Fatal("live < ceiling must set usb_anomaly")
	}

	reloaded := Open(path)
	devices = []device.Info{{UUID: "RFCX20VKMAR", Platform: "android"}}
	Enrich(devices, raw480, reloaded, nil)
	if devices[0].USBCeiling != "10 Gb/s" || !devices[0].USBAnomaly {
		t.Fatalf("persisted ceiling lost: speed=%q ceiling=%q anomaly=%v", devices[0].USBSpeed, devices[0].USBCeiling, devices[0].USBAnomaly)
	}
}

func TestEnrich_MissingSerialHasNeitherCeilingNorAnomaly(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "usb-speed.json"))
	raw := census(ioregNode("SAMSUNG_Android@1", "SAMSUNG_Android", "RFCX20VKMAR", 0, USBDeviceSpeedSuper))
	devices := []device.Info{
		{UUID: "RFCX20VKMAR", Platform: "android"},
		{UUID: "MISSINGSERIAL", Platform: "android"},
	}
	Enrich(devices, raw, store, nil)
	miss := findUUID(devices, "MISSINGSERIAL")
	if miss.USBSpeed != "" || miss.USBCeiling != "" || miss.USBAnomaly {
		t.Errorf("missing serial has USB fields: %+v", miss)
	}
	if !store.Has("RFCX20VKMAR") {
		t.Error("matched serial should seed the store")
	}
	if store.Has("MISSINGSERIAL") {
		t.Error("missing serial must not write a ceiling")
	}
}

func TestEnrich_InventoryUSBMaxSeedsCeiling(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "usb-speed.json"))
	raw := census(ioregNode("SAMSUNG_Android@1", "SAMSUNG_Android", "RFCX20VKMAR", 0, USBDeviceSpeedHigh))
	devices := []device.Info{{UUID: "RFCX20VKMAR", Platform: "android"}}
	seeds := map[string]string{"RFCX20VKMAR": "5 Gb/s"}
	Enrich(devices, raw, store, seeds)
	if devices[0].USBSpeed != "480 Mb/s" {
		t.Fatalf("live = %q; want 480 Mb/s", devices[0].USBSpeed)
	}
	if devices[0].USBCeiling != "5 Gb/s" {
		t.Fatalf("seeded ceiling = %q; want 5 Gb/s", devices[0].USBCeiling)
	}
	if !devices[0].USBAnomaly {
		t.Fatal("live below inventory usb_max seed should be an anomaly")
	}
}

func TestEnrich_EqualLiveAndCeilingNotAnomaly(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "usb-speed.json"))
	raw := census(ioregNode("iPhone@1", "iPhone", "000081100014182E0AC2801E", 0, USBDeviceSpeedHigh))
	devices := []device.Info{{UUID: "00008110-0014182E0AC2801E", Platform: "ios"}}
	Enrich(devices, raw, store, nil)
	if devices[0].USBSpeed != "480 Mb/s" || devices[0].USBCeiling != "480 Mb/s" || devices[0].USBAnomaly {
		t.Fatalf("equal live/ceiling should not be an anomaly: %+v", devices[0])
	}
}

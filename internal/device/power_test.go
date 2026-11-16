// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"strings"
	"testing"
)

// Captured 2026-11-16 from the lab's Pixel Tablet (3419105H80658D): on AC,
// charging, unlocked, PIN set.
const pixelBattery = `Current Battery Service state:
  AC powered: true
  USB powered: false
  Wireless powered: false
  Dock powered: false
  Max charging current: 1500000
  Max charging voltage: 5000000
  status: 2
  level: 74
  temperature: 288
`

const pixelPowerLock = `    mWakefulness=Awake
    mStayOn=true
mWakefulness=1
      isKeyguardShowing=false
    User "Marcelo" (id=0, flags=0x4c13) (current): trustState=UNTRUSTED, trustManaged=0, deviceLocked=0, isActiveUnlockRunning=0, strongAuthRequired=0x0
      CredentialType: PIN
`

// The A23 (R58T61ER0ZB) on a dead connection: no source, discharging,
// dozing, no credential. Max charging current is 0 here and also when it
// charges normally, so it is not used.
const a23Battery = `  AC powered: false
  USB powered: false
  Wireless powered: false
  Dock powered: false
  Max charging current: 0
  status: 3
  level: 61
  temperature: 297
  current now: -3
`

const a23PowerLock = `    mWakefulness=Dozing
    mStayOn=false
      isKeyguardShowing=false
    User "Owner" (id=0, flags=0x4c13) (current): trusted=0, trustManaged=0, deviceLocked=0, isActiveUnlockRunning=0, strongAuthRequired=0x0
      CredentialType: NONE
`

func androidPower(t *testing.T, battery, lock string) Power {
	t.Helper()
	b, err := parseAndroidBattery([]byte(battery))
	if err != nil {
		t.Fatal(err)
	}
	p := androidPowerFromBattery(b.Extra)
	parseAndroidPowerLock(lock, &p)
	p.Classify()
	return p
}

func TestAndroidPower_PixelHealthy(t *testing.T) {
	p := androidPower(t, pixelBattery, pixelPowerLock)
	if p.Source != PowerSourceAC || p.Status != ChargeCharging || p.TemperatureC == nil || *p.TemperatureC != 28.8 {
		t.Fatalf("battery fields: %+v", p)
	}
	if p.Wakefulness != "Awake" || p.StayOn == nil || !*p.StayOn {
		t.Fatalf("screen fields: %+v", p)
	}
	if p.KeyguardShowing == nil || *p.KeyguardShowing || p.DeviceLocked == nil || *p.DeviceLocked || p.CredentialSet == nil || !*p.CredentialSet {
		t.Fatalf("lock fields: %+v", p)
	}
	if p.Health != PowerOK || len(p.Issues) != 0 {
		t.Fatalf("health %s %v", p.Health, p.Issues)
	}
}

func TestAndroidPower_A23Unpowered(t *testing.T) {
	p := androidPower(t, a23Battery, a23PowerLock)
	if p.Source != PowerSourceNone || p.Status != ChargeDischarging || p.Wakefulness != "Dozing" {
		t.Fatalf("fields: %+v", p)
	}
	if p.CredentialSet == nil || *p.CredentialSet {
		t.Fatalf("CredentialType NONE read as set: %+v", p)
	}
	if p.Health != PowerNotPowered {
		t.Fatalf("health %s %v", p.Health, p.Issues)
	}
	// Discharging with no source is not_powered, not also draining.
	for _, i := range p.Issues {
		if strings.HasPrefix(i, PowerDraining) {
			t.Fatalf("issues %v", p.Issues)
		}
	}
}

// The 15 Nov failure: KEYCODE_SLEEP left four devices behind their PIN.
func TestAndroidPower_LockedBehindPIN(t *testing.T) {
	lock := strings.NewReplacer("isKeyguardShowing=false", "isKeyguardShowing=true", "deviceLocked=0", "deviceLocked=1").Replace(pixelPowerLock)
	p := androidPower(t, pixelBattery, lock)
	if p.Health != PowerLockedSecure {
		t.Fatalf("health %s %v", p.Health, p.Issues)
	}
	// A keyguard with no credential is a swipe, not a setup problem.
	noPIN := strings.Replace(lock, "CredentialType: PIN", "CredentialType: NONE", 1)
	noPIN = strings.Replace(noPIN, "deviceLocked=1", "deviceLocked=0", 1)
	if p := androidPower(t, pixelBattery, noPIN); p.Health != PowerOK {
		t.Fatalf("swipe lock: health %s %v", p.Health, p.Issues)
	}
}

func TestAndroidPower_DrainingAndHot(t *testing.T) {
	b := strings.NewReplacer("status: 2", "status: 3", "temperature: 288", "temperature: 431").Replace(pixelBattery)
	p := androidPower(t, b, pixelPowerLock)
	if p.Health != PowerHot || len(p.Issues) != 2 || !strings.HasPrefix(p.Issues[1], PowerDraining) {
		t.Fatalf("health %s %v", p.Health, p.Issues)
	}
}

// Samsung's battery protection holds the A9 at 80%: powered, status 4.
// That is not a drain.
func TestAndroidPower_NotChargingIsNotDraining(t *testing.T) {
	b := strings.NewReplacer("AC powered: true", "AC powered: false", "USB powered: false", "USB powered: true", "status: 2", "status: 4").Replace(pixelBattery)
	p := androidPower(t, b, pixelPowerLock)
	if p.Source != PowerSourceUSB || p.Status != ChargeNotCharging || p.Health != PowerOK {
		t.Fatalf("%+v", p)
	}
}

func TestIOSPower(t *testing.T) {
	// The iPad (Jevons) on 16 Nov: connected, full, temperature unreported.
	p := iosPower(true, false, true, map[string]any{"InstantAmperage": 0, "Temperature": 0})
	if p.Source != PowerSourceExternal || p.Status != ChargeFull || p.TemperatureC != nil || p.Health != PowerOK {
		t.Fatalf("%+v", p)
	}
	// Connected but losing current under load.
	p = iosPower(true, false, false, map[string]any{"InstantAmperage": -850, "Temperature": 3720})
	if p.Status != ChargeDischarging || p.Health != PowerDraining || *p.TemperatureC != 37.2 {
		t.Fatalf("%+v", p)
	}
	// Unplugged.
	if p := iosPower(false, false, false, nil); p.Health != PowerNotPowered {
		t.Fatalf("%+v", p)
	}
	// Locked per the supervisor: iOS only reports locked behind a passcode.
	locked := true
	p = iosPower(true, true, false, nil)
	p.DeviceLocked = &locked
	p.Classify()
	if p.Health != PowerLockedSecure {
		t.Fatalf("%+v", p)
	}
}

// The S24 on 16 Nov at 17:51: airplane mode on since its 8 Nov boot, Wi-Fi
// off, no default network.
const s24Offline = `airplane_mode_on=1
wifi_on=0
Active default network: none
`

// After `svc wifi enable` at 17:56: Wi-Fi on inside airplane mode,
// validated.
const s24WifiInAirplane = `airplane_mode_on=1
wifi_on=2
Active default network: 100
  NetworkAgentInfo{network{100}  handle{432902426637}  ni{WIFI CONNECTED extra: } created=2026-11-16T06:56:30.807Z Score(Policies : TRANSPORT_PRIMARY&EVER_EVALUATED&IS_UNMETERED&EVER_VALIDATED&IS_VALIDATED ; KeepConnected : 0)
  NetworkAgentInfo{network{99}  handle{1}  ni{MOBILE CONNECTED extra: } Score(Policies : EVER_EVALUATED ; KeepConnected : 0)
`

func TestAndroidPower_Network(t *testing.T) {
	p := androidPower(t, pixelBattery, pixelPowerLock+s24Offline)
	if p.Health != PowerOffline || p.AirplaneMode == nil || !*p.AirplaneMode || p.DefaultNetwork != NetworkNone {
		t.Fatalf("offline: %s %v %+v", p.Health, p.Issues, p)
	}
	if !strings.Contains(p.Issues[0], "airplane mode is on") {
		t.Fatalf("issues %v", p.Issues)
	}
	p = androidPower(t, pixelBattery, pixelPowerLock+s24WifiInAirplane)
	if p.Health != PowerOK || p.DefaultNetwork != "wifi" || p.NetworkValidated == nil || !*p.NetworkValidated || !*p.WifiOn {
		t.Fatalf("wifi in airplane: %s %v %+v", p.Health, p.Issues, p)
	}
	// Connected but not validated: captive portal or no upstream.
	unvalidated := strings.Replace(s24WifiInAirplane, "&IS_VALIDATED", "", 1)
	unvalidated = strings.Replace(unvalidated, "airplane_mode_on=1", "airplane_mode_on=0", 1)
	if p := androidPower(t, pixelBattery, pixelPowerLock+unvalidated); p.Health != PowerOffline || !strings.Contains(p.Issues[0], "not validated") {
		t.Fatalf("unvalidated: %s %v", p.Health, p.Issues)
	}
}

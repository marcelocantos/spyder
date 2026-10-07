# App-channel transports (🎯T128)

The app launches through the platform control plane, then the app itself dials
`SPYDER_APP_CHANNEL`. The control connection is **not** reused as a TCP data
path. The app-channel listener is unauthenticated; keep it off the LAN unless
that is the only viable transport. This is separate from the stream relay.

| Peer | Injected app-channel host | Listener | Route |
| --- | --- | --- | --- |
| iOS simulator | `127.0.0.1` | host loopback | simulator shares host loopback |
| Android emulator (`emulator-*`) | `10.0.2.2` | host loopback | emulator's host-loopback alias |
| USB Android (adb serial, not `host:port`) | `127.0.0.1` | host loopback | `adb -s SERIAL reverse tcp:PORT tcp:PORT`, reinstalled at every launch; removed when listener stops |
| Physical iOS | LAN IPv4 | `0.0.0.0` | device connects over LAN; macOS firewall and iOS Local Network permission can block |
| LAN-only Android (`host:port` adb serial) | LAN IPv4 | `0.0.0.0` | device connects over LAN; no USB reverse assumed |
| Desktop | `127.0.0.1` | host loopback | local process |

The USB reverse is installed **before** launching the app; failure aborts
launch and does not silently fall back to the LAN. The adb reverse mapping
survives a daemon's TCP listener briefly if the process exits without a clean
shutdown; adb drops mappings on disconnection, and the next launch replaces
its mapping. An explicit `SPYDER_APP_CHANNEL` env value still overrides auto
injection (including the choice of listener); operators own that override.

## Physical iOS reverse-path finding

There is no device→host arbitrary-TCP reverse in the transports used here.
`internal/device/ios_forward.go` calls `go-ios/ios/forward.Forward`, whose
listener is a **host-local** socket that opens a usbmux Connect to a **device**
port. usbmux requests originate from the host; the iOS app cannot connect to
a host-local usbmux listener from its own network stack. `ios tunnel start
--userspace` in `internal/iostunnel/supervisor.go` provides the host a route
to a device RSD address, not a listener on device localhost that forwards
connections into the host. An app-side reverse service would require a
separate agent installed on the device; spyder has none. Thus physical iOS
retains LAN dial-back and its firewall exposure until a different, proven
reverse transport exists.

## Developer ID signing / notarisation decision

**Adopt Developer ID signing for distributable macOS releases; do not block
this USB transport change on notarisation.** The existing
`scripts/release-package.sh` invokes `scripts/codesign-spyder.sh`, which
prefers Developer ID Application but can fall back to Apple Development.
That fallback is appropriate for local dev, **not** a durable firewall
allowance for shipped Homebrew versions. An ad-hoc/linker signature has no
stable team identity; a new Cellar path breaks path-based firewall exceptions.
A Developer ID team identity is the stable route for the remaining iOS/LAN
listener, but requires the release operator's certificate and a signed
artifact verification before claiming firewall auto-allow. Notarisation is
worth adopting for Gatekeeper distribution and provenance once credentials
and release automation are in place, but notarisation by itself does not
replace ALF approval and cannot fix the USB route. Do not claim it is active
in the pipeline today. Until the release path enforces Developer ID and tests
ALF on a fresh install, physical iOS remains an explicitly accepted firewall
risk (grant incoming permission or use a manually configured exception).

## Verification boundary

Hermetic `TestUSBAndroidAppChannelReverseHandshake` checks host injection,
loopback-only binding, reverse setup before the app handshake, and cleanup.
`TestReverseTCPCommands` checks the adb command boundary. On a USB-attached
Android device, an adb reverse to a host loopback-only test listener was
observed accepting a device `nc 127.0.0.1:PORT` connection; an actual
instrumented Android app and a firewall-blocked-daemon launch are still
required to close the physical-device acceptance oracle. Do not infer a
macOS ALF result from a hermetic socket test.

### Physical app attempt (2026-10-07)

`TestUSBAndroidAppChannel_Live` is an opt-in, end-to-end oracle: set
`SPYDER_LIVE_ANDROID_SERIAL` to a USB-attached serial and
`SPYDER_LIVE_ANDROID_BUNDLE` to an installed debuggable app that honours the
channel launch extra. On Samsung A23 `R58T61ER0ZB` with the installed
`com.squz.tiltbuggy` development build (installed 2026-07-19), the **test
failed**: the app log said it was dialing `127.0.0.1:56750`, but spyder's
listener reported `Accepted:0` after 25 seconds. At that same stage `adb
reverse --list` showed `UsbFfs tcp:56750 tcp:56750`; a device-side `nc` probe
over that mapping reached the host listener (`Accepted:1`, hello EOF expected).
The macOS application firewall was enabled. Thus the OS transport works and
the native app received the injected value, but the actual app handshake has
**not** been established; the old app build or its dial worker needs diagnosis
or a current app rebuild. Do not mark T128 achieved from the shell probe. A
firewall-denied wildcard baseline was not exercised (changing global ALF rules
is not part of this test).

### Successful end-to-end device run (2026-10-07)

On USB-attached Samsung Galaxy S21 (`R5CR112X76K`), the installed
`com.squz.tiltbuggy` debug app received `SPYDER_APP_CHANNEL=127.0.0.1:57916`,
connected to the host listener via `adb reverse`, completed Hello
(`app=tiltbuggy`, 19 methods), and answered `ping`. The test asserted that
no host LAN interface could connect to that port. macOS Application Firewall
was enabled (`socketfilterfw --getglobalstate` reported `State = 1`), though
an explicit ALF block of the test executable was **not** established: system
firewall mutation requires an administrator and is not an unattended test
side effect. Clean-checkout oracle: `TestUSBAndroidAppChannel_Live`,
`GATE spyder-t128-usb-real-app exit=0 GREEN id=860c57b6`, commit `f6251c47`.

The contrasting A23 run above is device-specific, not a universal reverse
failure: shell-UID `nc` reaches the mapped listener, but `run-as
com.squz.tiltbuggy /system/bin/nc` times out from that app's UID. The same
app-UID probe succeeds on the S21 and on Samsung Tab A9. This points to an
Android per-UID/OEM network restriction on the A23; do not claim the USB
route works for *every* Android device, and do not silently open a LAN
listener in response to a missing handshake. Diagnose the device policy or
choose an explicit LAN transport if USB reverse cannot serve that peer.

For a USB-attached Android device *proven* unable to serve app-UID connections
through `adb reverse` (such as the A23 above), set
`SPYDER_APP_CHANNEL_ANDROID_LAN_SERIALS=R58T61ER0ZB` on spyder. Only that
serial uses the LAN host and a wildcard listener; other USB serials continue
to use reverse and loopback. The optional `SPYDER_APP_CHANNEL_HOST` chooses
the LAN IPv4 as for physical iOS. This is an explicit firewall-dependent
exception, not an automatic fallback on a missed handshake. Remove the serial
from the exception list if device policy changes and USB reverse works again.

The A23 LAN exception was exercised on 2026-10-07 with
`TestAndroidLANException_Live`: it selected `192.168.1.217:58343` and an
all-interfaces listener, but saw `Accepted:0` and no handshake after 25s.
The macOS firewall was enabled; **this is not a passing LAN fallback**. ALF
permission for the test binary or another device-policy investigation is
needed before claiming the A23 can be served. Do not turn on the exception
in production assuming that selection alone proves connectivity.

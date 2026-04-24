# KeepAwake

Minimal SwiftUI companion app. Single responsibility: set
`UIApplication.shared.isIdleTimerDisabled = true` while foregrounded; unset on
background. This is the sole iOS mechanism that reliably prevents display
auto-lock, which is why spyder's autoawake foregrounds this app rather than
attempting pmd3 `PowerAssertion` calls (those turned out to be a no-op for
display sleep; see 🎯T31).

- **Bundle ID**: `com.marcelocantos.spyder.KeepAwake`
- **Targets**: iOS 18+ (iPhone + iPad)

## Install on your devices (one-time)

```bash
cd ios/KeepAwake
open KeepAwake.xcodeproj
```

In Xcode:

1. Select your Apple ID team under **Signing & Capabilities** (free-tier
   personal team works; the `project.pbxproj` pins Marcelo's personal team
   ID, replace with your own).
2. Select the device as the run destination.
3. Hit Run.
4. On the device: trust the developer certificate at
   **Settings → General → VPN & Device Management**.
5. Repeat for each device you want spyder to keep awake.

First install per device requires this Xcode round-trip. After that, the app
stays installed across reboots and autoawake foregrounds it via
`xcrun devicectl device process launch --device <udid>
com.marcelocantos.spyder.KeepAwake` on every new-device detection.

## Simulator build (for development)

```bash
xcodebuild -project KeepAwake.xcodeproj -scheme KeepAwake \
  -destination "generic/platform=iOS Simulator" \
  CODE_SIGNING_ALLOWED=NO build
```

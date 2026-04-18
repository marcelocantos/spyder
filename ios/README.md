# KeepAwake

Minimal SwiftUI companion app. Single responsibility: set
`UIApplication.shared.isIdleTimerDisabled = true` while foregrounded; unset on
background.

- **Bundle ID**: `com.marcelocantos.spyder.KeepAwake`
- **Targets**: iOS 18+ (iPhone + iPad)

## Build

The Xcode project is regenerated from `KeepAwake/project.yml` via
[xcodegen](https://github.com/yonaskolb/XcodeGen); the `.xcodeproj`
itself is gitignored.

```bash
brew install xcodegen            # once
cd ios/KeepAwake
xcodegen generate                # produces KeepAwake.xcodeproj

# Simulator build (no signing):
xcodebuild -project KeepAwake.xcodeproj -scheme KeepAwake \
  -destination "generic/platform=iOS Simulator" \
  CODE_SIGNING_ALLOWED=NO build

# Device build (requires Apple ID linked in Xcode → Settings → Accounts,
# with a personal team having GJF5DNC392 or update project.yml DEVELOPMENT_TEAM):
xcodebuild -project KeepAwake.xcodeproj -scheme KeepAwake \
  -destination "generic/platform=iOS" \
  -allowProvisioningUpdates build
```

## Install on Pippa

Open `KeepAwake.xcodeproj` in Xcode, select Pippa as the run destination,
and click Run. Xcode handles the signing/provisioning round-trip
automatically on a free Apple ID. Once installed, the app survives
device reboot.

The `keepawake` MCP tool foregrounds the already-installed app via
`xcrun devicectl device process launch --device <udid>
com.marcelocantos.spyder.KeepAwake`.

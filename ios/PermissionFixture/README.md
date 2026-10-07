# Disposable permission prompt (🎯T154)

This app has its own bundle ID, `com.marcelocantos.spyder.PermissionFixture`.
On first launch it browses for a fictitious Bonjour service, causing iOS to
show the Local Network permission alert. It does not connect to discovered
hosts. **Use a test device. Never uninstall or redeploy Jevons or another app
to reset a permission prompt.**

```sh
make permission-fixture DEVICE=<physical-udid> TEAM=<signing-team>
# Reserve the device, then uninstall *only this fixture* to reset its prompt:
spyder uninstall <physical-udid> com.marcelocantos.spyder.PermissionFixture
make permission-fixture DEVICE=<physical-udid> TEAM=<signing-team>
spyder launch-app <physical-udid> com.marcelocantos.spyder.PermissionFixture
spyder system-alert <physical-udid> --json
spyder system-alert <physical-udid> --tap Allow --title-contains "local network" --wait-ms 45000
```

Alternatively run the opt-in in-process integration test with
`SPYDER_LIVE_ALERT_DEVICE`, `SPYDER_LIVE_ALERT_APP`, and
`SPYDER_LIVE_ALERT_BUNDLE` set (see `internal/mcp/system_alert_live_test.go`).
The device also needs Spyder's XCUITest alert runner, installed separately
using `make alert-runner`. An Xcode account and provisioning profile for this
bundle ID are required; unsigned simulator compilation does not prove
physical-device alert handling.

The original T154 Jevons-specific *app channel* acceptance remains unverified:
this fixture deliberately has no Jevons app channel, and a real Jevons
uninstall/redeploy is not an acceptable substitute for a disposable test.

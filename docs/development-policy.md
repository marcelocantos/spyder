# Host development flags

Spyder remembers development flags in `~/.spyder/dev-policy.json` (or the directory selected by `SPYDER_DATA_DIR`). No office
policy is built into Spyder. Settings apply to an app only when it advertises
`dev_session_configure`, has a keyed device/bundle app-channel session, and its
device has fresh proof of a physical USB connection to this Mac.

The daemon reconciles existing sessions every two seconds. It sends
`dev_session_configure` with `{flags: {...}, lease_ms: 10000}` and requires an
explicit `{applied: true}` acknowledgement. The app must expire the lease and
restore its normal behavior after ten seconds without renewal. Empty flags
revoke the lease. No app-container setting survives disconnection or restart.
The initial MinicadesKit consumer accepts `orientation` in development builds.
Spyder treats flags as string values and leaves supported-value validation to
the advertising app; a rejected RPC is shown as an error, never as applied.

A complete profile looks like this (these are examples, not shipped defaults):

```json
{
  "version": 1,
  "global": {},
  "groups": [
    {
      "name": "minicades",
      "bundle_prefixes": ["com.minicades.", "com.minicadesmobile."],
      "bundle_ids": [
        "gokarting.minicadesmobile.com",
        "com.EPEL.dragster",
        "com.DefaultCompany.drag-n-brag"
      ],
      "flags": {"orientation": "landscapeRight"}
    }
  ],
  "apps": {
    "com.minicades.stockcars": {"orientation": "landscapeLeft"}
  }
}
```

Precedence is global, matching groups in their listed order, then exact bundle.
The result includes per-key provenance. App identity is the bundle/package ID
bound to its listener, never the calling directory or display name. Prefixes
must end with a dot, so `com.minicades.` cannot match `com.minicadesfake.game`.
Session overrides are not exposed; edit an exact-app entry for a temporary
exception, then remove it.

```console
spyder dev-policy set /private/tmp/office-policy.json --as office
spyder dev-policy show --bundle-id com.minicades.stockcars
spyder dev-policy clear --as office
```

`set` replaces the complete configuration after validation and an atomic write.
Its `--as` owner is persisted. A device reserved by another owner receives no
renewals; any lease this policy previously applied is revoked. Use the policy
owner for the office device reservation when testing. `clear` removes all flag
scopes and causes active leases to be revoked on the next sweep.

The same verbs are available over REST at `POST /api/v1/dev_policy` and through
Starlark:

```python
emit(dev_policy(op="get", bundle_id="com.minicades.stockcars"))
# set requires config=<complete JSON-equivalent dictionary>, owner="office".
# clear requires op="clear", owner="office".
```

The response includes the saved profile, configured flags and provenance,
capability status, current USB proof, last acknowledgement, applied flags,
lease deadline, and errors for each live session. A configuration being saved
is distinct from an app confirming it was applied. Disconnected sessions are
removed from this status list. Applications without the capability are skipped.

Every sweep obtains new IOKit USB serials and `adb devices -l` transport tags.
It dispatches lease requests concurrently, each with a one-second deadline,
so unresponsive apps cannot queue healthy apps behind their timeouts. A
monotonic age check rejects USB proof older than two seconds at dispatch.
A wireless usbmux connection, Android Wi-Fi address, emulator, or cached device
inventory is not proof of USB attachment. Enumeration failure prevents renewal
and sends a revoke if the app remains reachable. USB removal is detected on the
next sweep, including when an app retains its LAN channel; an unreachable app
falls back when its lease expires. A failed revoke remains visible until expiry.

For an isolated development daemon, build the tree to a scratch path and run
it with an absolute `SPYDER_DATA_DIR` and `--addr :3131`:

```console
SPYDER_DATA_DIR=/private/tmp/stockcars-dev-policy-20261015/state /private/tmp/stockcars-dev-policy-20261015/spyder-isolated serve --addr :3131
```

Copy only the device inventory into that state directory and configure its
policy through that daemon's CLI (`SPYDER_DAEMON_URL=http://127.0.0.1:3131`).
The default directory remains `~/.spyder` when `SPYDER_DATA_DIR` is unset or
empty. Relative overrides are resolved by the process's working directory;
use an absolute path for predictable isolation. Keep `HOME` unchanged: ADB
and Apple tools must retain their normal pairing and authentication keys.
Leave the supervised daemon on port 3030 untouched. Physical-device live
verification must cover apply/renew, policy clear, USB detach with LAN retained,
lease expiry on daemon shutdown, reconnect, and a store/non-capable app.

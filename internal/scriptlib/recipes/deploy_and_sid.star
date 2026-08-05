# Deploy an app build and return its live app-channel session id (🎯T120).
# Params: device, path, bundle_id? (derived from the bundle when omitted).
#
# spyder auto-creates the app-channel listener and injects
# SPYDER_APP_CHANNEL into the launch env; the app dials back and sends
# `hello`. This seed replaces the manual deploy → sleep → app_channel_list
# → session_id dance.
#
#   run_script(path="deploy_and_sid",
#              params={"device": "Jevons", "path": "/path/to/MyApp.app"})

device = params["device"] if "device" in params else ""
path = params["path"] if "path" in params else ""
want_bundle = params["bundle_id"] if "bundle_id" in params else ""

if not device or not path:
    fail("deploy_and_sid: params device and path are required")

if want_bundle:
    d = deploy_app(device=device, path=path, bundle_id=want_bundle)
else:
    d = deploy_app(device=device, path=path)
bundle = d["bundle_id"]

# Poll for the app to dial back (channel apps connect within a few seconds).
sid = ""
port = 0
for i in range(20):
    for l in app_channel_list()["listeners"]:
        if l["bundle_id"] == bundle:
            for s in l["sessions"]:
                sid = s["session_id"]
                port = l["port"]
    if sid:
        break
    sleep(500)

if not sid:
    fail("deploy_and_sid: %s launched (pid %s) but never connected to the " % (bundle, d["pid"]) +
         "app channel — is it a debug build with the spyder channel compiled in?")

emit({
    "recipe": "deploy_and_sid",
    "device": device,
    "bundle_id": bundle,
    "pid": d["pid"],
    "session_id": sid,
    "port": port,
})

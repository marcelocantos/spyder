// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// 🎯T113: topical help() slices. help("app"), help("device"), … return a
// focused reference with copy-paste Starlark recipes for the common flows,
// so an agent doesn't have to digest the full verb dump to get moving.
// Bare help() keeps the full verb list and names the topics.
package mcp

import (
	"sort"
	"strings"
)

// helpTopics maps topic name → focused guide text. Recipes are valid
// Starlark against the real builtin surface — keep them copy-paste
// runnable (TestExec_HelpTopics* and the seed-compile test guard the
// verbs they reference).
var helpTopics = map[string]string{
	"app": `app — drive a connected app over the app channel

launch_app/deploy_app auto-create the channel (spyder injects
SPYDER_APP_CHANNEL; the app dials back and sends hello). Address a
session by session_id, by device+bundle_id, or omit both when exactly
one session is connected.

recipes:
# find the live session after a launch
ls = app_channel_list()
sid = ls["listeners"][0]["sessions"][0]["session_id"]
emit(sid)

# frame-perfect tap → capture
app_pause(session_id=sid)
app_input(session_id=sid, type="finger_down", x=0.5, y=0.5)
app_input(session_id=sid, type="finger_up", x=0.5, y=0.5)
app_step(session_id=sid, frames=1)
emit(app_screenshot(session_id=sid))
app_resume(session_id=sid)

# query a state slice (server-side jq select)
emit(app_state(session_id=sid, slice="physics", select=".bodies"))

# discover then call game-private RPCs (never invent method names)
m = app_methods(session_id=sid, scope="app")
emit(m)
emit(app_call(session_id=sid, method="set_view",
              params={"lon": 31.5, "lat": 33.8, "zoom": 2.25}))

# bounded poll of a HUD slice
for i in range(10):
    emit(app_state(session_id=sid, slice="hud"))
    sleep(200)`,

	"device": `device — inventory, state, and OS-level observation

devices() lists everything spyder can see (inventory aliases attached);
resolve() maps an alias to its platform IDs. Observational verbs are
never reservation-gated.

recipes:
# what is connected right now?
devices(platform="all")

# alias → all known platform IDs
resolve(name="iPad")

# battery / thermal / foreground app
device_state(device="iPad")

# OS screenshot (works even if someone else holds the device)
emit(screenshot(device="iPad"))

# is my app up? (no forced launch)
is_running(device="iPad", bundle_id="com.example.app")`,

	"deploy": `deploy — install, launch, and reach the app channel

deploy_app is the atomic path: terminate → install → launch → verify
pid, returning {bundle_id, pid}. Pass env={} to inject launch-time
environment; spyder always injects SPYDER_APP_CHANNEL so the app can
dial back.

recipes:
# atomic deploy (bundle_id derived from the bundle)
d = deploy_app(device="Jevons", path="/path/to/MyApp.app")
emit(d)

# deploy with extra env
deploy_app(device="Jevons", path="/path/to/MyApp.app",
           env={"FEATURE_FLAG_X": "on"})

# launch an installed app with env
launch_app(device="Jevons", bundle_id="com.example.app",
           env={"DEBUG_HUD": "1"})

# deploy → wait for the app-channel session id (bundled seed, 🎯T120)
run_script(path="deploy_and_sid",
           params={"device": "Jevons", "path": "/path/to/MyApp.app"})

# stop it again
terminate_app(device="Jevons", bundle_id="com.example.app")`,

	"capture": `capture — timeseries, metrics, and log collection

app_state_capture_* polls a state slice into a buffer; app_metrics_*
is the zero-I/O per-frame ring; app_log_get / log_capture_* collect
logs. All are observational — never reservation-gated.

recipes:
# capture a slice at 60 Hz while driving, then drain
cap = app_state_capture_start(session_id=sid, slice="physics",
                              interval_ms=16, select=".bodies")
app_input(session_id=sid, type="accel", value=[0.3, 0.0, 9.8])
sleep(500)
emit(app_state_capture_stop(capture_id=cap["capture_id"]))

# per-frame metrics ring: arm → drive → dump
app_metrics_arm(session_id=sid, series=["dt"])
sleep(1000)
emit(app_metrics_dump(session_id=sid))

# drain structured app logs pushed since the last call
emit(app_log_get(session_id=sid))

# managed OS log capture across calls
c = log_capture_start(device="iPad", bundle_id="com.example.app")
emit(c)
# … later call: log_capture_stop(session_id=c["session_id"])`,

	"reservations": `reservations — exclusive holds for parallel sessions

Policy (🎯T116): reservations gate device-state-mutating verbs ONLY
(launch_app, terminate_app, install_app, uninstall_app, deploy_app,
launch_player, rotate, network, perf_fps, port_forward_*, input_tap,
input_swipe). Observational verbs (screenshot, record_*, logs, state
reads) always succeed regardless of who holds the device. app_exec
does NOT auto-reserve: verbs inside a script pass owner= exactly like
direct calls, and a foreign hold fails the verb immediately with a
structured conflict naming the holder — never a hang.

recipes:
# who holds what right now?
reservations()

# am I gated on this device? (🎯T116 observability)
emit(reservation_status(device="iPad", owner="tiltbuggy"))

# take a hold for a long sequence, then release
reserve(device="iPad", owner="tiltbuggy", ttl_seconds=3600)
# … mutating calls pass owner= …
launch_app(device="iPad", bundle_id="com.example.app", owner="tiltbuggy")
release(device="iPad", owner="tiltbuggy")

# fuzzy hold: any idle iPad
reserve(selector="{\"platform\":\"ios\",\"model_family\":\"ipad\"}",
        owner="tiltbuggy")`,

	"scripts": `scripts — durable Starlark library (🎯T108/🎯T120)

Recipes live as plain .star files: bundled in the binary, overridable
under ~/.spyder/scripts/<name>.star. Run them via app_exec
script_path=…, or run_script from inside a script; the params dict
carries string parameters.

recipes:
# what is available?
list_scripts()

# run a bundled seed by name
run_script(path="skeleton")

# deploy → session id in one call (🎯T120 seed)
run_script(path="deploy_and_sid",
           params={"device": "Jevons", "path": "/path/to/MyApp.app"})

# ge/yourworld mode smoke: start_mode → set_view → slice → screenshot
run_script(path="yw_mode_smoke",
           params={"session_id": sid, "mode": "europe"})

# equivalent from the MCP envelope:
# app_exec(script_path="deploy_and_sid", params={"device": "Jevons", ...})`,

	"stream": `stream — players and screen recording

launch_player starts spyder's stream glass against a headless game
server (SP2S command-stream replay). record_* captures mp4 on iOS
simulators / Android; recordings are observational — gated on the
recording owner, never the device reservation.

recipes:
# stream glass against a game server on this device
launch_player(device="iPad", owner="me")

# record around an event (two app_exec calls)
record_start(device="iPad", owner="me")
# … trigger the event, then in a later call:
# record_stop(device="iPad", owner="me")

# rotate a sim/emu before capturing
rotate(device="<sim-udid>", orientation="landscape-left")`,
}

// helpTopicNames returns the sorted topic list for discovery and errors.
func helpTopicNames() []string {
	names := make([]string, 0, len(helpTopics))
	for n := range helpTopics {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// helpTopicList is the comma-joined topic list used in help() output and
// unknown-topic errors.
func helpTopicList() string {
	return strings.Join(helpTopicNames(), ", ")
}

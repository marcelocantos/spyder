# ge/yourworld mode smoke (🎯T120): start a fresh mode, optionally frame
# the camera, read the game slice, capture a screenshot.
#
# Works against any ge game that advertises start_mode/set_view app RPCs
# in its appchannel hello (yourworld2 does, debug builds only). Discovery
# first, invocation second — fails closed if the game doesn't advertise
# the methods rather than guessing.
#
# Params: session_id? (omit when exactly one session is connected),
#         mode? (default "europe" — yourworld accepts world|usstates|
#         africa|americas|asia|auspacific|europe|cities|landmarks|wonders),
#         lon?, lat?, zoom? (set_view camera framing, all optional)
#
#   run_script(path="yw_mode_smoke",
#              params={"session_id": sid, "mode": "europe"})

sid = params["session_id"] if "session_id" in params else ""
mode = params["mode"] if "mode" in params else "europe"

def call(method, p):
    if sid:
        return app_call(session_id=sid, method=method, params=p)
    return app_call(method=method, params=p)

m = app_methods(session_id=sid, scope="app") if sid else app_methods(scope="app")
have = [x["name"] for x in m["methods"]]
if "start_mode" not in have:
    fail("yw_mode_smoke: app does not advertise start_mode (got %s) — " % have +
         "debug build with app-private RPCs required")

emit(call("start_mode", {"mode": mode}))
sleep(500)

if "set_view" in have and "lon" in params and "lat" in params:
    view = {"lon": float(params["lon"]), "lat": float(params["lat"])}
    if "zoom" in params:
        view["zoom"] = float(params["zoom"])
    call("set_view", view)
    sleep(300)

g = app_state(session_id=sid, slice="game") if sid else app_state(slice="game")
emit({"recipe": "yw_mode_smoke", "mode": mode, "game": g})
emit(app_screenshot(session_id=sid) if sid else app_screenshot())

# E1 Explore: tilt/accel probe — sample physics under synthetic gravity.
# Params: session_id (optional)

sid = params["session_id"] if "session_id" in params else ""

_input = app_input
_state = app_state
if sid:
    app_input(session_id=sid, type="accel", x=0.25, y=0.0, z=0.0)
else:
    app_input(type="accel", x=0.25, y=0.0, z=0.0)

obs = []
for i in range(30):
    if sid:
        s = app_state(session_id=sid, slice="physics", select=".bodies // .")
    else:
        s = app_state(slice="physics", select=".bodies // .")
    obs.append(s)
    sleep(16)

last = obs[len(obs) - 1] if len(obs) > 0 else None
emit({"recipe": "explore_tilt_probe", "samples": len(obs), "last": last})

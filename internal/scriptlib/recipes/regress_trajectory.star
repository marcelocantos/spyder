# R1 Regress: trajectory corridor under fixed accel.
sid = params["session_id"] if "session_id" in params else ""
fixture = params["fixture"] if "fixture" in params else ""
min_x = float(params["min_x"]) if "min_x" in params else -1000.0
max_x = float(params["max_x"]) if "max_x" in params else 1000.0
min_y = float(params["min_y"]) if "min_y" in params else -1000.0
max_y = float(params["max_y"]) if "max_y" in params else 1000.0

if fixture:
    if sid:
        app_restore_state(session_id=sid, path=fixture)
    else:
        app_restore_state(path=fixture)

if sid:
    app_pause(session_id=sid)
else:
    app_pause()

pts = []
for i in range(30):
    if sid:
        app_input(session_id=sid, type="accel", x=0.2, y=0.0, z=0.0)
        app_step(session_id=sid, frames=1)
        s = app_state(session_id=sid, slice="physics", select=".bodies.buggy.position // .bodies.marble.position // .")
    else:
        app_input(type="accel", x=0.2, y=0.0, z=0.0)
        app_step(frames=1)
        s = app_state(slice="physics", select=".bodies.buggy.position // .bodies.marble.position // .")
    if type(s) == "dict":
        x = s["x"] if "x" in s else 0.0
        y = s["y"] if "y" in s else 0.0
        pts.append({"x": x, "y": y})
    elif type(s) == "list" and len(s) >= 2:
        pts.append({"x": s[0], "y": s[1]})

if sid:
    app_resume(session_id=sid)
else:
    app_resume()

assert_trajectory(points=pts, min_x=min_x, max_x=max_x, min_y=min_y, max_y=max_y)
emit({"recipe": "regress_trajectory", "n": len(pts), "ok": True})

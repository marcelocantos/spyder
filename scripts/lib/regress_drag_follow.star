# R2 Regress: drag-follow p95 error bound.
sid = params["session_id"] if "session_id" in params else ""
max_p95 = float(params["max_p95"]) if "max_p95" in params else 0.15

finger = []
object_pts = []

if sid:
    app_input(session_id=sid, type="finger_down", x=0.4, y=0.5)
else:
    app_input(type="finger_down", x=0.4, y=0.5)

for i in range(12):
    x = 0.4 + 0.02 * i
    y = 0.5
    if sid:
        app_input(session_id=sid, type="finger_motion", x=x, y=y)
        s = app_state(session_id=sid, slice="physics", select=".bodies.buggy.screen // .bodies.buggy.position // .")
    else:
        app_input(type="finger_motion", x=x, y=y)
        s = app_state(slice="physics", select=".bodies.buggy.screen // .bodies.buggy.position // .")
    finger.append({"x": x, "y": y})
    if type(s) == "dict":
        if "cx" in s:
            ox = s["cx"]
            oy = s["cy"] if "cy" in s else 0.0
        else:
            ox = s["x"] if "x" in s else 0.0
            oy = s["y"] if "y" in s else 0.0
        object_pts.append({"x": ox, "y": oy})
    else:
        object_pts.append({"x": 0.0, "y": 0.0})
    sleep(16)

x_end = 0.4 + 0.02 * 11
if sid:
    app_input(session_id=sid, type="finger_up", x=x_end, y=0.5)
else:
    app_input(type="finger_up", x=x_end, y=0.5)

assert_drag_follow(finger=finger, object=object_pts, max_p95=max_p95)
emit({"recipe": "regress_drag_follow", "n": len(finger), "ok": True, "max_p95": max_p95})

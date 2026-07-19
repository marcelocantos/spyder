# E2 Explore: finger drag path (L0 normalized coords).
sid = params["session_id"] if "session_id" in params else ""

if sid:
    app_input(session_id=sid, type="finger_down", x=0.45, y=0.50)
else:
    app_input(type="finger_down", x=0.45, y=0.50)

path = []
for i in range(15):
    x = 0.45 + 0.02 * i
    if sid:
        app_input(session_id=sid, type="finger_motion", x=x, y=0.50)
        st = app_state(session_id=sid, slice="physics", select=".bodies // .")
    else:
        app_input(type="finger_motion", x=x, y=0.50)
        st = app_state(slice="physics", select=".bodies // .")
    path.append({"finger_x": x, "finger_y": 0.50, "state": st})
    sleep(16)

x_end = 0.45 + 0.02 * 14
if sid:
    app_input(session_id=sid, type="finger_up", x=x_end, y=0.50)
else:
    app_input(type="finger_up", x=x_end, y=0.50)

tail = path[len(path) - 1] if len(path) > 0 else None
emit({"recipe": "explore_drag", "steps": len(path), "path_tail": tail})

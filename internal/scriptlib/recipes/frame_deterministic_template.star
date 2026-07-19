# Frame-accurate drive template (T108.5):
# restore → pause → inject → step → observe → resume
sid = params["session_id"] if "session_id" in params else ""
fixture = params["fixture"] if "fixture" in params else ""

if fixture:
    if sid:
        app_restore_state(session_id=sid, path=fixture)
    else:
        app_restore_state(path=fixture)

if sid:
    app_pause(session_id=sid)
else:
    app_pause()

path = []
for i in range(10):
    if sid:
        app_input(session_id=sid, type="accel", x=0.15, y=0.0, z=0.0)
        app_step(session_id=sid, frames=1)
        path.append(app_state(session_id=sid, slice="physics", select=".bodies // ."))
    else:
        app_input(type="accel", x=0.15, y=0.0, z=0.0)
        app_step(frames=1)
        path.append(app_state(slice="physics", select=".bodies // ."))

if sid:
    app_resume(session_id=sid)
else:
    app_resume()

emit({"recipe": "frame_deterministic_template", "frames": len(path), "path": path})

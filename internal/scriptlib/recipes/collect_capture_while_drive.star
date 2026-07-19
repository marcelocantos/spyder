# C1 Collect: capture-while-drive (tilt stimulus + timeseries).
sid = params["session_id"] if "session_id" in params else ""

stimulus = [
    {"t_ms": 0, "type": "accel", "x": 0.3, "y": 0.0, "z": 0.0},
    {"t_ms": 500, "type": "accel", "x": 0.0, "y": 0.0, "z": 0.0},
]

if sid:
    cap = app_state_capture_start(session_id=sid, slice="physics", interval_ms=16, select=".bodies // .")
else:
    cap = app_state_capture_start(slice="physics", interval_ms=16, select=".bodies // .")

cid = cap["capture_id"]

if sid:
    app_input(session_id=sid, type="accel", x=0.3, y=0.0, z=0.0)
else:
    app_input(type="accel", x=0.3, y=0.0, z=0.0)
sleep(500)
if sid:
    app_input(session_id=sid, type="accel", x=0.0, y=0.0, z=0.0)
else:
    app_input(type="accel", x=0.0, y=0.0, z=0.0)
sleep(200)

drained = app_state_capture_stop(capture_id=cid)
dropped = drained["dropped_samples"] if type(drained) == "dict" and "dropped_samples" in drained else 0
errs = drained["errors"] if type(drained) == "dict" and "errors" in drained else 0

emit({
    "recipe": "collect_capture_while_drive",
    "stimulus": stimulus,
    "capture_id": cid,
    "samples": drained,
    "dropped_samples": dropped,
    "errors": errs,
})

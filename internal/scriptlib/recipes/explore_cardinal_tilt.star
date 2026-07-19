# Cardinal tilt probe with fine-grained accel authority.
# Takes accel stream (override), holds each direction, restores passthrough.
# Params: session_id

sid = params["session_id"] if "session_id" in params else ""

dirs = [
    {"name": "right", "x": 1.5, "y": 0.0, "z": 0.0},
    {"name": "left", "x": -1.5, "y": 0.0, "z": 0.0},
    {"name": "up", "x": 0.0, "y": -1.5, "z": 0.0},
    {"name": "down", "x": 0.0, "y": 1.5, "z": 0.0},
]

def set_override(x, y, z):
    if sid:
        return app_sensor_control(session_id=sid, sensor="accel", mode="override", x=x, y=y, z=z)
    return app_sensor_control(sensor="accel", mode="override", x=x, y=y, z=z)

def query_control():
    if sid:
        return app_sensor_control(session_id=sid, sensor="accel")
    return app_sensor_control(sensor="accel")

def set_passthrough():
    if sid:
        return app_sensor_control(session_id=sid, sensor="accel", mode="passthrough")
    return app_sensor_control(sensor="accel", mode="passthrough")

def pose():
    if sid:
        g = app_state(session_id=sid, slice="geometry")
    else:
        g = app_state(slice="geometry")
    for b in g["bodies"]:
        if b["id"] == "buggy":
            return {"pos": b["pos"], "vel": b["vel"], "angle": b["angle"]}
    return g

results = []
# Claim accel stream only — touch/keys still real/passthrough.
set_override(0.0, 0.0, 9.8)
sleep(200)
results.append({"phase": "start", "pose": pose(), "control": query_control()})

for d in dirs:
    # Update latch (re-asserted every frame while override is on).
    set_override(d["x"], d["y"], d["z"])
    sleep(800)
    results.append({
        "phase": d["name"],
        "accel": {"x": d["x"], "y": d["y"], "z": d["z"]},
        "pose": pose(),
    })
    set_override(0.0, 0.0, 9.8)
    sleep(200)

# Always restore real sensors.
set_passthrough()
results.append({"phase": "end", "pose": pose(), "control": query_control()})
emit({"recipe": "explore_cardinal_tilt", "results": results})

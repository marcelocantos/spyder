# Cardinal tilt with sticky override enable/disable + value=[x,y,z].
# Params: session_id

sid = params["session_id"] if "session_id" in params else ""

dirs = [
    {"name": "right", "value": [1.5, 0.0, 0.0]},
    {"name": "left", "value": [-1.5, 0.0, 0.0]},
    {"name": "up", "value": [0.0, -1.5, 0.0]},
    {"name": "down", "value": [0.0, 1.5, 0.0]},
]

def enable(value):
    if sid:
        return app_sensor_override_enable(session_id=sid, sensor="accel", value=value)
    return app_sensor_override_enable(sensor="accel", value=value)

def set_value(value):
    if sid:
        return app_sensor_override_set(session_id=sid, sensor="accel", value=value)
    return app_sensor_override_set(sensor="accel", value=value)

def disable():
    if sid:
        return app_sensor_disable(session_id=sid, sensor="accel")
    return app_sensor_disable(sensor="accel")

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
# Sticky enable — stays until disable, not one-frame.
enable([0.0, 0.0, 9.8])
sleep(200)
results.append({"phase": "start", "pose": pose()})

for d in dirs:
    set_value(d["value"])
    sleep(800)
    results.append({"phase": d["name"], "value": d["value"], "pose": pose()})
    set_value([0.0, 0.0, 9.8])
    sleep(200)

disable()
results.append({"phase": "end", "pose": pose()})
emit({"recipe": "explore_cardinal_tilt", "results": results})

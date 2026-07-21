# L1/L2 (🎯T109): list hit_targets → find by id/role → tap centre.
# No hard-coded x,y. Prefer id or role over display label.
# Params: session_id (optional), id or role (default id=reset), slice (default hit_targets).
sid = params["session_id"] if "session_id" in params else ""
key = ""
if "id" in params and params["id"]:
    key = params["id"]
elif "role" in params and params["role"]:
    key = params["role"]
else:
    key = "reset"
slice_name = params["slice"] if "slice" in params else "hit_targets"

if sid:
    payload = app_state(session_id=sid, slice=slice_name)
else:
    payload = app_state(slice=slice_name)

node = find_hit_target(nodes=payload, key=key)
xy = resolve_target(node=node)
cx = xy["cx"]
cy = xy["cy"]

if sid:
    app_input(session_id=sid, type="finger_down", x=cx, y=cy)
    app_input(session_id=sid, type="finger_up", x=cx, y=cy)
else:
    app_input(type="finger_down", x=cx, y=cy)
    app_input(type="finger_up", x=cx, y=cy)

emit({"recipe": "l1_tap_hit_target", "key": key, "cx": cx, "cy": cy, "id": node.get("id"), "role": node.get("role")})

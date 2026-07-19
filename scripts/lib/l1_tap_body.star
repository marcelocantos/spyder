# L1: resolve labelled body → inject at centre.
sid = params["session_id"] if "session_id" in params else ""
label = params["label"] if "label" in params else "buggy"

if sid:
    bodies = app_state(session_id=sid, slice="physics", select=".bodies // .")
else:
    bodies = app_state(slice="physics", select=".bodies // .")

node = find_by_label(nodes=bodies, label=label)
xy = resolve_target(node=node)
cx = xy["cx"]
cy = xy["cy"]

if sid:
    app_input(session_id=sid, type="finger_down", x=cx, y=cy)
    app_input(session_id=sid, type="finger_up", x=cx, y=cy)
else:
    app_input(type="finger_down", x=cx, y=cy)
    app_input(type="finger_up", x=cx, y=cy)

emit({"recipe": "l1_tap_body", "label": label, "cx": cx, "cy": cy})

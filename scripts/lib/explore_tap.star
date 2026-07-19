# E3 Explore: center tap + HUD observation.
sid = params["session_id"] if "session_id" in params else ""

if sid:
    app_input(session_id=sid, type="finger_down", x=0.5, y=0.5)
    app_input(session_id=sid, type="finger_up", x=0.5, y=0.5)
    sleep(100)
    hud = app_state(session_id=sid, slice="hud")
else:
    app_input(type="finger_down", x=0.5, y=0.5)
    app_input(type="finger_up", x=0.5, y=0.5)
    sleep(100)
    hud = app_state(slice="hud")

emit({"recipe": "explore_tap", "hud": hud})

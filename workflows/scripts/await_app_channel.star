# The app's channel reaches spyder: a session for the bundle on this device.
sess = wait_app_session(device=params["device"], bundle_id=params["bundle_id"], timeout_ms=60000)
if not sess or not sess.get("session_id"):
    fail("the app channel did not connect")
emit({"step": "app_channel", "session_id": sess.get("session_id")})

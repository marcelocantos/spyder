# Wait for an iOS system alert and tap one of its buttons (🎯T154).
# params: device, button, owner, title (optional text the alert title must contain)
device = params["device"]
result = system_alert_tap(device=device, button=params["button"], wait_ms=float(params.get("wait_ms", "45000")), owner=params["owner"])
title = params.get("title", "")
if title and title.lower() not in result["before"]["title"].lower():
    fail("tapped an unexpected alert: " + result["before"]["title"])
emit({"step": "answered", "alert": result["before"]["title"], "tapped": result["tapped"], "after": result["after"]})

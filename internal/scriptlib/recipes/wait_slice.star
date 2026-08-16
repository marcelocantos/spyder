# Poll a named app-channel slice until a jq select is truthy (🎯T129).
# Replaces hand-rolled `for i in range(N): if pred(): break; sleep(ms)`.
#
# params: device, bundle_id, slice (required), select? (jq; default
# select(.present and .active) for carousel-shaped slices), timeout_ms?,
# poll_ms?
#
#   run_script(path="wait_slice",
#              params={"device":"S24","bundle_id":"com.minicades.stockcars",
#                      "slice":"carousel"})

dev = params.get("device", "")
bid = params.get("bundle_id", "")
sl = params.get("slice", "")
sel = params.get("select", "select(.present == true and .active == true)")
if not dev or not bid or not sl:
    fail("wait_slice: params device, bundle_id, and slice are required")

to = int(params.get("timeout_ms", "10000"))
po = int(params.get("poll_ms", "200"))
v = wait_state(device=dev, bundle_id=bid, slice=sl, select=sel,
               timeout_ms=to, poll_ms=po)
emit({"recipe": "wait_slice", "ok": True, "slice": sl, "value": v})

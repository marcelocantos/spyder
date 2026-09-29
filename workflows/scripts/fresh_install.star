# Uninstall an app (if installed), then deploy its development build, so its
# next launch is a fresh install.
device = params["device"]
owner = params["owner"]
bundle = params["bundle_id"]
reserve(device=device, owner=owner, ttl_seconds=900)
if bundle in [app["bundle_id"] for app in list_apps(device=device)]:
    uninstall_app(device=device, bundle_id=bundle, owner=owner)
emit({"step": "deployed", "result": deploy_app(device=device, path=params["app"], bundle_id=bundle, owner=owner)})

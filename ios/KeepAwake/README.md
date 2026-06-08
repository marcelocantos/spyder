# KeepAwake

Manual iOS sidecar for devices without a "Never" auto-lock setting.

Build and run it from Xcode on the target device, then leave the app in
the foreground while the device is plugged in. The app disables the idle
timer while active and exits automatically when the device is unplugged.

The project targets iOS 15.0 and later. It is intentionally standalone:
spyder does not auto-install or supervise it.

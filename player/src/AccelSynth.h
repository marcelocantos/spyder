// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// AccelSynth — synthesize SDL_EVENT_SENSOR_UPDATE events from Shift-gated
// mouse motion when no real accelerometer is available (desktop, iOS
// simulator, Android emulator).
//
// iOS Simulator notes:
//  - Core Motion reports accelerometerAvailable=YES; we force
//    realSensorAvailable()=false so synth still arms.
//  - Do NOT enable SDL relative mouse mode on any iOS (device or sim).
//    UIKit delivers absolute pointer motion (SDL_SendMouseMotion relative=
//    false). With WINDOW_MOUSE_RELATIVE_MODE set, SDL drops those absolute
//    events (SDL_mouse.c), while GCMouse relative deltas are rarely available
//    for the host cursor — so arming relative mode silences all tilt motion.
//    AccelSynth uses absolute x/y deltas (and any non-zero xrel) instead.
//  - Hardware-keyboard Shift is unreliable on sim (Connect Hardware Keyboard
//    must be on). So on TARGET_OS_SIMULATOR we also arm while the primary
//    mouse button is held — click-drag alone tilts.
//
// Android emulator notes (same practical class as iOS Simulator):
//  - AVD exposes a virtual accelerometer, so realSensorAvailable() would
//    otherwise open it and *never* arm AccelSynth. Host mouse then does
//    nothing useful; the virtual sensor just reports constant gravity.
//  - Detect via ro.kernel.qemu / goldfish|ranchu / sdk_gphone* and treat
//    like the iOS Simulator: no real sensor, primary-button arm, no
//    relative-mouse mode (host pointer through the emu is absolute).
//
// Stream server path: same primary-button arm as the simulator.
// Player forwards device-local raw events (no content remap). Finger denorm
// uses setSurfacePixels(player DeviceInfo) — never the stream-host window.
//
// Desktop direct (not server, not sim): Shift + drag only, with relative mouse
// mode so the cursor doesn't leave the window. Button alone does not tilt.
//
// Belongs to the render subsystem. DirectRenderHost owns synthesis.
// Stream players forward raw Shift/drag; server-side synth interprets.
// Games only see SDL_EVENT_SENSOR_UPDATE. Direct vs stream: same AccelSynth
// math on the same device-local gesture units (transport is not meaning).
//
// Tilt model: mouse displacement from the arm point is the tilt vector.
// Magnitude × kTiltRadPerPixel → angle; axis ⊥ displacement in screen plane.
//
// ACCELSYNTH_AUTODRIVE=1 (iOS Simulator only): fixed 100 px X tilt for 2 s
// then ease — for matrix cells that cannot inject GCKeyboard Shift.
#pragma once

#include <SDL3/SDL.h>
#include <spdlog/spdlog.h>

#if defined(__APPLE__)
#include <TargetConditionals.h>
#endif

#if defined(__ANDROID__)
#include <sys/system_properties.h>
#include <cstring>
#endif

#include <cmath>
#include <cstdlib>
#include <functional>

namespace spyder {

constexpr float kTiltRadPerPixel = 0.0026f;  // ≈ π/4 / 300
constexpr float kG = 9.81f;

struct Tilt {
    float x = 0.f;
    float y = 0.f;
};

// Android AVD (not physical devices). Virtual sensors exist; host mouse is
// the practical tilt path — same role as iOS Simulator.
inline bool isAndroidEmulator() {
#if defined(__ANDROID__)
    char val[PROP_VALUE_MAX] = {};
    if (__system_property_get("ro.kernel.qemu", val) > 0 && val[0] == '1')
        return true;
    if (__system_property_get("ro.hardware", val) > 0 &&
        (std::strstr(val, "goldfish") || std::strstr(val, "ranchu")))
        return true;
    if (__system_property_get("ro.product.model", val) > 0 &&
        (std::strstr(val, "sdk_gphone") || std::strstr(val, "Emulator") ||
         std::strstr(val, "Android SDK")))
        return true;
#endif
    return false;
}

// Relative mouse for desktop Shift-drag only. Never on absolute-pointer
// hosts (iOS / Android emulator) — relative mode drops absolute motion.
inline void setRelativeMouseForShiftDrag(SDL_Window* window, bool armed) {
#if defined(__APPLE__) && TARGET_OS_IOS
    (void)window;
    (void)armed;
    return;
#else
    if (isAndroidEmulator()) {
        (void)window;
        (void)armed;
        return;
    }
    SDL_Window* w = window ? window : SDL_GetMouseFocus();
    if (w) SDL_SetWindowRelativeMouseMode(w, armed);
#endif
}

// True for touch-generated synthetic mouse events (SDL_TOUCH_MOUSEID).
// Direct game input and stream forward both drop these so one physical
// touch is not processed twice (finger + mouse).
inline bool isTouchSyntheticMouse(const SDL_Event& e) {
    if (e.type == SDL_EVENT_MOUSE_MOTION)
        return e.motion.which == SDL_TOUCH_MOUSEID;
    if (e.type == SDL_EVENT_MOUSE_BUTTON_DOWN ||
        e.type == SDL_EVENT_MOUSE_BUTTON_UP)
        return e.button.which == SDL_TOUCH_MOUSEID;
    return false;
}

class AccelSynth {
public:
    AccelSynth() = default;

    // Host window (relative-mouse on desktop; optional surface fallback).
    void setWindow(SDL_Window* w) {
        window_ = w;
        if (w && !surfacePinned_) {
            int ww = 0, wh = 0;
            SDL_GetWindowSizeInPixels(w, &ww, &wh);
            if (ww > 0 && wh > 0) {
                surfaceW_ = ww;
                surfaceH_ = wh;
            }
        }
    }

    // Device-local surface size for finger 0–1 → pixel denormalization.
    // Direct: match the local window. Stream: player DeviceInfo (viewer)
    // pixels — never the Mac host swapchain. Pinning stops setWindow from
    // overwriting with host size.
    void setSurfacePixels(int w, int h) {
        if (w > 0 && h > 0) {
            surfaceW_ = w;
            surfaceH_ = h;
            surfacePinned_ = true;
        }
    }

    void setEmit(std::function<void(const SDL_Event&)> fn) { emit_ = std::move(fn); }

    Tilt current() const { return tilt_; }

    // true = consumed (do not forward to the game as raw mouse/key).
    bool handle(const SDL_Event& e) {
        // ── Modifier / button arm ─────────────────────────────────
        if ((e.type == SDL_EVENT_KEY_DOWN || e.type == SDL_EVENT_KEY_UP) &&
            isShiftKey(e)) {
            const bool down = (e.type == SDL_EVENT_KEY_DOWN);
            if (down != shiftKey_) {
                shiftKey_ = down;
                onArmChanged();
            }
            return true;
        }

        if (e.type == SDL_EVENT_MOUSE_BUTTON_DOWN ||
            e.type == SDL_EVENT_MOUSE_BUTTON_UP) {
            // Caller should drop TOUCH_MOUSEID; still ignore if it arrives.
            if (isTouchSyntheticMouse(e)) return false;
            if (e.button.button == SDL_BUTTON_LEFT) {
                const bool down = (e.type == SDL_EVENT_MOUSE_BUTTON_DOWN);
                if (down != primaryDown_) {
                    primaryDown_ = down;
                    onArmChanged();
                }
            }
            // Do not consume button events — games may still want them.
            // On sim, button arms tilt but still reaches the game as a no-op.
            return false;
        }

        // Finger drag — same arm policy as primary mouse button (sim + stream
        // server). iOS Simulator host clicks often arrive as finger events;
        // without this path, only GCMouse-shaped motion would tilt.
        if (e.type == SDL_EVENT_FINGER_DOWN || e.type == SDL_EVENT_FINGER_UP ||
            e.type == SDL_EVENT_FINGER_CANCELED) {
            const bool down = (e.type == SDL_EVENT_FINGER_DOWN);
            if (down != primaryDown_) {
                primaryDown_ = down;
                onArmChanged();
            }
            // Do not consume — games may treat finger as UI input.
            return false;
        }

        if (e.type == SDL_EVENT_MOUSE_MOTION && armed()) {
            if (isTouchSyntheticMouse(e)) return false;
            applyMotionDelta(e.motion.xrel, e.motion.yrel,
                             e.motion.x, e.motion.y);
            return true;
        }

        if (e.type == SDL_EVENT_FINGER_MOTION && armed()) {
            // SDL finger x/y and dx/dy are normalized 0–1 on the *device*
            // surface. Denormalize with setSurfacePixels (player DeviceInfo
            // when streaming; local window when direct) — never the stream
            // host Mac window alone.
            const float sw = surfaceWidth();
            const float sh = surfaceHeight();
            const float dx = e.tfinger.dx * sw;
            const float dy = e.tfinger.dy * sh;
            const float ax = e.tfinger.x * sw;
            const float ay = e.tfinger.y * sh;
            applyMotionDelta(dx, dy, ax, ay);
            return true;
        }

        return false;
    }

    void update() {
#if defined(__APPLE__) && TARGET_OS_SIMULATOR
        if (!autodriveChecked_) {
            autodriveChecked_ = true;
            const char* env = std::getenv("ACCELSYNTH_AUTODRIVE");
            if (env && env[0] == '1') {
                autodriveActive_ = true;
                autodriveStartNs_ = SDL_GetPerformanceCounter();
                SPDLOG_INFO("AccelSynth: ACCELSYNTH_AUTODRIVE active");
            }
        }
        if (autodriveActive_) {
            const uint64_t freq = SDL_GetPerformanceFrequency();
            const float elapsed =
                float(SDL_GetPerformanceCounter() - autodriveStartNs_) /
                float(freq);
            if (elapsed < 2.0f) {
                tilt_.x = 100.f;
                tilt_.y = 0.f;
                emitSensorFromTilt();
                return;
            }
            autodriveActive_ = false;
            easing_ = true;
            lastTickNs_ = 0;
            SPDLOG_INFO("AccelSynth: ACCELSYNTH_AUTODRIVE drive complete, easing");
        }
#endif
        if (!easing_) return;
        const uint64_t now = SDL_GetPerformanceCounter();
        if (lastTickNs_ == 0) {
            lastTickNs_ = now;
            return;
        }
        const uint64_t freq = SDL_GetPerformanceFrequency();
        const float dt = float(now - lastTickNs_) / float(freq);
        lastTickNs_ = now;

        constexpr float kTau = 0.08f;
        const float decay = std::exp(-dt / kTau);
        tilt_.x *= decay;
        tilt_.y *= decay;
        if (std::sqrt(tilt_.x * tilt_.x + tilt_.y * tilt_.y) < 0.5f) {
            tilt_ = {};
            easing_ = false;
        }
        emitSensorFromTilt();
    }

    // Usable real accelerometer? iOS Simulator and Android emulator: false
    // (virtual sensors exist but host mouse tilt is the practical path).
    static bool realSensorAvailable() {
#if defined(__APPLE__) && TARGET_OS_SIMULATOR
        return false;
#else
        if (isAndroidEmulator()) return false;
        int count = 0;
        SDL_SensorID* ids = SDL_GetSensors(&count);
        if (ids) SDL_free(ids);
        return count > 0;
#endif
    }

private:
    static bool isShiftKey(const SDL_Event& e) {
        if (e.key.scancode == SDL_SCANCODE_LSHIFT ||
            e.key.scancode == SDL_SCANCODE_RSHIFT)
            return true;
        // Keycode path (some hosts fill key, not only scancode).
        if (e.key.key == SDLK_LSHIFT || e.key.key == SDLK_RSHIFT)
            return true;
        return false;
    }

    // Primary-button arm: iOS Simulator, Android emulator, stream server.
    static bool armOnPrimaryButton() {
#if defined(SPYDER_SERVER_BUILD)
        return true;
#elif defined(__APPLE__) && TARGET_OS_SIMULATOR
        return true;
#else
        return isAndroidEmulator();
#endif
    }

    // Armed when Shift is held (key event or current mod state).
    // On simulators / stream server also when primary button is held
    // (click-drag) — see file header for why.
    bool armed() const {
        if (shiftKey_) return true;
        if ((SDL_GetModState() & SDL_KMOD_SHIFT) != 0) return true;
        if (armOnPrimaryButton() && primaryDown_) return true;
        return false;
    }

    void onArmChanged() {
        const bool now = armed();
        setRelativeMouseForShiftDrag(window_, now);
        if (now) {
            // Fresh capture each time we re-arm.
            if (!wasArmed_) {
                tilt_ = {};
                easing_ = false;
                haveLastAbs_ = false;
            }
        } else {
            easing_ = true;
            lastTickNs_ = 0;
            haveLastAbs_ = false;
        }
        wasArmed_ = now;
    }

    // Relative deltas if non-zero; else absolute position seed/delta.
    void applyMotionDelta(float dx, float dy, float absX, float absY) {
        if (dx == 0.f && dy == 0.f) {
            if (haveLastAbs_) {
                dx = absX - lastAbsX_;
                dy = absY - lastAbsY_;
            }
            lastAbsX_ = absX;
            lastAbsY_ = absY;
            haveLastAbs_ = true;
        } else {
            haveLastAbs_ = false;
        }
        if (dx != 0.f || dy != 0.f) {
            tilt_.x += dx;
            tilt_.y += dy;
            emitSensorFromTilt();
        }
    }

    float surfaceWidth() const {
        if (surfaceW_ > 0) return float(surfaceW_);
        if (window_) {
            int ww = 0, wh = 0;
            SDL_GetWindowSizeInPixels(window_, &ww, &wh);
            if (ww > 0) return float(ww);
        }
        return 1.f;
    }
    float surfaceHeight() const {
        if (surfaceH_ > 0) return float(surfaceH_);
        if (window_) {
            int ww = 0, wh = 0;
            SDL_GetWindowSizeInPixels(window_, &ww, &wh);
            if (wh > 0) return float(wh);
        }
        return 1.f;
    }

    void emitSensorFromTilt() {
        if (!emit_) return;
        float mag = std::sqrt(tilt_.x * tilt_.x + tilt_.y * tilt_.y);
        float gx = 0.f, gy = 0.f;
        if (mag > 0.f) {
            float angle = mag * kTiltRadPerPixel;
            float s = std::sin(angle);
            gx = -kG * s * tilt_.x / mag;
            gy =  kG * s * tilt_.y / mag;
        }
        SDL_Event se{};
        se.type = SDL_EVENT_SENSOR_UPDATE;
        se.sensor.data[0] = gx;
        se.sensor.data[1] = gy;
        emit_(se);
    }

    Tilt tilt_;
    bool shiftKey_ = false;
    bool primaryDown_ = false;
    bool wasArmed_ = false;
    bool easing_ = false;
    bool haveLastAbs_ = false;
    float lastAbsX_ = 0.f;
    float lastAbsY_ = 0.f;
    uint64_t lastTickNs_ = 0;
    SDL_Window* window_ = nullptr;
    int surfaceW_ = 0;
    int surfaceH_ = 0;
    bool surfacePinned_ = false;
    std::function<void(const SDL_Event&)> emit_;
#if defined(__APPLE__) && TARGET_OS_SIMULATOR
    bool autodriveChecked_ = false;
    bool autodriveActive_ = false;
    uint64_t autodriveStartNs_ = 0;
#endif
};

} // namespace spyder

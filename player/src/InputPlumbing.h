// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// Player-side input delivery plumbing (🎯T156.5). NOT a copy of the engine's
// AccelSynth: the player never synthesizes tilt or models gravity. These two
// helpers only affect delivery quality of forwarded events:
//  - relative-mouse arming so relayed motion has usable xrel/yrel;
//  - real-accelerometer enumeration for the DeviceInfo capability bit.
// Arm policy derives from this glass's own declared device class — the same
// facts the server derives its policy from — so the two cannot drift apart.

#pragma once

#include <SDL3/SDL.h>

namespace spyder {

// Enable/disable relative mouse for tilt-gesture delivery. Never on iOS:
// UIKit delivers absolute pointer motion, and relative mode silences it.
inline void setRelativeMouseForShiftDrag(SDL_Window* window, bool armed) {
#if defined(__APPLE__) && TARGET_OS_IOS
    (void)window;
    (void)armed;
#else
    SDL_Window* w = window ? window : SDL_GetMouseFocus();
    if (w) SDL_SetWindowRelativeMouseMode(w, armed);
#endif
}

// Usable real accelerometer on this glass? iOS Simulator always false
// (Core Motion misreports availability there). Emscripten likewise: SDL's
// web backend enumerates a devicemotion-backed accelerometer that desktop
// browsers never feed (samples read NaN) and mobile browsers gate behind a
// permission prompt — browser motion glue is 🎯T101.6.
inline bool realSensorAvailable() {
#if (defined(__APPLE__) && TARGET_OS_SIMULATOR) || defined(__EMSCRIPTEN__)
    return false;
#else
    int count = 0;
    SDL_SensorID* ids = SDL_GetSensors(&count);
    if (ids) SDL_free(ids);
    return count > 0;
#endif
}

// True for touch-generated synthetic mouse events (SDL_TOUCH_MOUSEID).
// Both direct hosts and the player drop these so one physical touch is not
// processed twice (finger + synthetic mouse) — same rule, same constant.
inline bool isTouchSyntheticMouse(const SDL_Event& e) {
    if (e.type == SDL_EVENT_MOUSE_MOTION)
        return e.motion.which == SDL_TOUCH_MOUSEID;
    if (e.type == SDL_EVENT_MOUSE_BUTTON_DOWN ||
        e.type == SDL_EVENT_MOUSE_BUTTON_UP)
        return e.button.which == SDL_TOUCH_MOUSEID;
    return false;
}

} // namespace spyder

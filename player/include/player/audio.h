// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// spyder::audio — engine-driven pause/resume for SDL audio devices.
//
// The engine automatically pauses every registered device when the app is
// backgrounded or loses audio focus (incoming call, alarm, another media
// app), and resumes them when the app returns to the foreground / regains
// focus. Game code typically does not call pauseAll/resumeAll directly.
//
// Usage:
//   // In your audio init:
//   SDL_AudioDeviceID dev = SDL_OpenAudioDevice(...);
//   auto reg = spyder::audio::registerDevice(dev);  // RAII; keep alive
//
// The AudioPlayer (player-side, wire mode) calls registerDevice itself.
// Direct-mode apps that own their own SDL audio device call it once.
//
// Desktop builds are unaffected: SDL does not fire background/foreground
// events on macOS/Linux/Windows, and there is no audio focus concept.
#pragma once

#include <cstdint>

// SDL_AudioDeviceID is typedef Uint32 SDL_AudioDeviceID. We use the raw
// uint32_t here to avoid pulling in SDL headers from this public header —
// the conversion is implicit since Uint32 is always uint32_t. Callers
// that hold SDL_AudioDeviceID values can pass them directly.
using ge_audio_DeviceID = uint32_t;

namespace spyder::audio {

// RAII registration handle. While this object is alive the corresponding
// SDL audio device is managed by the engine. Dropping it unregisters.
// Non-copyable; moveable.
struct Registration {
    Registration() = default;
    ~Registration();

    Registration(const Registration&) = delete;
    Registration& operator=(const Registration&) = delete;
    Registration(Registration&& o) noexcept;
    Registration& operator=(Registration&& o) noexcept;

    explicit operator bool() const { return id_ != 0; }

private:
    friend Registration registerDevice(ge_audio_DeviceID);
    explicit Registration(ge_audio_DeviceID id) : id_(id) {}
    ge_audio_DeviceID id_ = 0;
};

// Register an SDL audio device for engine-driven pause/resume.
// Accepts SDL_AudioDeviceID (which is uint32_t) implicitly.
// Returns a RAII handle — let it drop to unregister.
[[nodiscard]] Registration registerDevice(ge_audio_DeviceID device);

// Manual pause/resume — game code rarely needs these directly.
// The engine calls them automatically on lifecycle transitions.
void pauseAll();
void resumeAll();

// Current focus state. Active = foreground + focus held.
// Paused = backgrounded or interrupted (incoming call, alarm, etc.).
enum class FocusState { Active, Paused };
FocusState state();

// Engine-internal: called by DirectRenderHost / AudioPlayer on lifecycle
// transitions. Not part of the game-facing public API but declared here
// so the header is self-contained and platform implementations can call
// through without a separate internal header.
//
// onBackground — app moving to background (SDL_EVENT_DID_ENTER_BACKGROUND
//                or equivalent window-level events on Android).
// onForeground — app returning to foreground.
// onAudioFocusLost   — platform audio focus lost (call, alarm, etc.).
// onAudioFocusGained — platform audio focus regained.
void onBackground();
void onForeground();
void onAudioFocusLost();
void onAudioFocusGained();

} // namespace spyder::audio

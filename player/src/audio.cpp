// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// spyder::audio — core state machine for engine-driven audio pause/resume.
//
// Two orthogonal "silence reasons" must BOTH be absent before audio plays:
//   1. Background: app is not in the foreground.
//   2. Focus lost: platform audio focus was taken by another app.
//
// Both are tracked as reference counts so nested calls don't accidentally
// re-enable audio mid-interruption. The SDL operations (Pause/Resume) are
// thread-safe per SDL3 docs; the global mutex protects the device list and
// the count pair, not the SDL call itself.

#include <player/audio.h>

#include <SDL3/SDL.h>
#include <spdlog/spdlog.h>

#include <mutex>
#include <vector>

namespace spyder::audio {

namespace {

struct State {
    std::mutex mu;
    std::vector<SDL_AudioDeviceID> devices;
    // Counts rather than bools: each nested onBackground/onForeground pair
    // is balanced. On a real device we'll never see nesting, but unit tests
    // call in arbitrary order.
    int backgroundDepth  = 0;  // > 0 → app is backgrounded
    int focusLostDepth   = 0;  // > 0 → platform focus lost

    bool isSilenced() const { return backgroundDepth > 0 || focusLostDepth > 0; }

    void applyAll(bool pause) {
        for (SDL_AudioDeviceID dev : devices) {
            if (dev == 0) continue;
            if (pause) {
                SDL_PauseAudioDevice(dev);
            } else {
                SDL_ResumeAudioDevice(dev);
            }
        }
    }

    // Pause or resume depending on current silence state.
    void sync() { applyAll(isSilenced()); }
};

State& g() {
    static State s;
    return s;
}

} // namespace

// ── Registration RAII ───────────────────────────────────────────────────────

Registration registerDevice(ge_audio_DeviceID device) {
    if (device == 0) return {};
    auto& s = g();
    std::lock_guard<std::mutex> lock(s.mu);
    s.devices.push_back(device);
    // If already silenced, pause the new device immediately.
    if (s.isSilenced()) {
        SDL_PauseAudioDevice(device);
    }
    SPDLOG_DEBUG("spyder::audio: registered device {}", device);
    return Registration{device};
}

Registration::Registration(Registration&& o) noexcept : id_(o.id_) {
    o.id_ = 0;
}

Registration& Registration::operator=(Registration&& o) noexcept {
    if (this != &o) {
        if (id_ != 0) {
            auto& s = g();
            std::lock_guard<std::mutex> lock(s.mu);
            auto it = std::find(s.devices.begin(), s.devices.end(), id_);
            if (it != s.devices.end()) s.devices.erase(it);
        }
        id_ = o.id_;
        o.id_ = 0;
    }
    return *this;
}

Registration::~Registration() {
    if (id_ == 0) return;
    auto& s = g();
    std::lock_guard<std::mutex> lock(s.mu);
    auto it = std::find(s.devices.begin(), s.devices.end(), id_);
    if (it != s.devices.end()) s.devices.erase(it);
    SPDLOG_DEBUG("spyder::audio: unregistered device {}", id_);
}

// ── Manual pause / resume ───────────────────────────────────────────────────

void pauseAll() {
    auto& s = g();
    std::lock_guard<std::mutex> lock(s.mu);
    s.applyAll(/*pause=*/true);
    SPDLOG_DEBUG("spyder::audio: pauseAll ({} device(s))", s.devices.size());
}

void resumeAll() {
    auto& s = g();
    std::lock_guard<std::mutex> lock(s.mu);
    if (!s.isSilenced()) {
        s.applyAll(/*pause=*/false);
        SPDLOG_DEBUG("spyder::audio: resumeAll ({} device(s))", s.devices.size());
    }
}

// ── State query ─────────────────────────────────────────────────────────────

FocusState state() {
    auto& s = g();
    std::lock_guard<std::mutex> lock(s.mu);
    return s.isSilenced() ? FocusState::Paused : FocusState::Active;
}

// ── Lifecycle hooks (called by engine internals) ────────────────────────────

void onBackground() {
    auto& s = g();
    std::lock_guard<std::mutex> lock(s.mu);
    const bool wasSilenced = s.isSilenced();
    ++s.backgroundDepth;
    if (!wasSilenced) {
        SPDLOG_INFO("spyder::audio: backgrounded — pausing {} device(s)", s.devices.size());
        s.applyAll(/*pause=*/true);
    }
}

void onForeground() {
    auto& s = g();
    std::lock_guard<std::mutex> lock(s.mu);
    if (s.backgroundDepth > 0) --s.backgroundDepth;
    if (!s.isSilenced()) {
        SPDLOG_INFO("spyder::audio: foregrounded — resuming {} device(s)", s.devices.size());
        s.applyAll(/*pause=*/false);
    }
}

void onAudioFocusLost() {
    auto& s = g();
    std::lock_guard<std::mutex> lock(s.mu);
    const bool wasSilenced = s.isSilenced();
    ++s.focusLostDepth;
    if (!wasSilenced) {
        SPDLOG_INFO("spyder::audio: audio focus lost — pausing {} device(s)", s.devices.size());
        s.applyAll(/*pause=*/true);
    }
}

void onAudioFocusGained() {
    auto& s = g();
    std::lock_guard<std::mutex> lock(s.mu);
    if (s.focusLostDepth > 0) --s.focusLostDepth;
    if (!s.isSilenced()) {
        SPDLOG_INFO("spyder::audio: audio focus gained — resuming {} device(s)", s.devices.size());
        s.applyAll(/*pause=*/false);
    }
}

// Engine-internal testing hook — stays inside spyder::audio so it can access
// the file-scoped g() singleton.
namespace testing {

void reset() {
    auto& s = g();
    std::lock_guard<std::mutex> lock(s.mu);
    // Drop all device registrations (tests should have let RAII handles
    // drop before calling reset, but be defensive). Do NOT call SDL
    // Pause/Resume on the device IDs — tests use stub IDs that don't
    // correspond to real SDL devices.
    s.devices.clear();
    s.backgroundDepth = 0;
    s.focusLostDepth  = 0;
}

} // namespace testing

} // namespace spyder::audio

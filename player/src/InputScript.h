// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// InputScript — deterministic scripted device-local input for the 🎯T156
// parity oracle. One command per line; '#' starts a comment:
//
//   SLEEP <ms>                     advance script clock
//   SHIFT <down|up>                modifier key
//   MOTION <xrel> <yrel>           relative mouse motion (device pixels)
//   FINGER_DOWN <x> <y>            normalized 0–1 device surface
//   FINGER_MOTION <x> <y> <dx> <dy>
//   FINGER_UP
//   SENSOR <gx> <gy>               real-accelerometer sample (screen frame)
//   QUIT                           push SDL_EVENT_QUIT (ends a direct run)
//
// The SAME script drives a direct host (GE_INPUT_SCRIPT env, injected in
// DirectRenderHost::pumpEvents) and a headless player (--script, injected
// at the player's upstream-event boundary). The spyder player carries a
// byte-identical mirror of this header (same convention as Protocol.h).

#pragma once

#include <SDL3/SDL.h>

#include <cstdio>
#include <cstring>
#include <fstream>
#include <sstream>
#include <string>
#include <vector>

namespace ge {

struct ScriptedEvent {
    uint32_t atMs = 0;   // script-clock time this event fires
    SDL_Event ev{};
};

// Parse a script file. Returns false on open failure; malformed lines are
// skipped (logged to stderr) so a typo cannot silently reorder a gesture.
inline bool loadInputScript(const std::string& path,
                            std::vector<ScriptedEvent>& out) {
    std::ifstream in(path);
    if (!in.good()) return false;
    uint32_t clockMs = 0;
    std::string line;
    int lineNo = 0;
    while (std::getline(in, line)) {
        ++lineNo;
        const auto hash = line.find('#');
        if (hash != std::string::npos) line.erase(hash);
        std::istringstream ss(line);
        std::string cmd;
        if (!(ss >> cmd)) continue;

        auto push = [&](const SDL_Event& e) {
            ScriptedEvent se;
            se.atMs = clockMs;
            se.ev = e;
            out.push_back(se);
        };
        bool ok = true;
        if (cmd == "SLEEP") {
            uint32_t ms = 0;
            ok = static_cast<bool>(ss >> ms);
            if (ok) clockMs += ms;
        } else if (cmd == "SHIFT") {
            std::string state;
            ok = static_cast<bool>(ss >> state) &&
                 (state == "down" || state == "up");
            if (ok) {
                SDL_Event e{};
                e.type = (state == "down") ? SDL_EVENT_KEY_DOWN
                                           : SDL_EVENT_KEY_UP;
                e.key.scancode = SDL_SCANCODE_LSHIFT;
                e.key.key = SDLK_LSHIFT;
                push(e);
            }
        } else if (cmd == "MOTION") {
            float xr = 0, yr = 0;
            ok = static_cast<bool>(ss >> xr >> yr);
            if (ok) {
                SDL_Event e{};
                e.type = SDL_EVENT_MOUSE_MOTION;
                e.motion.xrel = xr;
                e.motion.yrel = yr;
                push(e);
            }
        } else if (cmd == "FINGER_DOWN" || cmd == "FINGER_MOTION" ||
                   cmd == "FINGER_UP") {
            SDL_Event e{};
            if (cmd == "FINGER_DOWN") {
                float x = 0, y = 0;
                ok = static_cast<bool>(ss >> x >> y);
                e.type = SDL_EVENT_FINGER_DOWN;
                e.tfinger.x = x;
                e.tfinger.y = y;
            } else if (cmd == "FINGER_MOTION") {
                float x = 0, y = 0, dx = 0, dy = 0;
                ok = static_cast<bool>(ss >> x >> y >> dx >> dy);
                e.type = SDL_EVENT_FINGER_MOTION;
                e.tfinger.x = x;
                e.tfinger.y = y;
                e.tfinger.dx = dx;
                e.tfinger.dy = dy;
            } else {
                e.type = SDL_EVENT_FINGER_UP;
            }
            if (ok) push(e);
        } else if (cmd == "QUIT") {
            SDL_Event e{};
            e.type = SDL_EVENT_QUIT;
            push(e);
        } else if (cmd == "SENSOR") {
            float gx = 0, gy = 0;
            ok = static_cast<bool>(ss >> gx >> gy);
            if (ok) {
                SDL_Event e{};
                e.type = SDL_EVENT_SENSOR_UPDATE;
                e.sensor.data[0] = gx;
                e.sensor.data[1] = gy;
                push(e);
            }
        } else {
            ok = false;
        }
        if (!ok)
            std::fprintf(stderr, "InputScript: %s:%d malformed line: %s\n",
                         path.c_str(), lineNo, line.c_str());
    }
    return true;
}

// Incremental player for a loaded script: returns events due at or before
// nowMs (script clock starts at the first poll).
class InputScriptPlayer {
public:
    explicit InputScriptPlayer(std::vector<ScriptedEvent> events)
        : events_(std::move(events)) {}

    bool done() const { return next_ >= events_.size(); }

    template <typename Fn>
    void poll(uint32_t nowMs, Fn&& deliver) {
        if (!started_) {
            started_ = true;
            baseMs_ = nowMs;
        }
        const uint32_t t = nowMs - baseMs_;
        while (next_ < events_.size() && events_[next_].atMs <= t) {
            deliver(events_[next_].ev);
            ++next_;
        }
    }

private:
    std::vector<ScriptedEvent> events_;
    size_t next_ = 0;
    uint32_t baseMs_ = 0;
    bool started_ = false;
};

} // namespace ge

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// PlayerRender — the render half of the player (brokered modality).
//
// Counterpart to DirectRenderHost, but for the player: owns the SDL
// window + SDL_Renderer, uploads decoded video frames to a texture, and
// translates local input events into server-space.
//
// The player is a dumb peripheral: display out, raw input in. It runs NO
// accelerometer synthesis and NO tilt rendering — Shift+drag forwards raw to
// the server, where the ENGINE synthesizes sensor events (games only ever see
// SDL_EVENT_SENSOR_UPDATE, real or synthetic) and applies presentation tilt in
// the game's own projection (🎯T94), baked into the streamed video. A player
// device with a real accelerometer forwards real sensor events instead; the
// server synth stays dormant and the physical tilt IS the presentation.
//
// No render backend — the player just blits a decoded video texture.
// This keeps mobile player builds small and avoids a render-backend port there.
#pragma once

#include <player/Linalg.h>
#include <wire/Protocol.h>
#include <player/VideoDecoder.h>

#include <SDL3/SDL.h>

#include <cstddef>
#include <cstdint>
#include <memory>
#include <vector>

namespace spyder {

class PlayerRender {
public:
    struct Config {
        // Default portrait phone-ish glass. When orientation is landscape
        // (or any-landscape), the ctor swaps to landscape before create —
        // desktop has no OS rotation, so aspect is how SessionConfig is
        // honoured on the glass.
        int initialW = 820;
        int initialH = 1180;
        bool borderless = false;   // true on iOS / Android
        uint8_t orientation = 0;   // wire::kOrientation* — 0 = no lock
        // 🎯T156.7 headless oracle: no renderer/decoder; window exists only
        // for size queries (SDL dummy video driver).
        bool headless = false;
        int accelOverride = -1;        // DeviceInfo capability: -1 auto, 0 none, 1 present
        int deviceClassOverride = -1;  // DeviceInfo deviceClass override
        // SessionConfig.immersive already applied on the glass before
        // fillDeviceInfo. Used only so ui-safe does not keep phantom
        // system-bar insets SDL still reports after bars are hidden.
        bool immersive = false;
    };

    explicit PlayerRender(const Config&);
    ~PlayerRender();

    PlayerRender(const PlayerRender&) = delete;
    PlayerRender& operator=(const PlayerRender&) = delete;

    // Open the SDL_Sensor if present (called after receiving SessionConfig
    // with kSensorAccelerometer). No sensor is fine: tilt gestures forward
    // raw to the server-side synthesizer.
    void enableAccelerometer();
    bool hasAccelerometer() const;

    // Current window/display dimensions and pixel ratio for DeviceInfo.
    // Accounts for requested orientation (portrait/landscape swap).
    void getDeviceDimensions(int& w, int& h, int& pixelRatio) const;

    // Snapshot the viewing surface into wire DeviceInfo.
    // Caller must already have applied SessionConfig (orientation,
    // immersive, …) to the glass. Safe rects are read from that surface
    // once — they are never computed before policy is known.
    void fillDeviceInfo(wire::DeviceInfo& out) const;

    // Replace the video texture with a newly decoded frame. The texture is
    // (re)allocated whenever dimensions or pixel format change. Does not
    // resize the window — the glass (window/device) owns size; the server
    // follows via DeviceInfo. SDL handles YUV→RGB for NV12/IYUV.
    void updateVideoTexture(const VideoFrame& frame);

    // 🎯T128: apply a command-stream sprite frame (MakeImage + SpriteRun).
    // Uses SDL_RenderGeometry; no full-framebuffer upload.
    struct CmdImageUpload {
        uint32_t id = 0;
        uint16_t w = 0, h = 0;
        const uint8_t* rgba = nullptr; // w*h*4 RGBA
        size_t rgbaBytes = 0;
    };
    struct CmdSpriteRunDraw {
        uint32_t imageId = 0;
        uint16_t nVerts = 0;
        const uint8_t* verts = nullptr; // SpriteVertex[]
        const float* mvp = nullptr;     // 16 floats
    };
    // contentW/H = server framebuffer aspect for letterbox (0 = use window).
    void beginCmdFrame(uint16_t contentW = 0, uint16_t contentH = 0);
    void uploadCmdImage(const CmdImageUpload&);
    void drawCmdSpriteRun(const CmdSpriteRunDraw&);
    void endCmdFrame(); // marks cmdstream mode for render()

    // Drain SDL events. Returns:
    //   quit           — SDL_EVENT_QUIT received
    //   upstreamEvents — events the caller should forward to the server
    //                    (already coordinate-mapped; forwarded raw otherwise)
    struct PumpResult {
        bool quit = false;
        bool surfaceChanged = false; // resize / orientation — re-send DeviceInfo
        // 🎯T154: wire::kLife* to forward (0 = none this pump).
        uint8_t lifecycleKind = 0;
        uint8_t lifecycleMemoryLevel = 0;
        std::vector<SDL_Event> upstreamEvents;
    };
    PumpResult pumpEvents();

    // Render a frame (clears, draws the video texture aspect-fit, presents).
    struct RenderStats {
        float drainMs = 0.f;
        float renderMs = 0.f;
    };
    RenderStats render();

    SDL_Window* window() const;

private:
    struct Impl;
    std::unique_ptr<Impl> i_;
};

} // namespace spyder

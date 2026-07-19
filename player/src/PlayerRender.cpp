// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

#include <player/AccelScreen.h>
#include <player/PlayerRender.h>
#include <wire/Protocol.h>

#include "InputPlumbing.h"  // relative-mouse + accel enumeration (delivery only)

#include "player_orientation.h"
// Engine-internal cutout query (draw-safe = cutouts only). Stub zeros on
// desktop/iOS player links; Android returns WindowInsets.Type.displayCutout().
#include "CutoutInsets.h"

#include <SDL3/SDL.h>
#include <spdlog/spdlog.h>

#if defined(__APPLE__)
#include <TargetConditionals.h>
#endif

#include <algorithm>
#include <cmath>
#include <cstring>
#include <unordered_map>
#include <vector>

using std::clamp;

namespace spyder {

struct PlayerRender::Impl {
    SDL_Window* window = nullptr;
    SDL_Renderer* renderer = nullptr;
    SDL_Texture* videoTex = nullptr;
    int texW = 0, texH = 0;
    SDL_PixelFormat texFormat = SDL_PIXELFORMAT_UNKNOWN;

    // 🎯T128 cmdstream sprite textures (image_id → SDL_Texture RGBA).
    std::unordered_map<uint32_t, SDL_Texture*> cmdTextures;
    bool cmdFramePending = false;
    uint16_t cmdContentW = 0; // server swapchain aspect (letterbox target)
    uint16_t cmdContentH = 0;
    // Last aspect-fit content rect in window pixels (for input mapping).
    float cmdVisX = 0, cmdVisY = 0, cmdVisW = 0, cmdVisH = 0;
    struct PendingRun {
        uint32_t imageId = 0;
        uint16_t nVerts = 0;
        std::vector<uint8_t> verts;
        float mvp[16]{};
    };
    std::vector<PendingRun> cmdRuns;

    uint8_t requestedOrientation = 0;
    bool immersive = false;

    // Real accelerometer, if the player device has one (phone/tablet player).
    // Its events forward upstream verbatim. There is deliberately NO synthetic
    // fallback here: the player is a dumb peripheral — tilt-gesture input
    // (Shift+drag) forwards raw to the server, where the ENGINE synthesizes
    // sensor events and applies presentation tilt (🎯T94) in the game's own
    // projection. Games only ever see SDL_EVENT_SENSOR_UPDATE, real or
    // synthetic; that contract is the engine's, not the player's.
    SDL_Sensor* accelSensor = nullptr;
    // 🎯T156.7 headless oracle mode + declared-device overrides.
    bool headless = false;
    int accelOverride = -1;
    int deviceClassOverride = -1;


    // Aspect-fit content (contentW×contentH) into window (ww×wh).
    static void fitContentRect(int ww, int wh, float contentAspect,
                               float& outX, float& outY, float& outW, float& outH) {
        if (contentAspect <= 0.f || ww <= 0 || wh <= 0) {
            outX = 0; outY = 0; outW = float(ww); outH = float(wh);
            return;
        }
        const float winAspect = float(ww) / float(wh);
        if (contentAspect > winAspect) {
            // Content wider than window → fit width, letterbox top/bottom.
            outW = float(ww);
            outH = float(ww) / contentAspect;
            outX = 0.f;
            outY = (float(wh) - outH) * 0.5f;
        } else {
            // Content taller (or equal) → fit height, pillarbox sides.
            outH = float(wh);
            outW = float(wh) * contentAspect;
            outX = (float(ww) - outW) * 0.5f;
            outY = 0.f;
        }
    }

    // Content/texture hit-testing belongs in the engine (same as direct).
    // Wire events stay device-local — no player-side coord rewrite.
};

PlayerRender::PlayerRender(const Config& cfg)
    : i_(std::make_unique<Impl>()) {
    i_->requestedOrientation = cfg.orientation;
    i_->immersive = cfg.immersive;

    // Match DirectRenderHost / SokolContext iOS window policy:
    //   * HIGH_PIXEL_DENSITY — native scale (🎯T82)
    //   * no RESIZABLE on mobile — SDL's UIKit path treats RESIZABLE as
    //     "any orientation is okay" when the orientations hint is empty;
    //     direct never sets RESIZABLE on iOS. Desktop stays RESIZABLE so a
    //     human can change the glass size (DeviceInfo follows the glass).
    //   * BORDERLESS on mobile — fullscreen glass + status-bar path
    //
    // The glass owns dimensions. We never auto-resize the window to match
    // the video stream: that inverted control (engine → glass) caused a
    // DeviceInfo ↔ encode-size feedback loop (window jiggle ~5–8%).
    //
    // SessionConfig orientation *is* glass policy (same as mobile force-
    // orientation). On desktop there is no OS rotation — apply it as the
    // initial window aspect so DeviceInfo matches the game's preference
    // without relying on a report-only w/h swap.
    int winW = cfg.initialW;
    int winH = cfg.initialH;
    const uint8_t o = cfg.orientation;
    const bool wantLandscape =
        o == wire::kOrientationLandscape ||
        o == wire::kOrientationLandscapeFlipped ||
        o == wire::kOrientationAnyLandscape;
    const bool wantPortrait =
        o == wire::kOrientationPortrait ||
        o == wire::kOrientationPortraitFlipped;
    if (wantLandscape && winW < winH) std::swap(winW, winH);
    if (wantPortrait && winH < winW) std::swap(winW, winH);

#if defined(__ANDROID__) || (defined(__APPLE__) && TARGET_OS_IOS)
    Uint32 flags = SDL_WINDOW_HIGH_PIXEL_DENSITY;
    if (cfg.borderless) flags |= SDL_WINDOW_BORDERLESS;
#else
    Uint32 flags = SDL_WINDOW_RESIZABLE | SDL_WINDOW_HIGH_PIXEL_DENSITY;
    if (cfg.borderless) flags |= SDL_WINDOW_BORDERLESS;
#endif

    i_->window = SDL_CreateWindow("Spyder Player", winW, winH, flags);
    if (!i_->window) {
        SPDLOG_ERROR("PlayerRender: SDL_CreateWindow failed: {}", SDL_GetError());
        return;
    }
    if (wantLandscape || wantPortrait) {
        SPDLOG_INFO("PlayerRender: window {}x{} for orientation={}",
                    winW, winH, static_cast<int>(o));
    }
    playerForceOrientation(i_->requestedOrientation);

    i_->headless = cfg.headless;
    i_->accelOverride = cfg.accelOverride;
    i_->deviceClassOverride = cfg.deviceClassOverride;
    if (cfg.headless) {
        SPDLOG_INFO("PlayerRender: headless — no renderer/decoder");
        return; // window (dummy driver) exists for size queries only
    }
    i_->renderer = SDL_CreateRenderer(i_->window, nullptr);
    if (!i_->renderer) {
        SPDLOG_ERROR("PlayerRender: SDL_CreateRenderer failed: {}", SDL_GetError());
    }
}

PlayerRender::~PlayerRender() {
    if (i_->accelSensor) SDL_CloseSensor(i_->accelSensor);
    for (auto& kv : i_->cmdTextures) {
        if (kv.second) SDL_DestroyTexture(kv.second);
    }
    if (i_->videoTex)    SDL_DestroyTexture(i_->videoTex);
    if (i_->renderer)    SDL_DestroyRenderer(i_->renderer);
    if (i_->window)      SDL_DestroyWindow(i_->window);
}

SDL_Window* PlayerRender::window() const { return i_->window; }

// 🎯T158: relative-mouse delivery follows the SERVER's arm state (SP2A) —
// the authority that owns AccelSynth — never a glass-side approximation of
// its policy. Delivery plumbing only; no tilt semantics on the glass.
void PlayerRender::setRelativeMouseArmed(bool armed) {
    if (i_->accelSensor) return; // real-accel glass: no synth, no rel-mouse
    setRelativeMouseForShiftDrag(i_->window, armed);
}

bool PlayerRender::hasAccelerometer() const {
    // Capability declaration for DeviceInfo (kCapHasAccelerometer): does this
    // glass have a real accelerometer at all? Enumeration only — independent
    // of whether SessionConfig asked us to open it yet. Simulator reports
    // none (realSensorAvailable() is forced false there).
    if (!realSensorAvailable()) return false;
    bool found = false;
    int count = 0;
    SDL_SensorID* sensors = SDL_GetSensors(&count);
    if (sensors) {
        for (int k = 0; k < count; k++) {
            if (SDL_GetSensorTypeForID(sensors[k]) == SDL_SENSOR_ACCEL) {
                found = true;
                break;
            }
        }
        SDL_free(sensors);
    }
    return found;
}

void PlayerRender::enableAccelerometer() {
    // Open a real sensor if the player device has one; its events forward
    // upstream. No sensor is fine — Shift+drag forwards raw to the server,
    // whose engine-side AccelSynth synthesizes (see Impl::accelSensor note).
    // iOS Simulator: Core Motion lies about availability; AccelSynth policy
    // forces Shift+drag (realSensorAvailable() is false).
    if (realSensorAvailable()) {
        int count = 0;
        SDL_SensorID* sensors = SDL_GetSensors(&count);
        if (sensors) {
            for (int k = 0; k < count; k++) {
                if (SDL_GetSensorTypeForID(sensors[k]) == SDL_SENSOR_ACCEL) {
                    i_->accelSensor = SDL_OpenSensor(sensors[k]);
                    if (i_->accelSensor) {
                        SPDLOG_INFO("PlayerRender: opened real accelerometer");
                        break;
                    }
                }
            }
            SDL_free(sensors);
        }
    }
    if (!i_->accelSensor) {
        SPDLOG_INFO("PlayerRender: no local accelerometer — tilt gestures "
                    "(Shift+drag) forward to the server's synthesizer");
    }
}

void PlayerRender::getDeviceDimensions(int& w, int& h, int& pixelRatio) const {
    // Prefer live window size (player surface); fall back to display mode.
    if (i_->window) {
        SDL_GetWindowSizeInPixels(i_->window, &w, &h);
        const float pd = SDL_GetWindowPixelDensity(i_->window);
        pixelRatio = (pd > 0.f) ? int(std::lround(pd)) : 1;
    }
    if (w <= 0 || h <= 0) {
        const SDL_DisplayMode* dm = SDL_GetCurrentDisplayMode(SDL_GetPrimaryDisplay());
        w  = dm ? dm->w : 1080;
        h  = dm ? dm->h : 2400;
        pixelRatio = (dm && dm->pixel_density > 0) ? int(dm->pixel_density) : 1;
    }

    // SessionConfig orientation is applied before DeviceInfo is measured.
    // If the OS window has not rotated yet, report the *configured* viewing
    // orientation so the server content aspect matches the glass the player
    // will present (not a transient pre-rotate portrait).
    const uint8_t o = i_->requestedOrientation;
    const bool wantPortrait =
        o == wire::kOrientationPortrait ||
        o == wire::kOrientationPortraitFlipped;
    const bool wantLandscape =
        o == wire::kOrientationLandscape ||
        o == wire::kOrientationLandscapeFlipped ||
        o == wire::kOrientationAnyLandscape;
    if (wantPortrait  && w > h) std::swap(w, h);
    if (wantLandscape && h > w) std::swap(w, h);
}

void PlayerRender::fillDeviceInfo(wire::DeviceInfo& out) const {
    int w = 0, h = 0, pr = 1;
    getDeviceDimensions(w, h, pr);
    out.width = static_cast<uint16_t>(std::clamp(w, 0, 65535));
    out.height = static_cast<uint16_t>(std::clamp(h, 0, 65535));
    out.pixelRatio = static_cast<uint16_t>(std::clamp(pr, 1, 65535));
#if defined(__ANDROID__) || (defined(__APPLE__) && TARGET_OS_IOS)
    out.deviceClass = SDL_IsTablet() ? 2 : 1; // tablet / phone
#else
    // Desktop player (and any non-mobile host): class=desktop. Note: this TU
    // is also linked into a full engine build without SPYDER_DESKTOP, so use platform macros.
    out.deviceClass = 3;
#endif
    out.orientation = i_->requestedOrientation;
    out.safeX = out.safeY = out.safeW = out.safeH = 0;
    out.drawSafeX = out.drawSafeY = out.drawSafeW = out.drawSafeH = 0;
    out.capabilities = static_cast<uint8_t>(out.capabilities | wire::kCapDualSafe);
    const bool declareAccel = (i_->accelOverride >= 0)
                                  ? (i_->accelOverride == 1)
                                  : hasAccelerometer();
    if (declareAccel)
        out.capabilities =
            static_cast<uint8_t>(out.capabilities | wire::kCapHasAccelerometer);
    if (i_->deviceClassOverride >= 0)
        out.deviceClass = static_cast<uint8_t>(i_->deviceClassOverride);

    auto toPx = [&](const SDL_Rect& r, uint16_t& ox, uint16_t& oy,
                    uint16_t& ow, uint16_t& oh) {
        if (!i_->window || r.w <= 0 || r.h <= 0) return;
        float scale = SDL_GetWindowDisplayScale(i_->window);
        if (scale <= 0.f) scale = SDL_GetWindowPixelDensity(i_->window);
        if (scale <= 0.f) scale = 1.f;
        int wwPt = 0, whPt = 0, wwPx = 0, whPx = 0;
        SDL_GetWindowSize(i_->window, &wwPt, &whPt);
        SDL_GetWindowSizeInPixels(i_->window, &wwPx, &whPx);
        const bool looksLikePoints =
            wwPt > 0 && whPt > 0 &&
            r.w <= wwPt + 2 && r.h <= whPt + 2 &&
            (wwPx > wwPt || whPx > whPt);
        const float mul = looksLikePoints ? scale : 1.f;
        ox = static_cast<uint16_t>(std::clamp(int(r.x * mul), 0, 65535));
        oy = static_cast<uint16_t>(std::clamp(int(r.y * mul), 0, 65535));
        ow = static_cast<uint16_t>(std::clamp(int(r.w * mul), 0, 65535));
        oh = static_cast<uint16_t>(std::clamp(int(r.h * mul), 0, 65535));
    };

    if (!i_->window) return;

    // Dual contract (same as DirectRenderHost):
    //   draw-safe (drawSafe*) — display cutouts only
    //   ui-safe  (safe*)      — cutouts + system bars + gestures (when bars
    //                           are actually part of the surface)
    //
    // Pixel with no camera cutout → drawSafe == full always.
    // Never copy ui-safe into draw-safe.

    // draw-safe: full, then shrink by cutout px only.
    out.drawSafeX = 0;
    out.drawSafeY = 0;
    out.drawSafeW = out.width;
    out.drawSafeH = out.height;
    const SafeAreaInsets cut = queryDisplayCutoutInsets(); // px
    if (cut.x0 > 0.f || cut.y0 > 0.f || cut.x1 > 0.f || cut.y1 > 0.f) {
        const int l = std::max(0, int(std::lround(cut.x0)));
        const int t = std::max(0, int(std::lround(cut.y0)));
        const int r = std::max(0, int(std::lround(cut.x1)));
        const int b = std::max(0, int(std::lround(cut.y1)));
        const int dw = std::max(0, int(out.width)  - l - r);
        const int dh = std::max(0, int(out.height) - t - b);
        out.drawSafeX = static_cast<uint16_t>(std::clamp(l, 0, 65535));
        out.drawSafeY = static_cast<uint16_t>(std::clamp(t, 0, 65535));
        out.drawSafeW = static_cast<uint16_t>(std::clamp(dw, 0, 65535));
        out.drawSafeH = static_cast<uint16_t>(std::clamp(dh, 0, 65535));
    }

    // ui-safe: after immersive, system bars are not on the configured
    // surface. SDL_GetWindowSafeArea often still reports pre-hide bar
    // insets (status bar ~72px) — that would push HUD/title down into a
    // gap where no bar exists. SessionConfig.immersive already applied
    // ⇒ ui-safe collapses to draw-safe (cutouts only).
    if (i_->immersive) {
        out.safeX = out.drawSafeX;
        out.safeY = out.drawSafeY;
        out.safeW = out.drawSafeW;
        out.safeH = out.drawSafeH;
    } else {
        SDL_Rect ui{};
        if (SDL_GetWindowSafeArea(i_->window, &ui) && ui.w > 0 && ui.h > 0) {
            toPx(ui, out.safeX, out.safeY, out.safeW, out.safeH);
        }
    }
}

void PlayerRender::updateVideoTexture(const VideoFrame& frame) {
    if (!i_->renderer) return;

    SDL_PixelFormat sdlFormat = SDL_PIXELFORMAT_UNKNOWN;
    switch (frame.format) {
    case VideoFrame::Format::BGRA: sdlFormat = SDL_PIXELFORMAT_BGRA32; break;
    case VideoFrame::Format::NV12: sdlFormat = SDL_PIXELFORMAT_NV12;   break;
    case VideoFrame::Format::IYUV: sdlFormat = SDL_PIXELFORMAT_IYUV;   break;
    }
    if (sdlFormat == SDL_PIXELFORMAT_UNKNOWN) return;

    if (!i_->videoTex || i_->texW != frame.width ||
        i_->texH != frame.height || i_->texFormat != sdlFormat) {
        if (i_->videoTex) SDL_DestroyTexture(i_->videoTex);
        i_->videoTex = SDL_CreateTexture(i_->renderer, sdlFormat,
            SDL_TEXTUREACCESS_STREAMING, frame.width, frame.height);
        i_->texW = frame.width;
        i_->texH = frame.height;
        i_->texFormat = sdlFormat;
        SPDLOG_INFO("PlayerRender: video texture created {}x{} format={}",
                    frame.width, frame.height, SDL_GetPixelFormatName(sdlFormat));
        // Do not resize the window to the stream: glass size is authoritative
        // (DeviceInfo); server matches content aspect to the viewer.
    }

    switch (frame.format) {
    case VideoFrame::Format::BGRA:
        SDL_UpdateTexture(i_->videoTex, nullptr, frame.planes[0], frame.strides[0]);
        break;
    case VideoFrame::Format::NV12:
        // Y plane + interleaved UV plane.
        SDL_UpdateNVTexture(i_->videoTex, nullptr,
                            frame.planes[0], frame.strides[0],
                            frame.planes[1], frame.strides[1]);
        break;
    case VideoFrame::Format::IYUV:
        // Separate Y, U, V planes.
        SDL_UpdateYUVTexture(i_->videoTex, nullptr,
                             frame.planes[0], frame.strides[0],
                             frame.planes[1], frame.strides[1],
                             frame.planes[2], frame.strides[2]);
        break;
    }
}

void PlayerRender::beginCmdFrame(uint16_t contentW, uint16_t contentH) {
    i_->cmdRuns.clear();
    i_->cmdFramePending = false;
    if (contentW > 0 && contentH > 0) {
        i_->cmdContentW = contentW;
        i_->cmdContentH = contentH;
    }
}

void PlayerRender::uploadCmdImage(const CmdImageUpload& img) {
    if (!i_->renderer || !img.rgba || img.w == 0 || img.h == 0) return;
    auto it = i_->cmdTextures.find(img.id);
    if (it != i_->cmdTextures.end() && it->second) {
        SDL_DestroyTexture(it->second);
        it->second = nullptr;
    }
    SDL_Texture* tex = SDL_CreateTexture(i_->renderer, SDL_PIXELFORMAT_RGBA32,
                                         SDL_TEXTUREACCESS_STATIC, img.w, img.h);
    if (!tex) {
        SPDLOG_ERROR("PlayerRender: cmd image texture failed: {}", SDL_GetError());
        return;
    }
    SDL_SetTextureBlendMode(tex, SDL_BLENDMODE_BLEND);
    SDL_UpdateTexture(tex, nullptr, img.rgba, img.w * 4);
    i_->cmdTextures[img.id] = tex;
    SPDLOG_INFO("PlayerRender: cmdstream image id={} {}x{}", img.id, img.w, img.h);
}

void PlayerRender::drawCmdSpriteRun(const CmdSpriteRunDraw& run) {
    if (!run.verts || !run.mvp || run.nVerts < 3) return;
    Impl::PendingRun pr;
    pr.imageId = run.imageId;
    pr.nVerts = run.nVerts;
    pr.verts.assign(run.verts,
                    run.verts + size_t(run.nVerts) * 24);
    std::memcpy(pr.mvp, run.mvp, sizeof(pr.mvp));
    i_->cmdRuns.push_back(std::move(pr));
    i_->cmdFramePending = true;
}

void PlayerRender::endCmdFrame() {
    // render() consumes cmdRuns when cmdFramePending.
}

PlayerRender::PumpResult PlayerRender::pumpEvents() {
    PumpResult r;
    SDL_Event e;
    SDL_Event lastMotion{};
    bool hasMotion = false;
    while (SDL_PollEvent(&e)) {
        if (e.type == SDL_EVENT_QUIT) { r.quit = true; continue; }

        switch (e.type) {
        case SDL_EVENT_WINDOW_PIXEL_SIZE_CHANGED:
        case SDL_EVENT_WINDOW_RESIZED:
        case SDL_EVENT_WINDOW_SAFE_AREA_CHANGED:
        case SDL_EVENT_DISPLAY_ORIENTATION:
            r.surfaceChanged = true;
            break;
        case SDL_EVENT_DID_ENTER_BACKGROUND:
            r.lifecycleKind = wire::kLifeBackground;
            break;
        case SDL_EVENT_DID_ENTER_FOREGROUND:
            r.lifecycleKind = wire::kLifeForeground;
            break;
        case SDL_EVENT_LOW_MEMORY:
            r.lifecycleKind = wire::kLifeMemory;
            r.lifecycleMemoryLevel = 2; // Critical
            break;
        case SDL_EVENT_MOUSE_MOTION:
            // One physical touch → one feed (finger). Drop synthetic mouse.
            if (isTouchSyntheticMouse(e)) break;
            // Coalesce to one motion per pump, but SUM the relative deltas —
            // the engine AccelSynth accumulates xrel/yrel, so dropping
            // intermediate deltas would under-rotate the tilt.
            if (hasMotion && lastMotion.type == SDL_EVENT_MOUSE_MOTION) {
                e.motion.xrel += lastMotion.motion.xrel;
                e.motion.yrel += lastMotion.motion.yrel;
            }
            lastMotion = e;
            hasMotion = true;
            break;
        case SDL_EVENT_FINGER_MOTION:
            // Device-local normalized coords (no content remap). Coalesce
            // finger motion separately from mouse so both kinds do not merge.
            if (hasMotion && lastMotion.type == SDL_EVENT_FINGER_MOTION) {
                e.tfinger.dx += lastMotion.tfinger.dx;
                e.tfinger.dy += lastMotion.tfinger.dy;
                // Keep latest abs position; summed dx/dy carry the gesture.
            }
            lastMotion = e;
            hasMotion = true;
            break;
        case SDL_EVENT_MOUSE_BUTTON_DOWN:
        case SDL_EVENT_MOUSE_BUTTON_UP:
            if (isTouchSyntheticMouse(e)) break;
            if (e.button.button == SDL_BUTTON_LEFT) {
            }
            r.upstreamEvents.push_back(e);
            break;
        case SDL_EVENT_MOUSE_WHEEL:
            r.upstreamEvents.push_back(e);
            break;
        case SDL_EVENT_FINGER_DOWN:
        case SDL_EVENT_FINGER_UP:
            r.upstreamEvents.push_back(e);
            break;
        case SDL_EVENT_KEY_DOWN:
        case SDL_EVENT_KEY_UP:
            // Device-local keys only. Relative-mouse is host I/O so desktop
            // can emit motion; iOS is a no-op (see AccelSynth.h).
            if (e.key.scancode == SDL_SCANCODE_LSHIFT ||
                e.key.scancode == SDL_SCANCODE_RSHIFT ||
                e.key.key == SDLK_LSHIFT || e.key.key == SDLK_RSHIFT) {
            }
            r.upstreamEvents.push_back(e);
            break;
        case SDL_EVENT_SENSOR_UPDATE: {
            // A backend with no real data source can deliver non-finite
            // samples (observed: Emscripten devicemotion with no feed →
            // NaN, which detonates server-side physics). Never forward.
            if (!std::isfinite(e.sensor.data[0]) ||
                !std::isfinite(e.sensor.data[1]) ||
                !std::isfinite(e.sensor.data[2])) {
                break;
            }
            // Match DirectRenderHost: hardware frame → screen frame before
            // the game sees the sample. Server must not re-rotate (its host
            // orientation is the Mac streaming box, not this device).
            SDL_DisplayOrientation o = SDL_ORIENTATION_UNKNOWN;
            if (SDL_DisplayID disp = SDL_GetDisplayForWindow(i_->window)) {
                o = SDL_GetCurrentDisplayOrientation(disp);
            }
            spyder::rotateAccelToScreen(o, e.sensor.data);
            r.upstreamEvents.push_back(e);
            break;
        }
        }
    }
    if (hasMotion) r.upstreamEvents.push_back(lastMotion);
    return r;
}

PlayerRender::RenderStats PlayerRender::render() {
    RenderStats s;
    if (!i_->renderer) return s;

    const uint64_t tDrainStart = SDL_GetPerformanceCounter();

    SDL_SetRenderDrawColor(i_->renderer, 0, 0, 0, 255);
    SDL_RenderClear(i_->renderer);

    int ww, wh;
    SDL_GetWindowSizeInPixels(i_->window, &ww, &wh);

    if (i_->cmdFramePending && !i_->cmdRuns.empty()) {
        // Sprite runs: world verts × mvp → NDC → aspect-fit content rect.
        // Server MVP is built for contentW×contentH; stretching NDC to the
        // full portrait window double-squashes landscape content — letterbox.
        float contentAspect = 0.f;
        if (i_->cmdContentW > 0 && i_->cmdContentH > 0)
            contentAspect = float(i_->cmdContentW) / float(i_->cmdContentH);
        float vx, vy, vw, vh;
        Impl::fitContentRect(ww, wh, contentAspect, vx, vy, vw, vh);
        i_->cmdVisX = vx; i_->cmdVisY = vy; i_->cmdVisW = vw; i_->cmdVisH = vh;

        auto xform = [](const float m[16], float x, float y, float& ox, float& oy) {
            const float X = m[0] * x + m[4] * y + m[12];
            const float Y = m[1] * x + m[5] * y + m[13];
            const float W = m[3] * x + m[7] * y + m[15];
            const float inv = (std::fabs(W) > 1e-8f) ? (1.f / W) : 1.f;
            ox = X * inv;
            oy = Y * inv;
        };
        for (const auto& run : i_->cmdRuns) {
            auto it = i_->cmdTextures.find(run.imageId);
            if (it == i_->cmdTextures.end() || !it->second) continue;
            SDL_Texture* tex = it->second;
            const size_t n = run.nVerts;
            if (n < 3 || run.verts.size() < n * 24) continue;
            std::vector<SDL_Vertex> sdlVerts(n);
            for (size_t vi = 0; vi < n; ++vi) {
                const uint8_t* p = run.verts.data() + vi * 24;
                float x, y, z, u, v;
                uint32_t abgr;
                std::memcpy(&x, p + 0, 4);
                std::memcpy(&y, p + 4, 4);
                std::memcpy(&z, p + 8, 4);
                std::memcpy(&u, p + 12, 4);
                std::memcpy(&v, p + 16, 4);
                std::memcpy(&abgr, p + 20, 4);
                (void)z;
                float ndcX, ndcY;
                xform(run.mvp, x, y, ndcX, ndcY);
                // NDC [-1,1] → letterboxed content rect (y flip: Metal NDC y-up).
                sdlVerts[vi].position.x = vx + (ndcX * 0.5f + 0.5f) * vw;
                sdlVerts[vi].position.y = vy + (1.f - (ndcY * 0.5f + 0.5f)) * vh;
                sdlVerts[vi].tex_coord.x = u;
                sdlVerts[vi].tex_coord.y = v;
                // abgr → RGBA for SDL_FColor (0..1)
                const float a = float((abgr >> 24) & 0xff) / 255.f;
                const float b = float((abgr >> 16) & 0xff) / 255.f;
                const float g = float((abgr >> 8) & 0xff) / 255.f;
                const float r = float(abgr & 0xff) / 255.f;
                sdlVerts[vi].color = SDL_FColor{r, g, b, a};
            }
            SDL_RenderGeometry(i_->renderer, tex, sdlVerts.data(),
                               int(n), nullptr, 0);
        }
        // Keep last cmd frame for redraw when no new packet this tick.
    } else if (i_->videoTex) {
        const bool needsRotation = (ww > wh) && (i_->texH > i_->texW);

        if (needsRotation) {
            const float scale = std::min(float(ww) / float(i_->texH),
                                         float(wh) / float(i_->texW));
            const float dstW = i_->texW * scale;
            const float dstH = i_->texH * scale;
            SDL_FRect dst{ (ww - dstW) * 0.5f, (wh - dstH) * 0.5f,
                           dstW, dstH };
            SDL_RenderTextureRotated(i_->renderer, i_->videoTex,
                                     nullptr, &dst,
                                     -90.0, nullptr, SDL_FLIP_NONE);
        } else {
            const float scale = std::min(float(ww) / float(i_->texW),
                                         float(wh) / float(i_->texH));
            const float visW = i_->texW * scale;
            const float visH = i_->texH * scale;
            SDL_FRect dst{ (ww - visW) * 0.5f, (wh - visH) * 0.5f,
                           visW, visH };
            SDL_RenderTexture(i_->renderer, i_->videoTex, nullptr, &dst);
        }
    }

    const uint64_t tPresentStart = SDL_GetPerformanceCounter();
    SDL_RenderPresent(i_->renderer);
    const uint64_t tEnd = SDL_GetPerformanceCounter();
    const uint64_t freq = SDL_GetPerformanceFrequency();
    s.drainMs  = float(tPresentStart - tDrainStart) * 1000.f / float(freq);
    s.renderMs = float(tEnd - tPresentStart) * 1000.f / float(freq);
    return s;
}

} // namespace spyder

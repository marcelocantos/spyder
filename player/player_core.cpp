// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// Thin glue: owns the main loop and wires PlayerWireBridge ↔ PlayerRender.
// All wire concerns live in PlayerWireBridge (bridge/); all SDL / rendering
// concerns live in PlayerRender (render/). This file has no knowledge of
// H.264, sockets, or SDL windowing.

#include "player_core.h"
#include "src/InputScript.h"
#include <optional>
#include "player_orientation.h"

#include <player/FrameLog.h>
#include <player/PlayerRender.h>
#include <player/PlayerWireBridge.h>
#include <wire/Protocol.h>
#include <player/Signal.h>
#include <player/StreamHostPolicy.h>
#include <sqlpipe.h>

// Immersive policy applied on the glass (status bar hide). Desktop stub.
#include "Immersive.h"

#include <SDL3/SDL.h>
#include <spdlog/spdlog.h>

#ifdef __EMSCRIPTEN__
#include <emscripten/emscripten.h>
#include <emscripten/em_js.h>

// Yield one display frame: requestAnimationFrame when the page is visible
// (vsync pacing), with a timeout fallback so hidden pages — where RAF never
// fires — still drain the wire a few times a second. setTimeout alone is
// NOT usable as the pacer: WebKit throttles it to 1 s for inactive pages,
// which ran the whole main loop at 0.3 Hz.
EM_ASYNC_JS(void, spyderWebYield, (), {
    await new Promise((resolve) => {
        const t = setTimeout(resolve, 250);
        requestAnimationFrame(() => { clearTimeout(t); resolve(); });
    });
});
#endif

#include <chrono>
#ifndef __EMSCRIPTEN__
#include <condition_variable>
#include <mutex>
#include <thread>
#endif
#include <cstdint>
#include <filesystem>
#include <fstream>
#include <memory>
#include <vector>

int playerCore(const std::string& host, int port, const std::string& serverName,
               const PlayerOptions& opts) {
    // 🎯T156.7 headless oracle: SDL dummy video driver — full wire bridge,
    // window object for size queries, no display.
    if (opts.headless) SDL_SetHint(SDL_HINT_VIDEO_DRIVER, "dummy");
    spyder::installSignalHandlers();
    spyder::installCrashHandlers();
    SPDLOG_INFO("spyder player starting (H.264 + SP2S cmdstream)");

    // No synthetic mouse/touch events — each input source stays native.
    SDL_SetHint(SDL_HINT_TOUCH_MOUSE_EVENTS, "0");
    SDL_SetHint(SDL_HINT_MOUSE_TOUCH_EVENTS, "0");
    if (!SDL_Init(SDL_INIT_VIDEO | SDL_INIT_SENSOR)) {
        SPDLOG_ERROR("SDL_Init failed: {}", SDL_GetError());
        return 1;
    }

    // ── Handshake (🎯T154) — init is seed; discovery is derived ──
    //
    //   SessionHostConfig in spyder::run   fixed constants at process start
    //   server → SessionConfig         first app payload on the wire
    //   player applies that policy     orientation / immersive / sensors / …
    //   player → DeviceInfo            measured once from the configured surface
    //
    // Safe rects are not known before SessionConfig, and are not “updated”
    // after it. They are computed for the first time after policy is on glass.
    spyder::PlayerWireBridge wire({host, port, serverName});
    wire::SessionConfig cfg{};
    if (!wire.connect(cfg)) {
        SDL_Quit();
        return 1;
    }
    SPDLOG_INFO("SessionConfig: sensors={:#x} orientation={} transport={} flags={:#x}",
                cfg.sensors, cfg.orientation, cfg.transport, cfg.flags);

    const bool immersive = (cfg.flags & wire::kSessionFlagImmersive) != 0;
    const bool noSaver   = (cfg.flags & wire::kSessionFlagNoScreenSaver) != 0;

    // Same orientation surface as direct: narrow SDL's set before the window
    // exists (DirectRenderHost / SessionHost path). kOrientationAnyLandscape
    // must not fall through as a no-op — TiltBuggy uses it.
    if (cfg.orientation != 0) {
        const char* hint = nullptr;
        switch (cfg.orientation) {
        case wire::kOrientationLandscape:        hint = "LandscapeLeft"; break;
        case wire::kOrientationLandscapeFlipped: hint = "LandscapeRight"; break;
        case wire::kOrientationPortrait:         hint = "Portrait"; break;
        case wire::kOrientationPortraitFlipped:  hint = "PortraitUpsideDown"; break;
        case wire::kOrientationAnyLandscape:     hint = "LandscapeLeft LandscapeRight"; break;
        }
        if (hint) SDL_SetHint(SDL_HINT_ORIENTATIONS, hint);
    }

    spyder::PlayerRender::Config rc;
#ifndef SPYDER_DESKTOP
    rc.borderless = true;
#endif
    rc.orientation = cfg.orientation;
    rc.immersive = immersive;  // discovery: ui-safe after bars are applied
    rc.headless = opts.headless;
    rc.accelOverride = opts.accelOverride;
    rc.deviceClassOverride = opts.deviceClassOverride;
    if (opts.initialW > 0 && opts.initialH > 0) {
        rc.initialW = opts.initialW;
        rc.initialH = opts.initialH;
    }
    spyder::PlayerRender render(rc);
    // PlayerRender ctor already called playerForceOrientation (shared with
    // DirectRenderHost::send). Re-apply after glass policy so scenes/VCs that
    // attach during CreateWindow / immersive still get the lock + geometry
    // update — same API, same timing class as "policy on settled glass".

    // Apply the rest of SessionConfig now that a window/activity exists.
    // Blocking applyImmersive waits for the OS layout/insets pass so the
    // subsequent DeviceInfo read sees the configured surface.
    if (immersive) spyder::applyImmersive(true);
    if (noSaver) SDL_DisableScreenSaver();
    if (cfg.sensors & wire::kSensorAccelerometer) render.enableAccelerometer();

    // Direct: playerForceOrientation from DirectRenderHost::send after host
    // is up. Stream: same call after immersive so DeviceInfo is measured on
    // the locked orientation's glass (not a transient pre-rotate portrait).
    if (cfg.orientation != 0)
        playerForceOrientation(cfg.orientation);

    // Wait for the OS to apply the locked orientation to the SDL window.
    // fillDeviceInfo can *report* landscape before the window is landscape
    // (swap heuristic), which makes the server render landscape into a still-
    // portrait glass → letterbox. Pump until the pixel size matches the
    // lock (or a short timeout). Same settle idea as immersive's second kick.
    // Desktop: PlayerRender already created with the preferred aspect; this
    // loop is mainly for mobile OS rotation settle.
    if (cfg.orientation != 0) {
        const bool wantLandscape =
            cfg.orientation == wire::kOrientationLandscape ||
            cfg.orientation == wire::kOrientationLandscapeFlipped ||
            cfg.orientation == wire::kOrientationAnyLandscape;
        const bool wantPortrait =
            cfg.orientation == wire::kOrientationPortrait ||
            cfg.orientation == wire::kOrientationPortraitFlipped;
        for (int i = 0; i < 40; ++i) {
            int w = 0, h = 0, pr = 1;
            // Raw window size — bypass DeviceInfo's swap heuristic.
            if (SDL_Window* win = render.window()) {
                SDL_GetWindowSizeInPixels(win, &w, &h);
                (void)pr;
            }
            const bool ok =
                (wantLandscape && w > h) || (wantPortrait && h > w) ||
                (!wantLandscape && !wantPortrait);
            if (ok && w > 0 && h > 0) {
                SPDLOG_INFO("orientation settle: window {}x{} after {}ms",
                            w, h, i * 25);
                break;
            }
            SDL_PumpEvents();
            SDL_Delay(25);
            if (i == 10 || i == 20) playerForceOrientation(cfg.orientation);
#ifdef SPYDER_DESKTOP
            // Desktop has no UIKit rotation — enforce aspect if still wrong.
            if (i == 0 || i == 15) {
                if (SDL_Window* win = render.window()) {
                    int cw = 0, ch = 0;
                    SDL_GetWindowSize(win, &cw, &ch);
                    if (wantLandscape && cw < ch) {
                        SDL_SetWindowSize(win, ch, cw);
                        SPDLOG_INFO("desktop orientation: resized to {}x{}", ch, cw);
                    } else if (wantPortrait && ch < cw) {
                        SDL_SetWindowSize(win, ch, cw);
                        SPDLOG_INFO("desktop orientation: resized to {}x{}", ch, cw);
                    }
                }
            }
#endif
        }
    }

    {
        wire::DeviceInfo di{};
        render.fillDeviceInfo(di);
        wire.sendDeviceInfo(di);
        SPDLOG_INFO("DeviceInfo {}x{} @{}x class={} safe=({},{} {}x{}) "
                    "draw=({},{} {}x{})",
                    di.width, di.height, di.pixelRatio, di.deviceClass,
                    di.safeX, di.safeY, di.safeW, di.safeH,
                    di.drawSafeX, di.drawSafeY, di.drawSafeW, di.drawSafeH);
    }

    // 🎯T154 SP2T: player-authoritative durable db, one file per game
    // (server catalogue name). Connecting to different games does not share
    // state. Layout: <PrefPath squz/spyder-player>/games/<name>.db
    // Fail soft: an uncaught sqlpipe::Error here used to std::terminate the
    // process immediately after DeviceInfo (iOS: attach→detach in ~40ms with
    // zero SP2S frames). Keep the glass alive on :memory: instead.
    std::string playerDbPath = ":memory:";
#ifndef __EMSCRIPTEN__
    // 🎯T101.7 defers browser durable state (OPFS/IDBFS); web runs :memory:.
    if (char* pref = SDL_GetPrefPath("squz", "spyder-player")) {
        playerDbPath = spyder::durableDbPathForPlayer(serverName.c_str(), pref);
        if (playerDbPath != ":memory:") {
            std::error_code ec;
            std::filesystem::create_directories(
                std::filesystem::path(playerDbPath).parent_path(), ec);
            if (ec) {
                SPDLOG_WARN("SP2T: could not create games dir for {}: {} — "
                            "falling back to :memory:",
                            playerDbPath, ec.message());
                playerDbPath = ":memory:";
            }
        }
        SDL_free(pref);
    }
#endif
    std::unique_ptr<sqlpipe::Database> playerDb;
    try {
        playerDb = std::make_unique<sqlpipe::Database>(playerDbPath);
        SPDLOG_INFO("SP2T: opened durable db {}", playerDbPath);
    } catch (const std::exception& ex) {
        SPDLOG_ERROR("SP2T: open {} failed: {} — falling back to :memory:",
                     playerDbPath, ex.what());
        playerDbPath = ":memory:";
        try {
            playerDb = std::make_unique<sqlpipe::Database>(playerDbPath);
        } catch (const std::exception& ex2) {
            SPDLOG_ERROR("SP2T: :memory: open also failed: {} — continuing "
                         "without durable db",
                         ex2.what());
        }
    }
    if (playerDb) {
        // Only seed when the durable file already has user tables — a fresh
        // empty serialize would clobber the server's schemaDdl on attach.
        try {
            if (spyder::sqliteHasUserTables(playerDb->handle())) {
                std::vector<uint8_t> dump;
                if (spyder::dumpSqliteMain(playerDb->handle(), dump) &&
                    !dump.empty()) {
                    wire.sendSp2tSnapshot(dump);
                    SPDLOG_INFO("SP2T: seeded server from player durable db {} "
                                "({} bytes, game={})",
                                playerDbPath, dump.size(), serverName);
                }
            } else {
                SPDLOG_INFO("SP2T: no prior durable state for game '{}' ({})",
                            serverName, playerDbPath);
            }
        } catch (const std::exception& ex) {
            SPDLOG_WARN("SP2T: seed skipped: {}", ex.what());
        }
    }
    SPDLOG_INFO("player: entering main loop (wire open={})", wire.isOpen());

    // Mid-session orientation/resize only — re-measure the live surface.
    auto sendViewerDiscovery = [&] {
        wire::DeviceInfo di{};
        render.fillDeviceInfo(di);
        wire.sendDeviceInfo(di);
    };
    // 🎯T156.3 seat promotion: the server re-sends SessionConfig when this
    // player becomes the primary seat; answer with DeviceInfo, same as the
    // connect handshake, so the seat's authority (incl. accelerometer
    // capability) is re-established.
    wire.setOnSessionConfigUpdate([&](const wire::SessionConfig&) {
        SPDLOG_INFO("player: SessionConfig mid-session — re-sending DeviceInfo");
        sendViewerDiscovery();
    });
    struct PlayerFrame { uint64_t timestamp; int decoded; uint32_t lastSeq; float drainMs; float renderMs; float pumpMs; float evMs; float upMs; };
    static FrameLog<PlayerFrame> playerLog(
        [](const std::vector<PlayerFrame>& frames, uint64_t freq) {
            int total = 0, empty = 0, gaps = 0;
            uint32_t prev = 0, minSeq = UINT32_MAX, maxSeq = 0;
            float maxDrain = 0, maxRender = 0, maxGap = 0, maxPump = 0, sumPump = 0;
            float maxEv = 0, sumEv = 0, maxUp = 0, sumUp = 0;
            for (size_t i = 0; i < frames.size(); i++) {
                auto& f = frames[i];
                total += f.decoded;
                if (f.decoded == 0) empty++;
                maxDrain  = std::max(maxDrain,  f.drainMs);
                maxRender = std::max(maxRender, f.renderMs);
                maxPump   = std::max(maxPump,   f.pumpMs);
                sumPump  += f.pumpMs;
                maxEv     = std::max(maxEv,     f.evMs);
                sumEv    += f.evMs;
                maxUp     = std::max(maxUp,     f.upMs);
                sumUp    += f.upMs;
                if (i > 0) {
                    float g = float(frames[i].timestamp - frames[i-1].timestamp)
                            * 1000.f / float(freq);
                    maxGap = std::max(maxGap, g);
                }
                if (f.decoded > 0) {
                    if (prev && f.lastSeq > prev + 1) gaps += f.lastSeq - prev - 1;
                    prev = f.lastSeq;
                    minSeq = std::min(minSeq, f.lastSeq);
                    maxSeq = std::max(maxSeq, f.lastSeq);
                }
            }
            SPDLOG_INFO("PlayerLog: {} ticks, {} decoded ({} empty), seq {}-{} ({} gaps), "
                        "maxDrain={:.1f}ms maxRender={:.1f}ms maxGap={:.1f}ms "
                        "pump avg={:.1f}/max={:.1f}ms ev avg={:.1f}/max={:.1f}ms "
                        "upload avg={:.1f}/max={:.1f}ms",
                        frames.size(), total, empty, minSeq, maxSeq, gaps,
                        maxDrain, maxRender, maxGap,
                        frames.empty() ? 0.f : sumPump / frames.size(), maxPump,
                        frames.empty() ? 0.f : sumEv / frames.size(), maxEv,
                        frames.empty() ? 0.f : sumUp / frames.size(), maxUp);
        });

    // 🎯T156.7 scripted input + trace. Scripted events are injected at the
    // same upstream boundary as pumped device input (wire.sendEvent — the
    // real SP2I marshalling path); the trace records what this glass sent
    // and received, wall-clocked for latency measurement.
    std::optional<ge::InputScriptPlayer> script;
    if (!opts.scriptPath.empty()) {
        std::vector<ge::ScriptedEvent> evs;
        if (ge::loadInputScript(opts.scriptPath, evs)) {
            script.emplace(std::move(evs));
            SPDLOG_INFO("player: input script loaded ({})", opts.scriptPath);
        } else {
            SPDLOG_ERROR("player: input script open failed ({})", opts.scriptPath);
            return 3;
        }
    }
    std::ofstream trace;
    if (!opts.tracePath.empty()) {
        trace.open(opts.tracePath, std::ios::trunc);
        if (trace.good()) {
            wire::DeviceInfo tdi{};
            render.fillDeviceInfo(tdi);
            trace << "{\"k\":\"device\",\"t_ms\":" << SDL_GetTicks()
                  << ",\"class\":" << int(tdi.deviceClass)
                  << ",\"caps\":" << int(tdi.capabilities) << "}\n";
            trace.flush();
        }
    }

    // 🎯T156 PresentTrace: per-present (time, server seq) — the ground
    // truth for display cadence on the glass. Summarized every ~2 s into
    // one log line (per-line logging at 60 Hz is itself a stutter source).
    uint32_t ptLastSeq = 0;
    uint64_t ptLastUs = 0;
    int ptN = 0, ptSmooth = 0, ptSkips = 0, ptHolds = 0;
    double ptMaxDtMs = 0;
    double ptLatSumMs = 0, ptLatMaxMs = 0;
    int ptLatN = 0;

    // 🎯T156: SP2T durable writes happen OFF the render thread. The inline
    // Documents/ write (fopen/trunc/write at 2 Hz) blocked the loop 30-60 ms
    // per push on iOS flash — measured as 2 Hz hold-then-skip judder in
    // PresentTrace. Latest-wins: an unwritten older snapshot is superseded.
    // Web: no writer thread (no pthreads in the wasm leg) and no durable
    // path to write — playerDbPath is pinned to :memory: above.
#ifndef __EMSCRIPTEN__
    std::mutex sp2tMu;
    std::condition_variable sp2tCv;
    std::vector<uint8_t> sp2tPendingBlob;
    bool sp2tExit = false;
    std::thread sp2tWriter([&] {
        for (;;) {
            std::vector<uint8_t> blob;
            {
                std::unique_lock<std::mutex> lk(sp2tMu);
                sp2tCv.wait(lk, [&] {
                    return sp2tExit || !sp2tPendingBlob.empty();
                });
                if (sp2tExit && sp2tPendingBlob.empty()) return;
                blob = std::move(sp2tPendingBlob);
                sp2tPendingBlob.clear();
            }
            std::ofstream out(playerDbPath,
                              std::ios::binary | std::ios::trunc);
            if (out) {
                out.write(reinterpret_cast<const char*>(blob.data()),
                          static_cast<std::streamsize>(blob.size()));
                SPDLOG_INFO("SP2T: wrote durable snapshot {} ({} bytes, game={})",
                            playerDbPath, blob.size(), serverName);
            }
        }
    });
#endif

    uint64_t frameCount = 0;
    spyder::PlayerWireBridge::DecodedFrame decodedFrame;
    spyder::PlayerWireBridge::CmdDisplayFrame cmdFrame;
    // Present-rate meter for the ≥55 fps gate (cmdstream + video).
    uint64_t fpsWindowStart = SDL_GetPerformanceCounter();
    uint64_t fpsWindowFrames = 0;
    const uint64_t freq = SDL_GetPerformanceFrequency();

    while (!spyder::shouldQuit()) {
        const uint64_t tEv0 = SDL_GetPerformanceCounter();
        auto pump = render.pumpEvents();
        if (pump.quit) {
            SPDLOG_INFO("player: exit — SDL quit event");
            break;
        }
        if (pump.surfaceChanged) sendViewerDiscovery();
        if (pump.lifecycleKind != 0) {
            wire::ViewerLifecycle life{};
            life.kind = pump.lifecycleKind;
            life.memoryLevel = pump.lifecycleMemoryLevel;
            wire.sendLifecycle(life);
        }
        for (auto& e : pump.upstreamEvents) wire.sendEvent(e);
        // 🎯T158: server-owned arm state → relative-mouse delivery.
        if (const int arm = wire.pollArmState(); arm >= 0) {
            if (!opts.headless) render.setRelativeMouseArmed(arm == 1);
            if (trace.good()) {
                trace << "{\"k\":\"armstate\",\"t_ms\":" << SDL_GetTicks()
                      << ",\"armed\":" << arm << "}\n";
                trace.flush();
            }
        }
        if (script && !script->done()) {
            script->poll(static_cast<uint32_t>(SDL_GetTicks()),
                         [&](const SDL_Event& e) {
                             wire.sendEvent(e);
                             if (trace.good()) {
                                 using namespace std::chrono;
                                 const auto eus = duration_cast<microseconds>(
                                     system_clock::now().time_since_epoch())
                                         .count();
                                 trace << "{\"k\":\"inject\",\"t_ms\":"
                                       << SDL_GetTicks() << ",\"e_us\":" << eus
                                       << ",\"type\":" << e.type << "}\n";
                                 trace.flush();
                             }
                         });
        } else if (script && opts.exitAfterScript) {
            SPDLOG_INFO("player: script complete — exiting");
            break;
        }

        const uint64_t tPump0 = SDL_GetPerformanceCounter();
        if (!wire.pump()) {
            SPDLOG_INFO("player: exit — wire closed");
            break;
        }
        // 🎯T154 SP2T: server→player durable push (stream / detach). Write
        // immediately to the per-game PrefPath file (fire-and-forget FS).
        {
            std::vector<uint8_t> push;
            if (wire.pollSp2tSnapshot(push) && !push.empty() &&
                playerDbPath != ":memory:") {
#ifndef __EMSCRIPTEN__
                {
                    std::lock_guard<std::mutex> lk(sp2tMu);
                    sp2tPendingBlob = std::move(push); // latest wins
                }
                sp2tCv.notify_one();
#endif
            }
        }
        const uint64_t tPump1 = SDL_GetPerformanceCounter();

        bool presentedCmd = false;
        bool got = false;
        if (opts.headless) {
            // Drain and count frames; no GPU. Trace a heartbeat each frame
            // so the oracle can assert sustained delivery.
            if (wire.pollCmdFrame(cmdFrame) || wire.pollFrame(decodedFrame)) {
                frameCount++;
                fpsWindowFrames++;
                got = true;
                // 🎯T159: sampled emit→receipt latency for the oracle's
                // live tier (same host as the server ⇒ same clock).
                if (trace.good() && cmdFrame.serverUs != 0 &&
                    (frameCount % 60) == 0) {
                    using namespace std::chrono;
                    const uint64_t epochUs =
                        static_cast<uint64_t>(duration_cast<microseconds>(
                            system_clock::now().time_since_epoch()).count());
                    trace << "{\"k\":\"frame_meta\",\"seq\":"
                          << cmdFrame.seq << ",\"server_us\":"
                          << cmdFrame.serverUs << ",\"recv_us\":" << epochUs
                          << "}\n";
                    trace.flush();
                }
            }
        } else if (wire.pollCmdFrame(cmdFrame)) {
            render.beginCmdFrame(cmdFrame.contentW, cmdFrame.contentH);
            for (const auto& img : cmdFrame.images) {
                spyder::PlayerRender::CmdImageUpload u;
                u.id = img.id;
                u.w = img.w;
                u.h = img.h;
                u.rgba = img.rgba.data();
                u.rgbaBytes = img.rgba.size();
                render.uploadCmdImage(u);
            }
            for (const auto& run : cmdFrame.runs) {
                spyder::PlayerRender::CmdSpriteRunDraw d;
                d.imageId = run.imageId;
                d.nVerts = run.nVerts;
                d.verts = run.verts.data();
                d.mvp = run.mvp;
                render.drawCmdSpriteRun(d);
            }
            render.endCmdFrame();
            frameCount++;
            got = true;
            presentedCmd = true;
            fpsWindowFrames++;
        } else if (wire.pollFrame(decodedFrame)) {
            render.updateVideoTexture(decodedFrame.view());
            frameCount++;
            got = true;
            fpsWindowFrames++;
        }
        (void)got;
        const uint64_t tUp1 = SDL_GetPerformanceCounter();

        spyder::PlayerRender::RenderStats rs{};
        if (!opts.headless) rs = render.render();
        if (presentedCmd) {
            const uint64_t nowUs =
                SDL_GetPerformanceCounter() * 1000000ull / freq;
            if (ptLastUs != 0) {
                const double dtMs = double(nowUs - ptLastUs) / 1000.0;
                const uint32_t dseq = cmdFrame.seq - ptLastSeq;
                ptN++;
                if (dseq == 1) ptSmooth++;
                if (dseq >= 2) ptSkips++;
                if (dtMs > 25.0) ptHolds++;
                if (dtMs > ptMaxDtMs) ptMaxDtMs = dtMs;
                if (cmdFrame.serverUs != 0) {
                    using namespace std::chrono;
                    const uint64_t epochUs =
                        static_cast<uint64_t>(duration_cast<microseconds>(
                            system_clock::now().time_since_epoch()).count());
                    const double latMs =
                        (double(epochUs) - double(cmdFrame.serverUs)) / 1000.0;
                    ptLatSumMs += latMs;
                    ptLatN++;
                    if (latMs > ptLatMaxMs) ptLatMaxMs = latMs;
                }
                if (ptN >= 120) {
                    SPDLOG_INFO("PresentTrace: n={} smooth={} skips={} "
                                "holds>25ms={} maxDt={:.1f}ms "
                                "emitLat avg={:.1f}/max={:.1f}ms (n={})",
                                ptN, ptSmooth, ptSkips, ptHolds, ptMaxDtMs,
                                ptLatN ? ptLatSumMs / ptLatN : 0.0,
                                ptLatMaxMs, ptLatN);
                    ptN = ptSmooth = ptSkips = ptHolds = 0;
                    ptMaxDtMs = 0;
                    ptLatSumMs = ptLatMaxMs = 0;
                    ptLatN = 0;
                }
            }
            ptLastSeq = cmdFrame.seq;
            ptLastUs = nowUs;
        }
        auto stats = wire.lastPumpStats();
        const float tickHz = float(freq);
        playerLog.record({SDL_GetPerformanceCounter(),
                          stats.framesThisTick, stats.lastSeq,
                          rs.drainMs, rs.renderMs,
                          float(tPump1 - tPump0) * 1000.f / tickHz,
                          float(tPump0 - tEv0) * 1000.f / tickHz,
                          float(tUp1 - tPump1) * 1000.f / tickHz});

        // Log measured present rate every ~1 s (cmdstream / video).
        const uint64_t now = SDL_GetPerformanceCounter();
        const double winSec = double(now - fpsWindowStart) / double(freq);
        if (winSec >= 1.0) {
            const double fps = double(fpsWindowFrames) / winSec;
            SPDLOG_INFO("PlayerFPS: {:.1f} fps over {:.2f}s ({} frames) "
                        "cmdstream={} last_wire={}B last_seq={}",
                        fps, winSec, fpsWindowFrames,
                        stats.cmdstream ? 1 : 0,
                        stats.lastWireBytes, stats.lastSeq);
            fpsWindowStart = now;
            fpsWindowFrames = 0;
        }

#ifdef __EMSCRIPTEN__
        // Yield to the browser event loop each iteration (ASYNCIFY):
        // delivers WebSocket messages, input events, and display refresh.
        spyderWebYield();
#endif
    }

#ifndef __EMSCRIPTEN__
    {
        std::lock_guard<std::mutex> lk(sp2tMu);
        sp2tExit = true;
    }
    sp2tCv.notify_one();
    sp2tWriter.join();
#endif

    wire.close();
    if (trace.good()) {
        trace << "{\"k\":\"exit\",\"t_ms\":" << SDL_GetTicks()
              << ",\"frames\":" << frameCount << "}\n";
    }
    SDL_Quit();
    SPDLOG_INFO("Player exited ({} frames decoded)", frameCount);
    return 0;
}

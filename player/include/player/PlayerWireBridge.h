// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// PlayerWireBridge — the wire half of the player (brokered modality).
//
// Counterpart to ServerWireBridge on the server side. Owns the player's
// WebSocket to the stream relay (spyder), the H.264 decoder, and the AVCC parameter-set parser.
// Handles the SessionConfig → DeviceInfo handshake, buffers the latest
// decoded frame for the render subsystem to consume, and sends input
// events upstream to the server.
//
// Deliberately agnostic of SDL windowing / rendering — the player render
// subsystem (PlayerRender) owns all SDL state.
#pragma once

#include <player/Linalg.h>

#include <wire/Protocol.h>
#include <player/VideoDecoder.h>

#include <SDL3/SDL_events.h>

#include <cstddef>
#include <cstdint>
#include <memory>
#include <string>
#include <vector>

namespace spyder {

class PlayerWireBridge {
public:
    struct Config {
        std::string host;
        int port = 3030;
        std::string serverName = "server";
        int connectTimeoutMs = 2000;
    };

    // A decoded video frame ready for display. Owns its plane data so it
    // remains valid after the decoder callback returns. Plane count and
    // stride layout depend on `format` — see player/VideoDecoder.h's VideoFrame
    // for the contract; DecodedFrame is the owned counterpart.
    struct DecodedFrame {
        VideoFrame::Format format = VideoFrame::Format::BGRA;
        int width = 0;
        int height = 0;
        std::vector<uint8_t> plane0;
        std::vector<uint8_t> plane1;
        std::vector<uint8_t> plane2;
        int stride0 = 0;
        int stride1 = 0;
        int stride2 = 0;

        // View this owned frame as a VideoFrame with raw plane pointers.
        // Pointers are valid until the next mutation of *this.
        VideoFrame view() const {
            VideoFrame f;
            f.format = format;
            f.width = width;
            f.height = height;
            f.planes[0] = plane0.empty() ? nullptr : plane0.data();
            f.planes[1] = plane1.empty() ? nullptr : plane1.data();
            f.planes[2] = plane2.empty() ? nullptr : plane2.data();
            f.strides[0] = stride0;
            f.strides[1] = stride1;
            f.strides[2] = stride2;
            return f;
        }
    };

    explicit PlayerWireBridge(Config);
    ~PlayerWireBridge();

    PlayerWireBridge(const PlayerWireBridge&) = delete;
    PlayerWireBridge& operator=(const PlayerWireBridge&) = delete;

    // Connect to the stream relay (spyder) and wait for SessionConfig. Blocks until received
    // or connection fails. Fills `outConfig` on success.
    bool connect(wire::SessionConfig& outConfig);

    // Send DeviceInfo upstream. Call after connect() and after the
    // render subsystem has decided window dimensions.
    bool sendDeviceInfo(const wire::DeviceInfo&);

    // Player → server: safe-area change (orientation / chrome).
    bool sendSafeAreaUpdate(const wire::SafeAreaUpdate&);

    // Player → server: viewer lifecycle (background, back, memory, audio).
    bool sendLifecycle(const wire::ViewerLifecycle&);

    // 🎯T154 SP2T: player → server full sqlite serialize of durable main.
    bool sendSp2tSnapshot(const std::vector<uint8_t>& sqliteDump);

    // 🎯T154 SP2T: server → player durable push (poll after pump).
    bool pollSp2tSnapshot(std::vector<uint8_t>& outDump);

    // Send an SDL event (coordinate-mapped by caller) to the server.
    void sendEvent(const SDL_Event&);

    // Drain available wire messages, decoding any H.264 frames into the
    // internal frame buffer. Returns false if the connection has closed.
    bool pump();

    // If a new decoded frame is available since the last call, move it
    // into `out` and return true. Returns false if no new frame.
    bool pollFrame(DecodedFrame& out);

    // 🎯T128 command-stream display frame (sprite runs). Prefer over pollFrame
    // when transport is cmdstream — no multi-MB Present path.
    struct CmdImage {
        uint32_t id = 0;
        uint16_t w = 0, h = 0;
        std::vector<uint8_t> rgba; // RGBA8 premul
    };
    struct CmdSpriteRun {
        uint32_t imageId = 0;
        uint16_t nVerts = 0;
        std::vector<uint8_t> verts; // nVerts * 24 bytes
        float mvp[16]{};
    };
    struct CmdDisplayFrame {
        uint32_t seq = 0;
        size_t wireBytes = 0;
        // Server swapchain size (pixels) — player letterboxes to this aspect.
        uint16_t contentW = 0;
        uint16_t contentH = 0;
        std::vector<CmdImage> images; // MakeImage this frame (may be empty when warm)
        std::vector<CmdSpriteRun> runs;
        bool hasPresent = false; // legacy Present path still fills pollFrame
    };
    bool pollCmdFrame(CmdDisplayFrame& out);

    // Stats for the frame log in the main loop.
    struct PumpStats {
        int framesThisTick = 0;
        uint32_t lastSeq = 0;
        size_t lastWireBytes = 0;
        bool cmdstream = false;
    };
    PumpStats lastPumpStats() const;

    bool isOpen() const;
    void close();

private:
    struct Impl;
    std::unique_ptr<Impl> i_;
};

} // namespace spyder

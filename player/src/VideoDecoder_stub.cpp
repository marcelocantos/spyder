// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// No-op VideoDecoder for platforms without an H.264 leg (🎯T101.4: the
// browser player v1 is cmdstream-only; the WebCodecs backend is 🎯T101.5).
// PlayerWireBridge constructs the decoder lazily on the first H.264 AU, so
// a cmdstream session never reaches this code; if a server does select the
// H.264 rung, frames are dropped loudly rather than crashing the glass.

#include <player/VideoDecoder.h>

#include <spdlog/spdlog.h>

namespace spyder {

struct VideoDecoder::M {
    bool warned = false;
};

VideoDecoder::VideoDecoder(FrameCallback) : m(std::make_unique<M>()) {}

VideoDecoder::~VideoDecoder() = default;

void VideoDecoder::setParameterSets(const uint8_t*, size_t,
                                    const uint8_t*, size_t) {}

void VideoDecoder::decode(const uint8_t*, size_t) {
    if (!m->warned) {
        m->warned = true;
        SPDLOG_ERROR("VideoDecoder: no H.264 backend on this platform — "
                     "frames dropped (cmdstream-only glass)");
    }
}

void VideoDecoder::flush() {}

} // namespace spyder

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
#pragma once

#include <player/Linalg.h>

#include <cstdint>
#include <cstddef>
#include <functional>
#include <memory>

namespace spyder {

// A decoded video frame, view-only. Plane pointers belong to the decoder
// and are only valid for the duration of the FrameCallback invocation.
//
// Plane count depends on format:
//   BGRA: 1 plane.  planes[0] = BGRA pixels, strides[0] = bytesPerRow.
//   NV12: 2 planes. planes[0] = Y (w×h), planes[1] = interleaved UV (w×h/2).
//   IYUV: 3 planes. planes[0] = Y, planes[1] = U (w/2×h/2), planes[2] = V.
struct VideoFrame {
    enum class Format { BGRA, NV12, IYUV };
    Format format = Format::BGRA;
    int width = 0;
    int height = 0;
    const uint8_t* planes[3] = {nullptr, nullptr, nullptr};
    int strides[3] = {0, 0, 0};
};

class VideoDecoder {
public:
    using FrameCallback = std::function<void(const VideoFrame&)>;

    explicit VideoDecoder(FrameCallback onFrame);
    ~VideoDecoder();

    // Set SPS and PPS parameter sets (must be called before first decode).
    // Creates the decompression session from the provided parameters.
    void setParameterSets(const uint8_t* sps, size_t spsSize,
                          const uint8_t* pps, size_t ppsSize);

    // Decode a single NAL unit (Annex B format with 0x00000001 start code).
    void decode(const uint8_t* nalData, size_t nalSize);

    // Flush pending frames.
    void flush();

private:
    struct M;
    std::unique_ptr<M> m;
};

} // namespace spyder

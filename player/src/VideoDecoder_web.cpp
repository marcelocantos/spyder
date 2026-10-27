// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// Browser H.264 leg (T101.5): WebCodecs VideoDecoder, IYUV for SDL present.

#ifdef __EMSCRIPTEN__

#include <player/VideoDecoder.h>

#include <emscripten.h>
#include <emscripten/em_js.h>
#include <spdlog/spdlog.h>

#include <cstdlib>
#include <memory>

namespace spyder {

namespace {

VideoDecoder::FrameCallback* g_frameCb = nullptr;

} // namespace

extern "C" {

EMSCRIPTEN_KEEPALIVE
void spyder_wc_on_frame_iyuv(int width, int height, uintptr_t y, int sy,
                             uintptr_t u, int su, uintptr_t v, int sv) {
    if (!g_frameCb || !*g_frameCb) {
        std::free(reinterpret_cast<void*>(y));
        return;
    }
    VideoFrame f;
    f.format = VideoFrame::Format::IYUV;
    f.width = width;
    f.height = height;
    f.planes[0] = reinterpret_cast<const uint8_t*>(y);
    f.planes[1] = reinterpret_cast<const uint8_t*>(u);
    f.planes[2] = reinterpret_cast<const uint8_t*>(v);
    f.strides[0] = sy;
    f.strides[1] = su;
    f.strides[2] = sv;
    (*g_frameCb)(f);
    std::free(reinterpret_cast<void*>(y));
}

} // extern "C"

EM_JS(int, spyder_wc_supported, (), {
    return ('VideoDecoder' in globalThis) ? 1 : 0;
});

EM_JS(void, spyder_wc_shutdown, (), {
    if (!Module.spyderWC) return;
    if (Module.spyderWC.dec) {
        try { Module.spyderWC.dec.close(); } catch (e) {}
        Module.spyderWC.dec = null;
    }
    Module.spyderWC.configured = false;
});

EM_JS(int, spyder_wc_configure, (const void* spsPtr, int spsLen,
                                const void* ppsPtr, int ppsLen), {
    if (!('VideoDecoder' in globalThis)) return 0;
    const sps = HEAPU8.subarray(spsPtr, spsPtr + spsLen);
    if (spsLen < 4 || (sps[0] & 0x1f) !== 7) return 0;
    const hex = (n) => n.toString(16).padStart(2, '0');
    const codec = 'avc1.' + hex(sps[1]) + hex(sps[2]) + hex(sps[3]);

    if (!Module.spyderWC) Module.spyderWC = { ts: 0, dec: null, configured: false };
    if (Module.spyderWC.dec) {
        try { Module.spyderWC.dec.close(); } catch (e) {}
        Module.spyderWC.dec = null;
        Module.spyderWC.configured = false;
    }

    Module.spyderWC.dec = new VideoDecoder({
        output: (frame) => {
            (async () => {
                try {
                    const w = frame.displayWidth;
                    const h = frame.displayHeight;
                    if (w <= 0 || h <= 0) return;
                    const size = frame.allocationSize({ format: 'I420' });
                    const buf = new Uint8Array(size);
                    await frame.copyTo(buf, { format: 'I420' });
                    const ySize = w * h;
                    const uvSize = (w >> 1) * (h >> 1);
                    const total = ySize + uvSize + uvSize;
                    const ptr = Module._malloc(total);
                    if (!ptr) return;
                    Module.HEAPU8.set(buf.subarray(0, ySize), ptr);
                    Module.HEAPU8.set(buf.subarray(ySize, ySize + uvSize), ptr + ySize);
                    Module.HEAPU8.set(buf.subarray(ySize + uvSize, total),
                                      ptr + ySize + uvSize);
                    Module._spyder_wc_on_frame_iyuv(w, h, ptr, w, ptr + ySize, w >> 1,
                                                    ptr + ySize + uvSize, w >> 1);
                } catch (e) {
                    console.error('spyder WebCodecs output:', e);
                } finally {
                    frame.close();
                }
            })();
        },
        error: (e) => { console.error('spyder WebCodecs:', e); },
    });

    try {
        Module.spyderWC.dec.configure({ codec, optimizeForLatency: true });
        Module.spyderWC.configured = true;
        Module.spyderWC.ts = 0;
        return 1;
    } catch (e) {
        console.error('spyder WebCodecs configure:', e);
        try { Module.spyderWC.dec.close(); } catch (e2) {}
        Module.spyderWC.dec = null;
        Module.spyderWC.configured = false;
        return 0;
    }
});

EM_JS(void, spyder_wc_decode, (const void* ptr, int len), {
    if (!Module.spyderWC?.dec || !Module.spyderWC.configured || len <= 0) return;
    const data = HEAPU8.subarray(ptr, ptr + len);
    let off = 0;
    if (len >= 4 && data[0] === 0 && data[1] === 0 && data[2] === 0 && data[3] === 1)
        off = 4;
    else if (len >= 3 && data[0] === 0 && data[1] === 0 && data[2] === 1)
        off = 3;
    const nalType = off < len ? (data[off] & 0x1f) : 0;
    const key = nalType === 5;
    try {
        const chunk = new EncodedVideoChunk({
            type: key ? 'key' : 'delta',
            timestamp: Module.spyderWC.ts,
            data,
        });
        Module.spyderWC.ts += 16666;
        Module.spyderWC.dec.decode(chunk);
    } catch (e) {}
});

EM_ASYNC_JS(void, spyder_wc_flush, (), {
    if (Module.spyderWC?.dec?.state === 'configured')
        await Module.spyderWC.dec.flush();
});

struct VideoDecoder::M {
    FrameCallback callback;
    bool warned = false;
};

VideoDecoder::VideoDecoder(FrameCallback onFrame)
    : m(std::make_unique<M>()) {
    m->callback = std::move(onFrame);
    g_frameCb = &m->callback;
}

VideoDecoder::~VideoDecoder() {
    spyder_wc_shutdown();
    if (g_frameCb == &m->callback) g_frameCb = nullptr;
}

void VideoDecoder::setParameterSets(const uint8_t* sps, size_t spsSize,
                                    const uint8_t* pps, size_t ppsSize) {
    (void)pps;
    (void)ppsSize;
    if (!spyder_wc_supported()) {
        if (!m->warned) {
            m->warned = true;
            SPDLOG_ERROR("VideoDecoder: WebCodecs not available in this browser");
        }
        return;
    }
    if (!spyder_wc_configure(sps, static_cast<int>(spsSize), pps,
                             static_cast<int>(ppsSize))) {
        SPDLOG_ERROR("VideoDecoder: WebCodecs configure failed");
    } else {
        SPDLOG_INFO("VideoDecoder: WebCodecs configured (SPS {} bytes)", spsSize);
    }
}

void VideoDecoder::decode(const uint8_t* nalData, size_t nalSize) {
    if (!spyder_wc_supported()) return;
    spyder_wc_decode(nalData, static_cast<int>(nalSize));
}

void VideoDecoder::flush() {
    spyder_wc_flush();
}

} // namespace spyder

#endif // __EMSCRIPTEN__

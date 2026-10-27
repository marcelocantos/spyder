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

EM_JS(void, spyder_wc_decode_avcc, (const void* ptr, int len, int keyframe), {
    if (!('VideoDecoder' in globalThis) || len <= 0) return;
    const avcc = HEAPU8.subarray(ptr, ptr + len);
    const avccToAnnexB = (a) => {
        const out = [];
        for (let i = 0; i + 4 <= a.length;) {
            const nalLen = (a[i] << 24) | (a[i + 1] << 16) | (a[i + 2] << 8) | a[i + 3];
            i += 4;
            if (nalLen <= 0 || i + nalLen > a.length) break;
            const nalType = a[i] & 0x1f;
            if (nalType !== 6) {
                out.push(0, 0, 0, 1);
                for (let j = 0; j < nalLen; j++) out.push(a[i + j]);
            }
            i += nalLen;
        }
        return new Uint8Array(out);
    };
    const codecFromAVCC = (a) => {
        for (let i = 0; i + 4 <= a.length;) {
            const nalLen = (a[i] << 24) | (a[i + 1] << 16) | (a[i + 2] << 8) | a[i + 3];
            i += 4;
            if (i + nalLen > a.length || nalLen < 4) break;
            if ((a[i] & 0x1f) === 7) {
                const h = (n) => n.toString(16).padStart(2, '0');
                return 'avc1.' + h(a[i + 1]) + h(a[i + 2]) + h(a[i + 3]);
            }
            i += nalLen;
        }
        return null;
    };
    if (!Module.spyderWC) Module.spyderWC = { ts: 0, dec: null, configured: false };
    if (!Module.spyderWC.dec) {
        Module.spyderWC.dec = new VideoDecoder({
            output: (frame) => {
                (async () => {
                    try {
                        const w = frame.displayWidth;
                        const h = frame.displayHeight;
                        if (w <= 0 || h <= 0) return;
                        const ySize = w * h;
                        const uvSize = (w >> 1) * (h >> 1);
                        const size = frame.allocationSize();
                        const buf = new Uint8Array(size);
                        await frame.copyTo(buf, {
                            layout: [
                                { offset: 0, stride: w },
                                { offset: ySize, stride: w >> 1 },
                                { offset: ySize + uvSize, stride: w >> 1 },
                            ],
                        });
                        const total = ySize + uvSize + uvSize;
                        const fptr = Module._malloc(total);
                        if (!fptr) return;
                        HEAPU8.set(buf.subarray(0, ySize), fptr);
                        HEAPU8.set(buf.subarray(ySize, ySize + uvSize), fptr + ySize);
                        HEAPU8.set(buf.subarray(ySize + uvSize, total),
                                    fptr + ySize + uvSize);
                        globalThis.spyderTest = globalThis.spyderTest || {};
                        globalThis.spyderTest.wcFrames =
                            (globalThis.spyderTest.wcFrames || 0) + 1;
                        const onFrame = Module._spyder_wc_on_frame_iyuv;
                        if (!onFrame) {
                            globalThis.spyderTest.frameExportMissing = true;
                            Module._free(fptr);
                            return;
                        }
                        onFrame(w, h, fptr, w, fptr + ySize, w >> 1,
                                fptr + ySize + uvSize, w >> 1);
                    } catch (e) {
                        globalThis.spyderTest = globalThis.spyderTest || {};
                        globalThis.spyderTest.frameOutErr = String(e);
                        console.error('spyder WebCodecs output:', e);
                    } finally {
                        frame.close();
                    }
                })();
            },
            error: (e) => {
                globalThis.spyderTest = globalThis.spyderTest || {};
                globalThis.spyderTest.wcError = String(e);
                console.error('spyder WebCodecs:', e);
            },
        });
    }
    const key = keyframe !== 0;
    if (!Module.spyderWC.configured) {
        if (!key) return;
        const codec = codecFromAVCC(avcc);
        if (!codec) return;
        try {
            Module.spyderWC.dec.configure({ codec, optimizeForLatency: true });
            Module.spyderWC.configured = true;
            Module.spyderWC.ts = 0;
            globalThis.spyderTest = globalThis.spyderTest || {};
            globalThis.spyderTest.wcConfigured = true;
        } catch (e) {
            globalThis.spyderTest = globalThis.spyderTest || {};
            globalThis.spyderTest.wcError = String(e);
            return;
        }
    }
    try {
        globalThis.spyderTest = globalThis.spyderTest || {};
        globalThis.spyderTest.decodeCalls = (globalThis.spyderTest.decodeCalls || 0) + 1;
        const chunk = new EncodedVideoChunk({
            type: key ? 'key' : 'delta',
            timestamp: Module.spyderWC.ts,
            data: avccToAnnexB(avcc),
        });
        Module.spyderWC.ts += 16666;
        Module.spyderWC.dec.decode(chunk);
    } catch (e) {
        globalThis.spyderTest = globalThis.spyderTest || {};
        globalThis.spyderTest.decodeErr = String(e);
    }
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
    (void)sps;
    (void)spsSize;
    (void)pps;
    (void)ppsSize;
    // WebCodecs configures lazily from in-band SPS on the first key AU
    // (dashboard SP2V shape); out-of-band SPS/PPS is for VT/FFmpeg only.
}

void VideoDecoder::decodeAccessUnit(const uint8_t* avccData, size_t avccSize,
                                    bool keyframe) {
    if (!spyder_wc_supported()) return;
    spyder_wc_decode_avcc(avccData, static_cast<int>(avccSize), keyframe ? 1 : 0);
}

void VideoDecoder::decode(const uint8_t* nalData, size_t nalSize) {
    (void)nalData;
    (void)nalSize;
    // SP2V wasm path uses decodeAccessUnit (dashboard AVCC + wire keyframe).
}

void VideoDecoder::flush() {
    spyder_wc_flush();
}

} // namespace spyder

#endif // __EMSCRIPTEN__

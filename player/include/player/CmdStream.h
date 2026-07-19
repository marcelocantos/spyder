// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// Command-stream codec + content-addressed cache (🎯T128 pass-through + cache MVP).
//
// Wire envelope is still wire::MessageHeader with magic kCommandStreamMagic
// ("SP2S"). Payload is a sequence of tagged ops (see Op). Large blobs are
// content-addressed: first transmission may carry full bytes; later ops refer
// to the hash only when the peer is known (or assumed) to have the blob.
//
// Capture (server) and replay (player) are layered on top of this codec.
// Apple captures via sg_install_trace_hooks (no g_ge_sg_api on Metal); a true
// null serialising backend is dispatch-only (Android / T128.6).
#pragma once

#include <array>
#include <cstddef>
#include <cstdint>
#include <memory>
#include <span>
#include <string>
#include <string_view>
#include <unordered_map>
#include <vector>

namespace spyder::cmdstream {

// 16-byte content hash (FNV-1a 128-bit, two 64-bit lanes).
using Hash = std::array<uint8_t, 16>;

// Wire opcodes. Stable within a protocol version; unknown ops abort the reader.
enum class Op : uint16_t {
    End = 0,           // end of frame (optional; length also bounds the payload)
    Blob = 1,          // hash[16] + u32 size + size bytes (full transfer)
    BlobRef = 2,       // hash[16] only (cache hit — no body)
    // Simplified draw/resource ops for the spike. Full sokol descriptor dump
    // is a later widening; these prove pass-through + cache economics.
    MakeBuffer = 10,   // u32 id, u32 size, u32 usage, Hash data
    MakeImage = 11,    // u32 id, u16 w, u16 h, u32 pixel_format, Hash pixels
    UpdateBuffer = 12, // u32 id, Hash data
    UpdateImage = 13,  // u32 id, Hash pixels
    DestroyBuffer = 14,
    DestroyImage = 15,
    BeginPass = 20,    // u8 n_colors, then n_colors × float4 clear; u8 clear_depth
    ApplyPipeline = 21, // u32 pipeline_id (opaque server id)
    ApplyBindings = 22, // u8 n_vb, then n_vb × u32 buf_id; u32 ib_id; u8 n_img, n_img × u32
    ApplyUniforms = 23, // u8 slot, Hash data
    Draw = 24,         // i32 base, i32 num_elements, i32 num_instances
    EndPass = 25,
    Commit = 26,
    // Present a content-addressed framebuffer to the player (SDL blit path).
    // Payload: u16 w, u16 h, u8 format (0=BGRA8), u8 encoding (0=raw, 1=lz4),
    //          u32 raw_size, Hash blob (compressed or raw bytes).
    // Legacy interim path — multi-MB; does NOT meet 60 fps OTA goals.
    Present = 27,
    // One same-texture sprite run (spyder::SpriteBatch). Payload:
    //   u32 image_id, u16 n_verts, Hash verts (n * 24 B SpriteVertex),
    //   Hash mvp (64 B = 16×f32 column-major world→clip).
    // Steady-state path: MakeImage once (cached), SpriteRun verts + mvp each
    // frame (~tens of KB). Player replays with SDL_RenderGeometry.
    SpriteRun = 32,
    // Framing markers for measurement / reconnect
    // FrameBegin: u32 seq, u8 flags (bit0 = cold/full state),
    //             u16 contentW, u16 contentH (server swapchain pixels;
    //             player letterboxes NDC to this aspect — no stretch).
    FrameBegin = 30,
    FrameEnd = 31,
    // ── Recipe verbs (🎯T128.7) — source on the wire, rasterise on player ──
    // MakeSvg: u32 id, i16 targetW, i16 targetH (-1 = intrinsic), Hash svg_utf8
    MakeSvg = 40,
    // MakeText: u32 id, f32 sizePt, f32 r,g,b,a, i32 faceIndex,
    //           Hash font_bytes, Hash text_utf8
    MakeText = 41,
    // MakeEncodedImage: u32 id, u8 format (0=png), Hash encoded_bytes
    // Player decodes to RGBA (w/h from decode). Flatten fallback = MakeImage.
    MakeEncodedImage = 42,
};

// Encoded-image format tag for Op::MakeEncodedImage.
constexpr uint8_t kEncodedPng = 0;
constexpr uint8_t kEncodedJpeg = 1;

// Recipe kind stored in the server-side image registry.
enum class ImageRecipeKind : uint8_t {
    Pixels = 0,   // opaque RGBA — MakeImage flatten
    Svg = 1,      // MakeSvg
    Text = 2,     // MakeText
    Encoded = 3,  // MakeEncodedImage (PNG/JPEG bytes)
};

// One registered image for LiveCapture emission.
struct ImageRecipe {
    ImageRecipeKind kind = ImageRecipeKind::Pixels;
    uint16_t w = 0;
    uint16_t h = 0;
    std::vector<uint8_t> rgba; // Pixels flatten / optional
    // Svg
    std::string svg;
    int16_t svgTargetW = -1;
    int16_t svgTargetH = -1;
    // Text
    std::string text;
    std::vector<uint8_t> fontBytes;
    int32_t faceIndex = 0;
    float sizePt = 0.f;
    float colorR = 1.f, colorG = 1.f, colorB = 1.f, colorA = 1.f;
    // Encoded
    std::vector<uint8_t> encoded;
    uint8_t encodedFormat = kEncodedPng;
};

// SpriteVertex on the wire (matches spyder::SpriteVertex layout).
constexpr size_t kSpriteVertexBytes = 24; // 5×f32 + u32 abgr

// Pixel formats for Op::Present.
constexpr uint8_t kPresentBGRA8 = 0;
constexpr uint8_t kPresentRGBA8 = 1;
// Encodings for the Present blob body.
constexpr uint8_t kPresentEncRaw = 0;
constexpr uint8_t kPresentEncLz4 = 1;

// ── Hash ──────────────────────────────────────────────────────────

Hash hashBytes(const void* data, size_t n);
inline Hash hashBytes(std::span<const uint8_t> s) {
    return hashBytes(s.data(), s.size());
}
std::string hashHex(const Hash&);

// ── Content-addressed cache ───────────────────────────────────────

// In-memory store. Player persists to disk later (T128.5 durable path); for
// the spike, process lifetime is enough to measure warm reconnect.
class Cache {
public:
    bool contains(const Hash& h) const;
    // Returns nullptr if missing.
    const std::vector<uint8_t>* get(const Hash& h) const;
    // Insert or overwrite. Returns true if this was a new key.
    bool put(const Hash& h, std::vector<uint8_t> bytes);
    bool put(const Hash& h, const void* data, size_t n);
    size_t size() const { return map_.size(); }
    size_t totalBytes() const { return totalBytes_; }
    void clear();

private:
    struct HashKey {
        Hash h;
        bool operator==(const HashKey& o) const { return h == o.h; }
    };
    struct HashKeyHash {
        size_t operator()(const HashKey& k) const {
            size_t x = 0;
            for (size_t i = 0; i < 8; ++i)
                x ^= (size_t)k.h[i] << (i * 8);
            // fold high 8 bytes
            for (size_t i = 0; i < 8; ++i)
                x ^= (size_t)k.h[8 + i] << (i * 8);
            return x;
        }
    };
    std::unordered_map<HashKey, std::vector<uint8_t>, HashKeyHash> map_;
    size_t totalBytes_ = 0;
};

// ── Writer ────────────────────────────────────────────────────────

// Builds one SP2S payload. Does not include MessageHeader.
class Writer {
public:
    explicit Writer(Cache* serverCache = nullptr);

    // contentW/H: server framebuffer size (aspect for player letterbox).
    // Zero dimensions are written as-is; player falls back to window aspect.
    void frameBegin(uint32_t seq, bool fullState,
                    uint16_t contentW = 0, uint16_t contentH = 0);
    void frameEnd();

    // Emit Blob or BlobRef depending on whether `peerHas` / local cache says
    // the peer already has this content. On the server, pass the player's
    // known-cache (or nullptr to always send full on first Writer lifetime
    // and track via internal sent_ set).
    Hash emitBlob(const void* data, size_t n);

    void makeBuffer(uint32_t id, uint32_t size, uint32_t usage, const void* data, size_t n);
    void makeImage(uint32_t id, uint16_t w, uint16_t h, uint32_t pixelFormat,
                   const void* pixels, size_t n);
    void updateBuffer(uint32_t id, const void* data, size_t n);
    void updateImage(uint32_t id, const void* pixels, size_t n);
    void destroyBuffer(uint32_t id);
    void destroyImage(uint32_t id);
    void beginPass(std::span<const float> clearRgba /* 4*n */, bool clearDepth);
    void applyPipeline(uint32_t pipelineId);
    void applyBindings(std::span<const uint32_t> vbufs, uint32_t ibuf,
                       std::span<const uint32_t> images);
    void applyUniforms(uint8_t slot, const void* data, size_t n);
    void draw(int32_t base, int32_t numElements, int32_t numInstances);
    void endPass();
    void commit();

    // Present a framebuffer. Compresses with LZ4 when it shrinks the payload;
    // content-addressed so identical frames become BlobRef-only.
    void present(uint16_t w, uint16_t h, uint8_t format,
                 const void* pixels, size_t rawBytes);

    // Emit one sprite batch run (verts are world-space SpriteVertex[]).
    // `mvp` is 16 floats column-major. Image must already be MakeImage'd
    // (or a recipe verb that materialises the same image_id).
    void spriteRun(uint32_t imageId, const void* verts, uint16_t nVerts,
                   const float mvp[16]);

    // Recipe verbs (🎯T128.7) — content-addressed source, not raster.
    void makeSvg(uint32_t id, int16_t targetW, int16_t targetH,
                 const void* svgUtf8, size_t n);
    void makeText(uint32_t id, float sizePt,
                  float r, float g, float b, float a,
                  int32_t faceIndex,
                  const void* fontBytes, size_t fontN,
                  const void* textUtf8, size_t textN);
    void makeEncodedImage(uint32_t id, uint8_t format,
                          const void* encoded, size_t n);

    // Steal the finished payload (ops only; caller wraps with MessageHeader).
    std::vector<uint8_t> take();
    const std::vector<uint8_t>& bytes() const { return buf_; }
    size_t size() const { return buf_.size(); }

    // Stats for cold/warm measurement.
    struct Stats {
        size_t fullBlobBytes = 0;
        size_t fullBlobCount = 0;
        size_t refBlobCount = 0;
        size_t opCount = 0;
    };
    Stats stats() const { return stats_; }
    void resetStats() { stats_ = {}; }

private:
    void putU8(uint8_t v);
    void putU16(uint16_t v);
    void putU32(uint32_t v);
    void putI32(int32_t v);
    void putF32(float v);
    void putHash(const Hash& h);
    void putBytes(const void* p, size_t n);
    void putOp(Op op);

    Cache* cache_ = nullptr; // optional: record what we have sent
    std::vector<uint8_t> buf_;
    // Hashes already fully transferred in this Writer's lifetime (and/or
    // known present in cache_). Used to prefer BlobRef.
    std::unordered_map<std::string, bool> sentFull_;
    Stats stats_{};
};

// ── Reader ────────────────────────────────────────────────────────

class Reader {
public:
    explicit Reader(Cache* playerCache);

    // Parse one SP2S payload. Invokes the callback per op. Returns false on
    // malformed input. Blob ops populate the cache; BlobRef requires a hit.
    using OpFn = bool (*)(Op op, const uint8_t* payload, size_t payloadLen, void* user);
    // Lower-level: walk ops with a visitor that can pull typed fields via
    // the Cursor helpers below.
    struct Cursor {
        const uint8_t* p = nullptr;
        const uint8_t* end = nullptr;
        bool ok = true;
        uint8_t u8();
        uint16_t u16();
        uint32_t u32();
        int32_t i32();
        float f32();
        Hash hash();
        std::span<const uint8_t> bytes(size_t n);
        bool remain() const { return ok && p < end; }
    };

    // Decode every op. For Blob: insert into cache. For BlobRef: require hit.
    // Visitor returns false to abort.
    bool decode(std::span<const uint8_t> payload,
                bool (*visit)(Op op, Cursor& c, void* user),
                void* user);

    struct Stats {
        size_t fullBlobBytes = 0;
        size_t fullBlobCount = 0;
        size_t refBlobCount = 0;
        size_t cacheMisses = 0;
        size_t opCount = 0;
    };
    Stats stats() const { return stats_; }
    void resetStats() { stats_ = {}; }

private:
    Cache* cache_ = nullptr;
    Stats stats_{};
};

// ── Synthetic scene for measurement (no GPU) ──────────────────────

// Models a tiltbuggy-like frame: a few static textures + per-frame uniforms
// and draws. Used by the spike to compare cold/warm/steady byte costs to an
// H.264 full-res estimate without requiring a live device.
struct SyntheticScene {
    // One-time assets (created on first frame, then cacheable).
    std::vector<uint8_t> texDirt;   // e.g. 512×512 RGBA
    std::vector<uint8_t> texAsphalt;
    std::vector<uint8_t> meshVerts;
    std::vector<uint8_t> meshIdx;

    static SyntheticScene tiltbuggyLike();

    // Write one frame into `w`. firstFrame emits Make* + full blobs; later
    // frames only UpdateUniforms + Draw (+ optional texture churn).
    void writeFrame(Writer& w, uint32_t seq, bool firstFrame,
                    bool forceTextureChurn = false) const;
};

// Rough H.264 full-res byte estimate for comparison (not a real encode).
// keyframeInterval frames, average p-frame ratio of keyframe size.
struct H264Estimate {
    size_t keyframeBytes = 0;
    size_t pframeBytes = 0;
    size_t bytesForFrames(int n, int keyEvery = 60) const;
};

// Conservative estimate for 2048×1536 BGRA → H.264 High @ CRF~36 style.
H264Estimate estimateH264FullRes(int width, int height);

// ── Live capture (game thread) ────────────────────────────────────
//
// SpriteBatch::submit and loadImage feed this when armed. ServerSession
// arms one LiveCapture per frame under transport=cmdstream.

class LiveCapture {
public:
    // contentW/H = server swapchain size (pixels) for player aspect-fit.
    void begin(uint32_t seq, Cache* serverCache,
               uint16_t contentW, uint16_t contentH);
    // Ensure MakeImage / recipe verb has been emitted for this sokol image
    // id (once per session). Prefers recipe (SVG/text/encoded) when registered.
    void noteImage(uint32_t imageId, uint16_t w, uint16_t h,
                   const void* rgba, size_t n);
    // Emit whatever recipe (or pixel flatten) is registered for imageId.
    void noteRegisteredImage(uint32_t imageId);
    void spriteRun(uint32_t imageId, const void* verts, uint16_t nVerts,
                   const float mvp[16]);
    // Finish frame; returns SP2S payload (may be empty if no runs).
    std::vector<uint8_t> end();
    bool active() const { return active_; }
    Writer::Stats stats() const { return lastStats_; }
    size_t runCount() const { return runCount_; }
    // Drop session-local MakeImage memo (player detached / cache cleared).
    void resetSession() {
        imagesEmitted_.clear();
        active_ = false;
        w_.reset();
        runCount_ = 0;
        lastStats_ = {};
    }

private:
    std::unique_ptr<Writer> w_;
    Cache* cache_ = nullptr;
    bool active_ = false;
    size_t runCount_ = 0;
    Writer::Stats lastStats_{};
    std::unordered_map<uint32_t, bool> imagesEmitted_;
};

// Arm/disarm process-global live capture (game thread only).
void setLiveCapture(LiveCapture* cap);
LiveCapture* liveCapture();

// Side-table: recipe or RGBA for sokol image ids (filled by load/raster paths).
void registerImagePixels(uint32_t imageId, uint16_t w, uint16_t h,
                         const void* rgba, size_t n);
void registerImageSvg(uint32_t imageId, std::string_view svg,
                      int16_t targetW, int16_t targetH,
                      uint16_t rasterW, uint16_t rasterH);
void registerImageText(uint32_t imageId, std::string_view text,
                       const void* fontBytes, size_t fontN, int32_t faceIndex,
                       float sizePt, float r, float g, float b, float a,
                       uint16_t rasterW, uint16_t rasterH);
void registerImageEncoded(uint32_t imageId, uint8_t format,
                          const void* encoded, size_t n,
                          uint16_t w, uint16_t h);
bool lookupImagePixels(uint32_t imageId, uint16_t& w, uint16_t& h,
                       const std::vector<uint8_t>*& px);
const ImageRecipe* lookupImageRecipe(uint32_t imageId);

} // namespace spyder::cmdstream

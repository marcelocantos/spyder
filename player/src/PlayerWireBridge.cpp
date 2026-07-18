// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

#include <player/PlayerWireBridge.h>
#include <player/CmdStream.h>
#include <player/VideoDecoder.h>
#include <player/WebSocketClient.h>
#include <player/svg.h>   // rasterizeSvgToPixels (CPU; no GPU on player)
#include <player/text.h>  // rasterizeTextToPixelsFromMemory

#include "wire_input.h"

#include <SDL3/SDL_iostream.h>
#include <SDL3/SDL_surface.h>
#include <SDL3_image/SDL_image.h>
#include <lz4.h>
#include <spdlog/spdlog.h>

#include <cstring>
#include <mutex>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

namespace spyder {

namespace {

// Parse AVCC-format NAL units from encoded frame data.
// AVCC format: [4-byte big-endian length][NAL body] repeated.
// For keyframes the encoder prepends SPS and PPS NAL units.
// NAL type is (first byte of NAL body) & 0x1F:
//   7 = SPS, 8 = PPS, 5 = IDR (keyframe), 1 = non-IDR (P-frame)
struct AVCCParser {
    std::vector<uint8_t> sps, pps;
    bool paramsDirty = false;

    std::vector<std::pair<const uint8_t*, size_t>>
    parse(const uint8_t* data, size_t size) {
        std::vector<std::pair<const uint8_t*, size_t>> frameNals;
        size_t offset = 0;
        while (offset + 4 <= size) {
            uint32_t nalLen = (uint32_t(data[offset]) << 24)
                            | (uint32_t(data[offset+1]) << 16)
                            | (uint32_t(data[offset+2]) << 8)
                            | uint32_t(data[offset+3]);
            offset += 4;
            if (nalLen == 0 || offset + nalLen > size) break;

            const uint8_t* nalBody = data + offset;
            uint8_t nalType = nalBody[0] & 0x1F;

            if (nalType == 7) {
                if (sps.size() != nalLen || std::memcmp(sps.data(), nalBody, nalLen) != 0) {
                    sps.assign(nalBody, nalBody + nalLen);
                    paramsDirty = true;
                }
            } else if (nalType == 8) {
                if (pps.size() != nalLen || std::memcmp(pps.data(), nalBody, nalLen) != 0) {
                    pps.assign(nalBody, nalBody + nalLen);
                    paramsDirty = true;
                }
            } else {
                frameNals.emplace_back(nalBody, nalLen);
            }
            offset += nalLen;
        }
        return frameNals;
    }

    bool hasParams() const { return !sps.empty() && !pps.empty(); }
};

} // namespace

struct PlayerWireBridge::Impl {
    Config cfg;
    std::shared_ptr<WsConnection> conn;
    std::unique_ptr<VideoDecoder> decoder;
    AVCCParser avcc;

    // Frame buffer, written from decoder callback (VT thread), read from pump.
    std::mutex frameMutex;
    DecodedFrame pending;
    bool pendingReady = false;
    CmdDisplayFrame pendingCmd;
    bool pendingCmdReady = false;

    PumpStats stats;

    // 🎯T128 — content-addressed resource cache for SP2S frames.
    cmdstream::Cache cmdCache;
    uint8_t transport = wire::kTransportH264;

    // 🎯T154 SP2T: last server→player durable dump (pollSp2tSnapshot).
    std::mutex sp2tMutex;
    std::vector<uint8_t> pendingSp2t;
    bool pendingSp2tReady = false;
};

PlayerWireBridge::PlayerWireBridge(Config config)
    : i_(std::make_unique<Impl>()) {
    i_->cfg = std::move(config);
}

PlayerWireBridge::~PlayerWireBridge() = default;

bool PlayerWireBridge::connect(wire::SessionConfig& outConfig) {
    const std::string path = "/ws/wire?preference=" + i_->cfg.serverName
                           + "&name=" + i_->cfg.serverName;
    i_->conn = spyder::connectWebSocket(i_->cfg.host, i_->cfg.port, path,
                                    i_->cfg.connectTimeoutMs);
    if (!i_->conn || !i_->conn->isOpen()) {
        SPDLOG_ERROR("PlayerWireBridge: failed to connect to stream relay");
        return false;
    }
    SPDLOG_INFO("PlayerWireBridge: connected to stream relay");

    // Wait for SessionConfig (skip unrelated housekeeping messages).
    while (i_->conn->isOpen()) {
        std::vector<char> msg;
        if (!i_->conn->recvBinary(msg) || msg.size() < 8) return false;
        uint32_t magic = 0;
        std::memcpy(&magic, msg.data(), 4);
        if (magic == wire::kSessionConfigMagic &&
            msg.size() >= sizeof(wire::MessageHeader) + sizeof(wire::SessionConfig)) {
            std::memcpy(&outConfig,
                        msg.data() + sizeof(wire::MessageHeader),
                        sizeof(wire::SessionConfig));
            return true;
        }
    }
    return false;
}

bool PlayerWireBridge::sendDeviceInfo(const wire::DeviceInfo& devInfo) {
    if (!i_->conn || !i_->conn->isOpen()) return false;
    // Advertise command-stream replay capability (T128.9). Server intersects
    // with TRANSPORT=cmdstream before selecting the rung.
    wire::DeviceInfo di = devInfo;
    di.magic = wire::kDeviceInfoMagic;
    di.version = wire::kProtocolVersion;
    di.capabilities = static_cast<uint8_t>(di.capabilities | wire::kCapCommandStream);
    wire::MessageHeader hdr{};
    hdr.magic = wire::kDeviceInfoMagic;
    hdr.length = sizeof(wire::DeviceInfo);
    std::vector<uint8_t> msg(sizeof(hdr) + sizeof(di));
    std::memcpy(msg.data(), &hdr, sizeof(hdr));
    std::memcpy(msg.data() + sizeof(hdr), &di, sizeof(di));
    i_->conn->sendBinary(msg.data(), msg.size());
    // VideoDecoder is deferred until the first H.264 AU (see pump). Cmdstream
    // sessions never need VT/FFmpeg; constructing early was wasted work on the
    // post-DeviceInfo path that must stay lean on iOS.
    return true;
}

bool PlayerWireBridge::sendSafeAreaUpdate(const wire::SafeAreaUpdate& sa) {
    if (!i_->conn || !i_->conn->isOpen()) return false;
    wire::SafeAreaUpdate msg = sa;
    msg.magic = wire::kSafeAreaMagic;
    wire::MessageHeader hdr{};
    hdr.magic = wire::kSafeAreaMagic;
    hdr.length = sizeof(wire::SafeAreaUpdate);
    std::vector<uint8_t> buf(sizeof(hdr) + sizeof(msg));
    std::memcpy(buf.data(), &hdr, sizeof(hdr));
    std::memcpy(buf.data() + sizeof(hdr), &msg, sizeof(msg));
    i_->conn->sendBinary(buf.data(), buf.size());
    return true;
}

bool PlayerWireBridge::sendLifecycle(const wire::ViewerLifecycle& life) {
    if (!i_->conn || !i_->conn->isOpen()) return false;
    wire::ViewerLifecycle msg = life;
    msg.magic = wire::kLifecycleMagic;
    wire::MessageHeader hdr{};
    hdr.magic = wire::kLifecycleMagic;
    hdr.length = sizeof(wire::ViewerLifecycle);
    std::vector<uint8_t> buf(sizeof(hdr) + sizeof(msg));
    std::memcpy(buf.data(), &hdr, sizeof(hdr));
    std::memcpy(buf.data() + sizeof(hdr), &msg, sizeof(msg));
    i_->conn->sendBinary(buf.data(), buf.size());
    return true;
}

bool PlayerWireBridge::sendSp2tSnapshot(const std::vector<uint8_t>& sqliteDump) {
    if (!i_->conn || !i_->conn->isOpen() || sqliteDump.empty()) return false;
    wire::MessageHeader hdr{};
    hdr.magic = wire::kSqlpipeMsgMagic;
    hdr.length = static_cast<uint32_t>(sqliteDump.size());
    std::vector<uint8_t> buf(sizeof(hdr) + sqliteDump.size());
    std::memcpy(buf.data(), &hdr, sizeof(hdr));
    std::memcpy(buf.data() + sizeof(hdr), sqliteDump.data(), sqliteDump.size());
    i_->conn->sendBinary(buf.data(), buf.size());
    SPDLOG_INFO("PlayerWireBridge: SP2T snapshot sent ({} bytes)", sqliteDump.size());
    return true;
}

bool PlayerWireBridge::pollSp2tSnapshot(std::vector<uint8_t>& outDump) {
    std::lock_guard<std::mutex> lock(i_->sp2tMutex);
    if (!i_->pendingSp2tReady) return false;
    outDump = std::move(i_->pendingSp2t);
    i_->pendingSp2t.clear();
    i_->pendingSp2tReady = false;
    return true;
}

void PlayerWireBridge::sendEvent(const SDL_Event& e) {
    if (!i_->conn || !i_->conn->isOpen()) return;
    wire::MessageHeader hdr{};
    hdr.magic = wire::kSdlEventMagic;
    hdr.length = sizeof(SDL_Event);
    std::vector<uint8_t> msg(sizeof(hdr) + sizeof(SDL_Event));
    std::memcpy(msg.data(), &hdr, sizeof(hdr));
    std::memcpy(msg.data() + sizeof(hdr), &e, sizeof(SDL_Event));
    i_->conn->sendBinary(msg.data(), msg.size());
}

bool PlayerWireBridge::pump() {
    if (!i_->conn) return false;
    i_->stats = {};
    // Cap messages per pump and stop as soon as we have a presentable frame.
    // Processing eight ~11 MB SP2S Presents before render made Android ~0.25 fps
    // (PlayerLog: pump avg≈4s, 1 tick / 4s). One Present (or video AU) then
    // return → pollFrame/render can run between network frames.
    constexpr int kMaxMsgsPerPump = 4;
    int msgs = 0;
    bool haveDisplayFrame = false;
    while (i_->conn->isOpen() && i_->conn->available() > 0 &&
           msgs < kMaxMsgsPerPump && !haveDisplayFrame) {
        ++msgs;
        std::vector<char> data;
        if (!i_->conn->recvBinary(data) || data.size() < 8) break;

        uint32_t magic = 0;
        std::memcpy(&magic, data.data(), 4);
        if (magic == wire::kVideoStreamMagic) {
            // 🎯T142: validate the wire length before deriving any pointer/size.
            // Guards unsigned underflow of (length - 5), 32-bit overflow of
            // (8 + length), and the OOB seq/AVCC reads on a malformed message.
            uint32_t seq = 0;
            const uint8_t* avccData = nullptr;
            size_t avccSize = 0;
            if (!detail::decodeVideoStreamMessage(data, seq, avccData, avccSize))
                continue;

            if (!i_->decoder) {
                i_->decoder = std::make_unique<VideoDecoder>(
                    [this](const VideoFrame& f) {
                        std::lock_guard<std::mutex> lock(i_->frameMutex);
                        i_->pending.format = f.format;
                        i_->pending.width = f.width;
                        i_->pending.height = f.height;
                        i_->pending.stride0 = f.strides[0];
                        i_->pending.stride1 = f.strides[1];
                        i_->pending.stride2 = f.strides[2];

                        auto copyPlane = [](std::vector<uint8_t>& dst,
                                            const uint8_t* src, int stride,
                                            int rows) {
                            if (!src || stride <= 0 || rows <= 0) {
                                dst.clear();
                                return;
                            }
                            size_t bytes = static_cast<size_t>(stride) * rows;
                            dst.assign(src, src + bytes);
                        };

                        switch (f.format) {
                        case VideoFrame::Format::BGRA:
                            copyPlane(i_->pending.plane0, f.planes[0],
                                      f.strides[0], f.height);
                            i_->pending.plane1.clear();
                            i_->pending.plane2.clear();
                            break;
                        case VideoFrame::Format::NV12:
                            copyPlane(i_->pending.plane0, f.planes[0],
                                      f.strides[0], f.height);
                            copyPlane(i_->pending.plane1, f.planes[1],
                                      f.strides[1], f.height / 2);
                            i_->pending.plane2.clear();
                            break;
                        case VideoFrame::Format::IYUV:
                            copyPlane(i_->pending.plane0, f.planes[0],
                                      f.strides[0], f.height);
                            copyPlane(i_->pending.plane1, f.planes[1],
                                      f.strides[1], f.height / 2);
                            copyPlane(i_->pending.plane2, f.planes[2],
                                      f.strides[2], f.height / 2);
                            break;
                        }
                        i_->pendingReady = true;
                    });
                SPDLOG_INFO("PlayerWireBridge: VideoDecoder created (H.264 AU)");
            }

            auto frameNals = i_->avcc.parse(avccData, avccSize);
            if (i_->decoder && i_->avcc.paramsDirty && i_->avcc.hasParams()) {
                i_->decoder->setParameterSets(
                    i_->avcc.sps.data(), i_->avcc.sps.size(),
                    i_->avcc.pps.data(), i_->avcc.pps.size());
                i_->avcc.paramsDirty = false;
                SPDLOG_INFO("PlayerWireBridge: decoder initialized with SPS/PPS");
            }
            if (i_->decoder) {
                static const uint8_t startCode[] = {0x00, 0x00, 0x00, 0x01};
                for (auto& [nalBody, nalSize] : frameNals) {
                    uint8_t nalType = nalBody[0] & 0x1F;
                    if (nalType != 1 && nalType != 5) continue;
                    std::vector<uint8_t> annexB(4 + nalSize);
                    std::memcpy(annexB.data(), startCode, 4);
                    std::memcpy(annexB.data() + 4, nalBody, nalSize);
                    i_->decoder->decode(annexB.data(), annexB.size());
                }
            }
            i_->stats.framesThisTick++;
            i_->stats.lastSeq = seq;
            haveDisplayFrame = true; // decoded into pending via callback soon
        } else if (magic == wire::kCommandStreamMagic) {
            // 🎯T128 — SP2S. Prefer SpriteRun (draw-list) → CmdDisplayFrame.
            // Legacy Present still fills DecodedFrame for SDL blit.
            if (data.size() < sizeof(wire::MessageHeader)) continue;
            uint32_t length = 0;
            std::memcpy(&length, data.data() + 4, 4);
            if (data.size() < sizeof(wire::MessageHeader) + length) continue;
            const auto* payload = reinterpret_cast<const uint8_t*>(
                data.data() + sizeof(wire::MessageHeader));

            struct Ctx {
                cmdstream::Cache* cache = nullptr;
                DecodedFrame* out = nullptr;
                bool* ready = nullptr;
                CmdDisplayFrame* cmdOut = nullptr;
                bool* cmdReady = nullptr;
                std::mutex* mu = nullptr;
                bool gotPresent = false;
                bool gotSprite = false;
                uint32_t frameSeq = 0;
                size_t wireBytes = 0;
                CmdDisplayFrame building{};
            } ctx;
            ctx.cache = &i_->cmdCache;
            ctx.out = &i_->pending;
            ctx.ready = &i_->pendingReady;
            ctx.cmdOut = &i_->pendingCmd;
            ctx.cmdReady = &i_->pendingCmdReady;
            ctx.mu = &i_->frameMutex;
            ctx.wireBytes = length;

            cmdstream::Reader reader(&i_->cmdCache);
            auto visit = [](cmdstream::Op op, cmdstream::Reader::Cursor& c,
                            void* user) -> bool {
                using cmdstream::Op;
                auto* cx = static_cast<Ctx*>(user);
                switch (op) {
                case Op::FrameBegin:
                    cx->frameSeq = c.u32();
                    (void)c.u8();
                    {
                        const uint16_t cw = c.u16();
                        const uint16_t ch = c.u16();
                        cx->building = {};
                        cx->building.seq = cx->frameSeq;
                        cx->building.contentW = cw;
                        cx->building.contentH = ch;
                    }
                    return c.ok;
                case Op::FrameEnd: case Op::EndPass: case Op::Commit: case Op::End:
                case Op::Blob: case Op::BlobRef:
                    return true;
                case Op::MakeBuffer:
                    (void)c.u32(); (void)c.u32(); (void)c.u32(); (void)c.hash();
                    return c.ok;
                case Op::MakeImage: {
                    const uint32_t id = c.u32();
                    const uint16_t w = c.u16();
                    const uint16_t h = c.u16();
                    (void)c.u32(); // pixel format
                    const auto hash = c.hash();
                    if (!c.ok || !cx->cache) return false;
                    const auto* blob = cx->cache->get(hash);
                    if (!blob) return false;
                    CmdImage img;
                    img.id = id;
                    img.w = w;
                    img.h = h;
                    img.rgba = *blob;
                    cx->building.images.push_back(std::move(img));
                    return c.ok;
                }
                case Op::MakeSvg: {
                    const uint32_t id = c.u32();
                    const int16_t tw = static_cast<int16_t>(c.u16());
                    const int16_t th = static_cast<int16_t>(c.u16());
                    const auto hash = c.hash();
                    if (!c.ok || !cx->cache) return false;
                    const auto* blob = cx->cache->get(hash);
                    if (!blob) return false;
                    std::string_view svg(
                        reinterpret_cast<const char*>(blob->data()), blob->size());
                    auto px = spyder::rasterizeSvgToPixels(
                        svg, tw < 0 ? -1 : int(tw), th < 0 ? -1 : int(th));
                    if (px.isNull()) return false;
                    CmdImage img;
                    img.id = id;
                    img.w = static_cast<uint16_t>(px.width);
                    img.h = static_cast<uint16_t>(px.height);
                    img.rgba = std::move(px.rgba);
                    cx->building.images.push_back(std::move(img));
                    return c.ok;
                }
                case Op::MakeText: {
                    const uint32_t id = c.u32();
                    const float sizePt = c.f32();
                    const float r = c.f32(), g = c.f32(), b = c.f32(), a = c.f32();
                    const int32_t faceIndex = c.i32();
                    const auto fontHash = c.hash();
                    const auto textHash = c.hash();
                    if (!c.ok || !cx->cache) return false;
                    const auto* fontBlob = cx->cache->get(fontHash);
                    const auto* textBlob = cx->cache->get(textHash);
                    if (!fontBlob || !textBlob) return false;
                    std::string text(reinterpret_cast<const char*>(textBlob->data()),
                                    textBlob->size());
                    auto px = spyder::rasterizeTextToPixelsFromMemory(
                        text, fontBlob->data(), fontBlob->size(), faceIndex,
                        sizePt, {r, g, b, a});
                    if (px.isNull()) return false;
                    CmdImage img;
                    img.id = id;
                    img.w = static_cast<uint16_t>(px.width);
                    img.h = static_cast<uint16_t>(px.height);
                    img.rgba = std::move(px.rgba);
                    cx->building.images.push_back(std::move(img));
                    return c.ok;
                }
                case Op::MakeEncodedImage: {
                    const uint32_t id = c.u32();
                    const uint8_t format = c.u8();
                    const auto hash = c.hash();
                    if (!c.ok || !cx->cache) return false;
                    const auto* blob = cx->cache->get(hash);
                    if (!blob) return false;
                    (void)format; // PNG/JPEG both via SDL_image
                    SDL_IOStream* io = SDL_IOFromConstMem(blob->data(), blob->size());
                    if (!io) return false;
                    SDL_Surface* raw = IMG_Load_IO(io, true);
                    if (!raw) return false;
                    SDL_Surface* rgba = raw;
                    bool owned = false;
                    if (raw->format != SDL_PIXELFORMAT_RGBA32) {
                        rgba = SDL_ConvertSurface(raw, SDL_PIXELFORMAT_RGBA32);
                        SDL_DestroySurface(raw);
                        owned = true;
                        if (!rgba) return false;
                    }
                    // Premultiply to match spyder::loadImage.
                    {
                        const int ww = rgba->w, hh = rgba->h, pitch = rgba->pitch;
                        auto* pixels = static_cast<uint8_t*>(rgba->pixels);
                        for (int y = 0; y < hh; ++y) {
                            uint8_t* row = pixels + size_t(y) * pitch;
                            for (int x = 0; x < ww; ++x) {
                                uint8_t* p = row + x * 4;
                                const uint32_t aa = p[3];
                                p[0] = static_cast<uint8_t>((p[0] * aa + 127u) / 255u);
                                p[1] = static_cast<uint8_t>((p[1] * aa + 127u) / 255u);
                                p[2] = static_cast<uint8_t>((p[2] * aa + 127u) / 255u);
                            }
                        }
                    }
                    CmdImage img;
                    img.id = id;
                    img.w = static_cast<uint16_t>(rgba->w);
                    img.h = static_cast<uint16_t>(rgba->h);
                    img.rgba.assign(static_cast<const uint8_t*>(rgba->pixels),
                                    static_cast<const uint8_t*>(rgba->pixels) +
                                        size_t(rgba->w) * size_t(rgba->h) * 4);
                    if (owned) SDL_DestroySurface(rgba);
                    else SDL_DestroySurface(raw);
                    cx->building.images.push_back(std::move(img));
                    return c.ok;
                }
                case Op::UpdateBuffer: case Op::UpdateImage:
                    (void)c.u32(); (void)c.hash();
                    return c.ok;
                case Op::DestroyBuffer: case Op::DestroyImage:
                    (void)c.u32();
                    return c.ok;
                case Op::BeginPass: {
                    uint8_t n = c.u8();
                    for (uint8_t i = 0; i < n * 4; ++i) (void)c.u32();
                    (void)c.u8();
                    return c.ok;
                }
                case Op::ApplyPipeline:
                    (void)c.u32();
                    return c.ok;
                case Op::ApplyBindings: {
                    uint8_t nv = c.u8();
                    for (uint8_t i = 0; i < nv; ++i) (void)c.u32();
                    (void)c.u32();
                    uint8_t ni = c.u8();
                    for (uint8_t i = 0; i < ni; ++i) (void)c.u32();
                    return c.ok;
                }
                case Op::ApplyUniforms:
                    (void)c.u8(); (void)c.hash();
                    return c.ok;
                case Op::Draw:
                    (void)c.i32(); (void)c.i32(); (void)c.i32();
                    return c.ok;
                case Op::SpriteRun: {
                    const uint32_t imageId = c.u32();
                    const uint16_t nVerts = c.u16();
                    const auto vh = c.hash();
                    const auto mh = c.hash();
                    if (!c.ok || !cx->cache) return false;
                    const auto* vb = cx->cache->get(vh);
                    const auto* mb = cx->cache->get(mh);
                    if (!vb || !mb) return false;
                    if (vb->size() < size_t(nVerts) * cmdstream::kSpriteVertexBytes)
                        return false;
                    if (mb->size() < 16 * sizeof(float)) return false;
                    CmdSpriteRun run;
                    run.imageId = imageId;
                    run.nVerts = nVerts;
                    run.verts = *vb;
                    std::memcpy(run.mvp, mb->data(), 16 * sizeof(float));
                    cx->building.runs.push_back(std::move(run));
                    cx->gotSprite = true;
                    return c.ok;
                }
                case Op::Present: {
                    const uint16_t w = c.u16();
                    const uint16_t h = c.u16();
                    const uint8_t format = c.u8();
                    const uint8_t encoding = c.u8();
                    const uint32_t rawSize = c.u32();
                    const cmdstream::Hash hash = c.hash();
                    if (!c.ok || !cx->cache) return false;
                    const auto* blob = cx->cache->get(hash);
                    if (!blob) return false;
                    if (format != cmdstream::kPresentBGRA8 &&
                        format != cmdstream::kPresentRGBA8) {
                        return false;
                    }
                    std::vector<uint8_t> pixels;
                    if (encoding == cmdstream::kPresentEncLz4) {
                        pixels.resize(rawSize);
                        const int n = LZ4_decompress_safe(
                            reinterpret_cast<const char*>(blob->data()),
                            reinterpret_cast<char*>(pixels.data()),
                            static_cast<int>(blob->size()),
                            static_cast<int>(rawSize));
                        if (n != static_cast<int>(rawSize)) return false;
                    } else if (encoding == cmdstream::kPresentEncRaw) {
                        if (blob->size() != rawSize) return false;
                        pixels = *blob;
                    } else {
                        return false;
                    }
                    {
                        std::lock_guard<std::mutex> lock(*cx->mu);
                        cx->out->format = VideoFrame::Format::BGRA;
                        cx->out->width = w;
                        cx->out->height = h;
                        cx->out->stride0 = static_cast<int>(w) * 4;
                        cx->out->stride1 = 0;
                        cx->out->stride2 = 0;
                        cx->out->plane0 = std::move(pixels);
                        cx->out->plane1.clear();
                        cx->out->plane2.clear();
                        if (format == cmdstream::kPresentRGBA8) {
                            auto& p = cx->out->plane0;
                            for (size_t i = 0; i + 3 < p.size(); i += 4)
                                std::swap(p[i], p[i + 2]);
                        }
                        *cx->ready = true;
                    }
                    cx->gotPresent = true;
                    cx->building.hasPresent = true;
                    return c.ok;
                }
                }
                return false;
            };
            if (!reader.decode({payload, length}, visit, &ctx)) {
                SPDLOG_WARN("PlayerWireBridge: SP2S decode failed (misses={})",
                            reader.stats().cacheMisses);
            } else if (ctx.gotSprite) {
                std::lock_guard<std::mutex> lock(i_->frameMutex);
                ctx.building.wireBytes = length;
                ctx.building.seq = ctx.frameSeq;
                i_->pendingCmd = std::move(ctx.building);
                i_->pendingCmdReady = true;
                i_->stats.framesThisTick++;
                i_->stats.lastSeq = ctx.frameSeq;
                i_->stats.lastWireBytes = length;
                i_->stats.cmdstream = true;
                haveDisplayFrame = true;
                static uint32_t sprLog = 0;
                if (sprLog++ < 3 || (ctx.frameSeq % 60) == 0) {
                    SPDLOG_INFO("PlayerWireBridge: SP2S SpriteRun seq={} wire={}B "
                                "runs={} images={} cache={} full={} refs={}",
                                ctx.frameSeq, length,
                                i_->pendingCmd.runs.size(),
                                i_->pendingCmd.images.size(),
                                i_->cmdCache.size(),
                                reader.stats().fullBlobCount,
                                reader.stats().refBlobCount);
                }
            } else if (ctx.gotPresent) {
                i_->stats.framesThisTick++;
                i_->stats.lastSeq = ctx.frameSeq;
                i_->stats.lastWireBytes = length;
                haveDisplayFrame = true;
                static uint32_t presentLog = 0;
                if (presentLog++ < 3 || (ctx.frameSeq % 60) == 0) {
                    SPDLOG_INFO("PlayerWireBridge: SP2S Present seq={} wire={}B "
                                "cache={} full={} refs={}",
                                ctx.frameSeq, length, i_->cmdCache.size(),
                                reader.stats().fullBlobCount,
                                reader.stats().refBlobCount);
                }
            }
        } else if (magic == wire::kSessionConfigMagic &&
                   data.size() >= sizeof(wire::MessageHeader) + sizeof(wire::SessionConfig)) {
            wire::SessionConfig sc{};
            std::memcpy(&sc, data.data() + sizeof(wire::MessageHeader), sizeof(sc));
            i_->transport = sc.transport;
            SPDLOG_INFO("PlayerWireBridge: SessionConfig update transport={}",
                        sc.transport == wire::kTransportCommandStream ? "cmdstream" : "h264");
        } else if (magic == wire::kServerAssignedMagic) {
            SPDLOG_INFO("PlayerWireBridge: server assigned");
        } else if (magic == wire::kSessionEndMagic) {
            SPDLOG_INFO("PlayerWireBridge: session ended");
        } else if (magic == wire::kSqlpipeMsgMagic &&
                   data.size() > sizeof(wire::MessageHeader)) {
            // 🎯T154 SP2T: server→player durable push (full sqlite dump).
            const size_t n = data.size() - sizeof(wire::MessageHeader);
            std::lock_guard<std::mutex> lock(i_->sp2tMutex);
            i_->pendingSp2t.assign(
                data.begin() + static_cast<std::ptrdiff_t>(sizeof(wire::MessageHeader)),
                data.end());
            i_->pendingSp2tReady = true;
            SPDLOG_INFO("PlayerWireBridge: SP2T snapshot received ({} bytes)", n);
        }
        // Unknown magics are ignored (relay is agnostic; player is tolerant).
    }
    return i_->conn->isOpen();
}

bool PlayerWireBridge::pollFrame(DecodedFrame& out) {
    std::lock_guard<std::mutex> lock(i_->frameMutex);
    if (!i_->pendingReady) return false;
    std::swap(out, i_->pending);
    i_->pendingReady = false;
    return true;
}

bool PlayerWireBridge::pollCmdFrame(CmdDisplayFrame& out) {
    std::lock_guard<std::mutex> lock(i_->frameMutex);
    if (!i_->pendingCmdReady) return false;
    std::swap(out, i_->pendingCmd);
    i_->pendingCmdReady = false;
    return true;
}

PlayerWireBridge::PumpStats PlayerWireBridge::lastPumpStats() const {
    return i_->stats;
}

bool PlayerWireBridge::isOpen() const {
    return i_->conn && i_->conn->isOpen();
}

void PlayerWireBridge::close() {
    if (i_->decoder) i_->decoder->flush();
    if (i_->conn) i_->conn->close();
}

} // namespace spyder

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// H.264 decoder using FFmpeg libavcodec. Used on Android where the
// native MediaCodec API is fragile. Delivers IYUV (planar YUV 4:2:0)
// directly to the renderer — no software CSC.

#ifdef __ANDROID__

#include <player/VideoDecoder.h>
#include <spdlog/spdlog.h>

extern "C" {
#include <libavcodec/avcodec.h>
}

#include <cstring>
#include <mutex>
#include <vector>

namespace spyder {

struct VideoDecoder::M {
    FrameCallback callback;

    const AVCodec* codec = nullptr;
    AVCodecContext* ctx = nullptr;
    AVCodecParserContext* parser = nullptr;
    AVFrame* frame = nullptr;
    AVPacket* pkt = nullptr;
    bool opened = false;

    std::mutex decodeMutex;

    ~M() {
        av_frame_free(&frame);
        av_packet_free(&pkt);
        if (parser) av_parser_close(parser);
        avcodec_free_context(&ctx);
    }

    void init() {
        codec = avcodec_find_decoder(AV_CODEC_ID_H264);
        if (!codec) {
            SPDLOG_ERROR("VideoDecoder: H.264 decoder not found");
            return;
        }

        ctx = avcodec_alloc_context3(codec);
        ctx->thread_count = 2;

        // Don't open yet — wait for SPS/PPS via setParameterSets.
        parser = av_parser_init(AV_CODEC_ID_H264);
        frame = av_frame_alloc();
        pkt = av_packet_alloc();
        SPDLOG_INFO("VideoDecoder: FFmpeg H.264 decoder allocated (deferred open)");
    }

    bool open() {
        if (opened) return true;
        int ret = avcodec_open2(ctx, codec, nullptr);
        if (ret < 0) {
            SPDLOG_ERROR("VideoDecoder: avcodec_open2 failed: {}", ret);
            return false;
        }
        opened = true;
        SPDLOG_INFO("VideoDecoder: codec opened (extradata={} bytes)", ctx->extradata_size);
        return true;
    }

    void deliverDecodedFrames() {
        int ret = 0;
        while (ret >= 0) {
            ret = avcodec_receive_frame(ctx, frame);
            if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) break;
            if (ret < 0) break;

            // libavcodec H.264 outputs AV_PIX_FMT_YUV420P (planar Y/U/V),
            // which maps directly to SDL_PIXELFORMAT_IYUV. Pass plane
            // pointers and strides through with no conversion.
            VideoFrame f;
            f.format = VideoFrame::Format::IYUV;
            f.width = frame->width;
            f.height = frame->height;
            f.planes[0] = frame->data[0];
            f.planes[1] = frame->data[1];
            f.planes[2] = frame->data[2];
            f.strides[0] = frame->linesize[0];
            f.strides[1] = frame->linesize[1];
            f.strides[2] = frame->linesize[2];
            callback(f);
        }
    }
};

VideoDecoder::VideoDecoder(FrameCallback onFrame)
    : m(std::make_unique<M>()) {
    m->callback = std::move(onFrame);
    m->init();
}

VideoDecoder::~VideoDecoder() = default;

void VideoDecoder::setParameterSets(const uint8_t* sps, size_t spsSize,
                                     const uint8_t* pps, size_t ppsSize) {
    if (!m->ctx) return;

    // Set SPS+PPS as extradata BEFORE opening the codec.
    // Format: [start code][SPS][start code][PPS]
    static const uint8_t kStartCode[] = {0x00, 0x00, 0x00, 0x01};
    size_t totalSize = 4 + spsSize + 4 + ppsSize;
    uint8_t* extra = (uint8_t*)av_malloc(totalSize + AV_INPUT_BUFFER_PADDING_SIZE);
    std::memset(extra, 0, totalSize + AV_INPUT_BUFFER_PADDING_SIZE);
    std::memcpy(extra, kStartCode, 4);
    std::memcpy(extra + 4, sps, spsSize);
    std::memcpy(extra + 4 + spsSize, kStartCode, 4);
    std::memcpy(extra + 4 + spsSize + 4, pps, ppsSize);
    av_free(m->ctx->extradata);
    m->ctx->extradata = extra;
    m->ctx->extradata_size = static_cast<int>(totalSize);

    // Now open the codec with the extradata set.
    m->open();

    SPDLOG_INFO("VideoDecoder: SPS/PPS set ({} + {} bytes), codec opened", spsSize, ppsSize);
}

void VideoDecoder::decode(const uint8_t* nalData, size_t nalSize) {
    if (!m->ctx || !m->opened || !m->parser || nalSize == 0) return;

    std::lock_guard<std::mutex> lock(m->decodeMutex);

    // FFmpeg requires AV_INPUT_BUFFER_PADDING_SIZE bytes of zero padding.
    std::vector<uint8_t> padded(nalSize + AV_INPUT_BUFFER_PADDING_SIZE, 0);
    std::memcpy(padded.data(), nalData, nalSize);

    const uint8_t* data = padded.data();
    int remaining = static_cast<int>(nalSize);

    while (remaining > 0) {
        uint8_t* outBuf = nullptr;
        int outSize = 0;

        int consumed = av_parser_parse2(
            m->parser, m->ctx,
            &outBuf, &outSize,
            data, remaining,
            AV_NOPTS_VALUE, AV_NOPTS_VALUE, 0);

        if (consumed < 0) break;
        data += consumed;
        remaining -= consumed;

        if (outSize > 0) {
            m->pkt->data = outBuf;
            m->pkt->size = outSize;
            int ret = avcodec_send_packet(m->ctx, m->pkt);
            if (ret < 0 && ret != AVERROR(EAGAIN)) {
                static int errCount = 0;
                if (errCount++ < 5)
                    SPDLOG_ERROR("VideoDecoder: avcodec_send_packet error {}", ret);
                continue;
            }
            m->deliverDecodedFrames();
        }
    }
}

void VideoDecoder::flush() {
    if (!m->ctx || !m->opened) return;
    std::lock_guard<std::mutex> lock(m->decodeMutex);
    avcodec_send_packet(m->ctx, nullptr);
    m->deliverDecodedFrames();
}

} // namespace spyder

#endif // __ANDROID__

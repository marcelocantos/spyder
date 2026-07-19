// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

#include <player/VideoDecoder.h>

#import <VideoToolbox/VideoToolbox.h>
#import <CoreMedia/CoreMedia.h>
#import <CoreVideo/CoreVideo.h>
#include <spdlog/spdlog.h>

#include <vector>

namespace spyder {

struct VideoDecoder::M {
    FrameCallback callback;
    VTDecompressionSessionRef session = nullptr;
    CMVideoFormatDescriptionRef formatDesc = nullptr;

    ~M() {
        if (session) {
            VTDecompressionSessionWaitForAsynchronousFrames(session);
            VTDecompressionSessionInvalidate(session);
            CFRelease(session);
        }
        if (formatDesc) {
            CFRelease(formatDesc);
        }
    }

    void createSession();

    static void outputCallback(void* decompressionOutputRefCon,
                                void* sourceFrameRefCon,
                                OSStatus status,
                                VTDecodeInfoFlags infoFlags,
                                CVImageBufferRef imageBuffer,
                                CMTime presentationTimeStamp,
                                CMTime presentationDuration);
};

void VideoDecoder::M::createSession() {
    if (!formatDesc) {
        SPDLOG_ERROR("VideoDecoder: no format description, cannot create session");
        return;
    }

    // Clean up any existing session
    if (session) {
        VTDecompressionSessionWaitForAsynchronousFrames(session);
        VTDecompressionSessionInvalidate(session);
        CFRelease(session);
        session = nullptr;
    }

    // Request BGRA output
    NSDictionary* destImageBufferAttrs = @{
        (NSString*)kCVPixelBufferPixelFormatTypeKey: @(kCVPixelFormatType_32BGRA),
    };

    VTDecompressionOutputCallbackRecord callbackRecord{};
    callbackRecord.decompressionOutputCallback = &M::outputCallback;
    callbackRecord.decompressionOutputRefCon = this;

    OSStatus err = VTDecompressionSessionCreate(
        nullptr,                                          // allocator
        formatDesc,                                       // videoFormatDescription
        nullptr,                                          // videoDecoderSpecification
        (__bridge CFDictionaryRef)destImageBufferAttrs,    // destinationImageBufferAttributes
        &callbackRecord,                                  // outputCallback
        &session
    );

    if (err != noErr) {
        SPDLOG_ERROR("VTDecompressionSessionCreate failed: {}", static_cast<int>(err));
        session = nullptr;
        return;
    }

    SPDLOG_INFO("VideoDecoder: decompression session created");
}

void VideoDecoder::M::outputCallback(void* decompressionOutputRefCon,
                                      void* /*sourceFrameRefCon*/,
                                      OSStatus status,
                                      VTDecodeInfoFlags /*infoFlags*/,
                                      CVImageBufferRef imageBuffer,
                                      CMTime /*presentationTimeStamp*/,
                                      CMTime /*presentationDuration*/) {
    if (status != noErr) {
        SPDLOG_ERROR("VideoDecoder callback error: {}", static_cast<int>(status));
        return;
    }
    if (!imageBuffer) return;

    auto* self = static_cast<M*>(decompressionOutputRefCon);

    CVPixelBufferLockBaseAddress(imageBuffer, kCVPixelBufferLock_ReadOnly);

    int width = static_cast<int>(CVPixelBufferGetWidth(imageBuffer));
    int height = static_cast<int>(CVPixelBufferGetHeight(imageBuffer));
    size_t bytesPerRow = CVPixelBufferGetBytesPerRow(imageBuffer);
    const uint8_t* baseAddr = static_cast<const uint8_t*>(
        CVPixelBufferGetBaseAddress(imageBuffer));

    if (baseAddr) {
        VideoFrame f;
        f.format = VideoFrame::Format::BGRA;
        f.width = width;
        f.height = height;
        f.planes[0] = baseAddr;
        f.strides[0] = static_cast<int>(bytesPerRow);
        self->callback(f);
    }

    CVPixelBufferUnlockBaseAddress(imageBuffer, kCVPixelBufferLock_ReadOnly);
}

VideoDecoder::VideoDecoder(FrameCallback onFrame)
    : m(std::make_unique<M>()) {
    m->callback = std::move(onFrame);
}

VideoDecoder::~VideoDecoder() = default;

void VideoDecoder::setParameterSets(const uint8_t* sps, size_t spsSize,
                                     const uint8_t* pps, size_t ppsSize) {
    // Release old format description
    if (m->formatDesc) {
        CFRelease(m->formatDesc);
        m->formatDesc = nullptr;
    }

    const uint8_t* paramSets[2] = {sps, pps};
    size_t paramSizes[2] = {spsSize, ppsSize};

    OSStatus err = CMVideoFormatDescriptionCreateFromH264ParameterSets(
        nullptr,       // allocator
        2,             // parameterSetCount
        paramSets,     // parameterSetPointers
        paramSizes,    // parameterSetSizes
        4,             // NALUnitHeaderLength (AVCC uses 4-byte length prefix)
        &m->formatDesc
    );

    if (err != noErr) {
        SPDLOG_ERROR("CMVideoFormatDescriptionCreateFromH264ParameterSets failed: {}",
                     static_cast<int>(err));
        m->formatDesc = nullptr;
        return;
    }

    CMVideoDimensions dims = CMVideoFormatDescriptionGetDimensions(m->formatDesc);
    SPDLOG_INFO("VideoDecoder: SPS/PPS set, {}x{}", dims.width, dims.height);

    m->createSession();
}

void VideoDecoder::decode(const uint8_t* nalData, size_t nalSize) {
    if (!m->session || !m->formatDesc) return;

    // Input is Annex B format (0x00000001 start code + NAL).
    // VideoToolbox expects AVCC format (4-byte big-endian length + NAL).
    // Strip the start code and prepend the length.

    // Find the NAL body after the start code
    const uint8_t* nalBody = nullptr;
    size_t nalBodySize = 0;

    if (nalSize >= 4 && nalData[0] == 0x00 && nalData[1] == 0x00 &&
        nalData[2] == 0x00 && nalData[3] == 0x01) {
        nalBody = nalData + 4;
        nalBodySize = nalSize - 4;
    } else if (nalSize >= 3 && nalData[0] == 0x00 && nalData[1] == 0x00 &&
               nalData[2] == 0x01) {
        nalBody = nalData + 3;
        nalBodySize = nalSize - 3;
    } else {
        // No start code — assume raw NAL body
        nalBody = nalData;
        nalBodySize = nalSize;
    }

    if (nalBodySize == 0) return;

    // Build AVCC packet: 4-byte big-endian length + NAL body
    size_t avccSize = 4 + nalBodySize;
    std::vector<uint8_t> avccData(avccSize);
    uint32_t nalLen = static_cast<uint32_t>(nalBodySize);
    avccData[0] = static_cast<uint8_t>((nalLen >> 24) & 0xFF);
    avccData[1] = static_cast<uint8_t>((nalLen >> 16) & 0xFF);
    avccData[2] = static_cast<uint8_t>((nalLen >> 8) & 0xFF);
    avccData[3] = static_cast<uint8_t>(nalLen & 0xFF);
    std::memcpy(avccData.data() + 4, nalBody, nalBodySize);

    // Create CMBlockBuffer from the AVCC data
    CMBlockBufferRef blockBuffer = nullptr;
    OSStatus err = CMBlockBufferCreateWithMemoryBlock(
        nullptr,                      // allocator
        nullptr,                      // memoryBlock (nullptr = allocate)
        avccSize,                     // blockLength
        kCFAllocatorDefault,          // blockAllocator
        nullptr,                      // customBlockSource
        0,                            // offsetToData
        avccSize,                     // dataLength
        0,                            // flags
        &blockBuffer
    );

    if (err != noErr || !blockBuffer) {
        SPDLOG_ERROR("CMBlockBufferCreateWithMemoryBlock failed: {}", static_cast<int>(err));
        return;
    }

    err = CMBlockBufferReplaceDataBytes(avccData.data(), blockBuffer, 0, avccSize);
    if (err != noErr) {
        SPDLOG_ERROR("CMBlockBufferReplaceDataBytes failed: {}", static_cast<int>(err));
        CFRelease(blockBuffer);
        return;
    }

    // Create CMSampleBuffer
    CMSampleBufferRef sampleBuffer = nullptr;
    const size_t sampleSizeArray[1] = {avccSize};

    err = CMSampleBufferCreateReady(
        nullptr,                  // allocator
        blockBuffer,              // dataBuffer
        m->formatDesc,            // formatDescription
        1,                        // numSamples
        0,                        // numSampleTimingEntries
        nullptr,                  // sampleTimingArray
        1,                        // numSampleSizeEntries
        sampleSizeArray,          // sampleSizeArray
        &sampleBuffer
    );

    CFRelease(blockBuffer);

    if (err != noErr || !sampleBuffer) {
        SPDLOG_ERROR("CMSampleBufferCreateReady failed: {}", static_cast<int>(err));
        return;
    }

    // Decode
    VTDecodeInfoFlags flagsOut = 0;
    err = VTDecompressionSessionDecodeFrame(
        m->session,
        sampleBuffer,
        0,              // decodeFlags (synchronous)
        nullptr,        // sourceFrameRefCon
        &flagsOut
    );

    CFRelease(sampleBuffer);

    if (err != noErr) {
        SPDLOG_ERROR("VTDecompressionSessionDecodeFrame failed: {}", static_cast<int>(err));
    }
}

void VideoDecoder::flush() {
    if (m->session) {
        VTDecompressionSessionWaitForAsynchronousFrames(m->session);
    }
}

} // namespace spyder

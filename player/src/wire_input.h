// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// Input-validation guards for the brokered bridge's two untrusted-length wire
// paths. Header-only + pure so they are unit-testable without a live socket or
// decoder (src/wire_input_test.cpp). Both fixes trace to the Fable-5 audit
// (docs/audit/fable-2026-07.md, findings F5 / F6):
//
//   * decodeVideoStreamMessage — 🎯T142 (PlayerWireBridge::pump). Validates a
//     kVideoStreamMagic message's length field before it is used to compute
//     pointers/sizes, closing the unsigned underflow of (length - 5), the 32-bit
//     overflow of (8 + length), and the out-of-bounds seq / AVCC heap reads.
//
//   * wsPayloadWithinCap — 🎯T143 (WebSocketClient::recvFrame). Bounds a
//     wire-supplied 64-bit payload length by wire::kMaxMessageSize before any
//     allocation, covering both a single oversized frame and unbounded
//     fragmented-continuation accumulation.
#pragma once

#include <wire/Protocol.h>

#include <cstddef>
#include <cstdint>
#include <cstring>
#include <vector>

namespace spyder {
namespace detail {

// `data` is the full wire message: [u32 magic][u32 length][u8 flags][u32 seq]
// [avcc...]. `length` counts the bytes after the 8-byte MessageHeader, so a
// well-formed video-stream message has length >= 5 (the flags byte + seq) and a
// total size >= 8 + length. Returns false (caller skips the message) when the
// length field is inconsistent with the buffer; on success sets seq, avccData
// (a pointer into `data`) and avccSize. The caller has already matched the magic
// and so guarantees data.size() >= 8, but this re-checks it to stay self-contained.
inline bool decodeVideoStreamMessage(const std::vector<char>& data,
                                     uint32_t& seq,
                                     const uint8_t*& avccData,
                                     size_t& avccSize) {
    if (data.size() < 8) return false;  // no MessageHeader
    uint32_t length = 0;
    std::memcpy(&length, data.data() + 4, 4);
    // All arithmetic in 64-bit so (8 + length) cannot wrap. Requiring the fixed
    // 5-byte flags+seq prefix makes (length - 5) safe; 8 + 5 == 13 also
    // guarantees the seq read at offset 9 and the AVCC start at offset 13.
    if (length < 5) return false;
    if (static_cast<uint64_t>(8) + length > data.size()) return false;
    std::memcpy(&seq, data.data() + 9, 4);
    avccData = reinterpret_cast<const uint8_t*>(data.data()) + 13;
    avccSize = static_cast<size_t>(length) - 5;
    return true;
}

// Returns true iff appending a WebSocket frame of `payloadLen` bytes to a
// message already holding `accumulated` bytes keeps the running total within
// wire::kMaxMessageSize. Overflow-safe: the single-frame cap is checked first,
// so the accumulation comparison can never wrap.
inline bool wsPayloadWithinCap(uint64_t payloadLen, size_t accumulated) {
    if (payloadLen > wire::kMaxMessageSize) return false;
    // payloadLen <= kMaxMessageSize here, so the subtraction is non-negative.
    if (accumulated > wire::kMaxMessageSize - static_cast<size_t>(payloadLen)) return false;
    return true;
}

}  // namespace detail
}  // namespace spyder

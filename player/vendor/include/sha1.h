// Minimal SHA-1 implementation for WebSocket handshake.
// Public domain — based on RFC 3174.
#pragma once

#include <array>
#include <cstdint>
#include <cstring>
#include <string>
#include <vector>

namespace sha1 {

inline std::array<uint8_t, 20> hash(const void* data, size_t len) {
    uint32_t h0 = 0x67452301, h1 = 0xEFCDAB89, h2 = 0x98BADCFE,
             h3 = 0x10325476, h4 = 0xC3D2E1F0;

    // Pre-processing: pad to 64-byte blocks
    size_t padded = ((len + 8) / 64 + 1) * 64;
    std::vector<uint8_t> msg(padded, 0);
    std::memcpy(msg.data(), data, len);
    msg[len] = 0x80;
    uint64_t bits = len * 8;
    for (int i = 0; i < 8; ++i)
        msg[padded - 1 - i] = static_cast<uint8_t>(bits >> (i * 8));

    auto rotl = [](uint32_t x, int n) { return (x << n) | (x >> (32 - n)); };

    for (size_t offset = 0; offset < padded; offset += 64) {
        uint32_t w[80];
        for (int i = 0; i < 16; ++i)
            w[i] = (uint32_t(msg[offset + i*4]) << 24) |
                   (uint32_t(msg[offset + i*4+1]) << 16) |
                   (uint32_t(msg[offset + i*4+2]) << 8) |
                   uint32_t(msg[offset + i*4+3]);
        for (int i = 16; i < 80; ++i)
            w[i] = rotl(w[i-3] ^ w[i-8] ^ w[i-14] ^ w[i-16], 1);

        uint32_t a = h0, b = h1, c = h2, d = h3, e = h4;
        for (int i = 0; i < 80; ++i) {
            uint32_t f, k;
            if (i < 20)      { f = (b & c) | (~b & d);         k = 0x5A827999; }
            else if (i < 40) { f = b ^ c ^ d;                  k = 0x6ED9EBA1; }
            else if (i < 60) { f = (b & c) | (b & d) | (c & d); k = 0x8F1BBCDC; }
            else              { f = b ^ c ^ d;                  k = 0xCA62C1D6; }
            uint32_t t = rotl(a, 5) + f + e + k + w[i];
            e = d; d = c; c = rotl(b, 30); b = a; a = t;
        }
        h0 += a; h1 += b; h2 += c; h3 += d; h4 += e;
    }

    std::array<uint8_t, 20> result;
    for (int i = 0; i < 4; ++i) {
        result[i]    = (h0 >> (24 - i*8)) & 0xFF;
        result[4+i]  = (h1 >> (24 - i*8)) & 0xFF;
        result[8+i]  = (h2 >> (24 - i*8)) & 0xFF;
        result[12+i] = (h3 >> (24 - i*8)) & 0xFF;
        result[16+i] = (h4 >> (24 - i*8)) & 0xFF;
    }
    return result;
}

inline std::string base64(const uint8_t* data, size_t len) {
    static const char table[] =
        "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    std::string out;
    out.reserve((len + 2) / 3 * 4);
    for (size_t i = 0; i < len; i += 3) {
        uint32_t n = uint32_t(data[i]) << 16;
        if (i+1 < len) n |= uint32_t(data[i+1]) << 8;
        if (i+2 < len) n |= uint32_t(data[i+2]);
        out += table[(n >> 18) & 0x3F];
        out += table[(n >> 12) & 0x3F];
        out += (i+1 < len) ? table[(n >> 6) & 0x3F] : '=';
        out += (i+2 < len) ? table[n & 0x3F] : '=';
    }
    return out;
}

// Compute WebSocket Sec-WebSocket-Accept from client key.
inline std::string websocketAccept(const std::string& clientKey) {
    std::string concat = clientKey + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11";
    auto digest = hash(concat.data(), concat.size());
    return base64(digest.data(), digest.size());
}

} // namespace sha1

#define ASIO_STANDALONE
#include <asio.hpp>

#include <player/WebSocketClient.h>
#include <sha1.h>
#include <spdlog/spdlog.h>

#include "wire_input.h"

#include <mutex>
#include <sstream>
#include <vector>

using asio::ip::tcp;

namespace spyder {

// WebSocket connection backed by a TCP socket.
class TcpWsConnection : public WsConnection {
public:
    // Server-side: socket comes from the server's io_context (which outlives us).
    explicit TcpWsConnection(tcp::socket socket, bool serverSide = true)
        : socket_(std::move(socket)), serverSide_(serverSide) { setNoDelay(); }

    // Client-side: we own the io_context the socket was created on.
    // Optional preread data is prepended to future reads (handles asio::read_until over-read).
    TcpWsConnection(std::unique_ptr<asio::io_context> io, tcp::socket socket,
                    std::vector<char> preread = {})
        : ownedIo_(std::move(io)), socket_(std::move(socket)),
          serverSide_(false), preread_(std::move(preread)) { setNoDelay(); }

    void sendBinary(const void* data, size_t len) override {
        sendFrame(0x02, data, len);
    }

    void sendText(const std::string& text) override {
        sendFrame(0x01, text.data(), text.size());
    }

    bool recvBinary(std::vector<char>& out) override {
        return recvFrame(out);
    }

    void close() override {
        std::lock_guard lock(writeMtx_);
        if (open_) {
            open_ = false;
            asio::error_code ec;
            // Send close frame
            uint8_t closeFrame[2] = {0x88, 0x00};
            asio::write(socket_, asio::buffer(closeFrame, 2), ec);
            socket_.close(ec);
        }
    }

    bool isOpen() const override { return open_; }

    size_t available() override {
        if (!preread_.empty()) return preread_.size();
        asio::error_code ec;
        size_t n = socket_.available(ec);
        if (ec) { open_ = false; return 0; }
        // Peek at the socket to detect remote close or WebSocket close frame.
        char buf;
        auto fd = socket_.native_handle();
        ssize_t r = ::recv(fd, &buf, 1, MSG_PEEK | MSG_DONTWAIT);
        if (r == 0) { open_ = false; return 0; }  // TCP EOF
        if (r > 0 && (static_cast<uint8_t>(buf) & 0x0F) == 0x08) {
            // WebSocket close frame (opcode 0x8) -- mark connection as dead
            open_ = false;
            return 0;
        }
        if (r > 0 && n == 0) return 1;  // Data arrived between available() and peek
        return n;
    }

    tcp::socket& socket() { return socket_; }

private:
    void sendFrame(uint8_t opcode, const void* data, size_t len) {
        std::lock_guard lock(writeMtx_);
        if (!open_) return;

        // Build frame into a single buffer (header + optional mask + payload)
        // to avoid Nagle/delayed-ACK interaction from split writes.
        bool mask = !serverSide_;
        uint8_t maskBit = mask ? 0x80 : 0x00;

        size_t headerLen;
        uint8_t header[10];
        header[0] = 0x80 | opcode;  // FIN + opcode

        if (len < 126) {
            header[1] = maskBit | static_cast<uint8_t>(len);
            headerLen = 2;
        } else if (len < 65536) {
            header[1] = maskBit | 126;
            header[2] = (len >> 8) & 0xFF;
            header[3] = len & 0xFF;
            headerLen = 4;
        } else {
            header[1] = maskBit | 127;
            for (int i = 0; i < 8; ++i)
                header[2 + i] = (len >> (56 - i * 8)) & 0xFF;
            headerLen = 10;
        }

        size_t maskLen = mask ? 4 : 0;
        std::vector<uint8_t> buf(headerLen + maskLen + len);
        std::memcpy(buf.data(), header, headerLen);

        if (mask) {
            uint8_t maskKey[4] = {0x12, 0x34, 0x56, 0x78};
            std::memcpy(buf.data() + headerLen, maskKey, 4);
            auto* src = static_cast<const uint8_t*>(data);
            for (size_t i = 0; i < len; ++i)
                buf[headerLen + 4 + i] = src[i] ^ maskKey[i % 4];
        } else {
            std::memcpy(buf.data() + headerLen, data, len);
        }

        asio::error_code ec;
        asio::write(socket_, asio::buffer(buf), ec);
        if (ec) open_ = false;
    }

    // Read exactly len bytes, draining any pre-read data first.
    bool readExact(void* dest, size_t len) {
        auto* p = static_cast<char*>(dest);
        if (!preread_.empty()) {
            size_t n = std::min(preread_.size(), len);
            std::memcpy(p, preread_.data(), n);
            preread_.erase(preread_.begin(), preread_.begin() + n);
            p += n;
            len -= n;
        }
        if (len > 0) {
            asio::error_code ec;
            asio::read(socket_, asio::buffer(p, len), ec);
            if (ec) {
                // SO_RCVTIMEO fires EAGAIN — treat as a soft timeout, not a fatal error
                if (ec == asio::error::would_block || ec == asio::error::try_again) {
                    return false;
                }
                open_ = false;
                return false;
            }
        }
        return true;
    }

    bool recvFrame(std::vector<char>& out) {
        out.clear();

        // Reassemble fragmented messages
        while (true) {
            uint8_t header[2];
            if (!readExact(header, 2)) return false;

            bool fin = header[0] & 0x80;
            uint8_t opcode = header[0] & 0x0F;
            bool masked = header[1] & 0x80;
            uint64_t payloadLen = header[1] & 0x7F;

            if (payloadLen == 126) {
                uint8_t ext[2];
                if (!readExact(ext, 2)) return false;
                payloadLen = (uint64_t(ext[0]) << 8) | ext[1];
            } else if (payloadLen == 127) {
                uint8_t ext[8];
                if (!readExact(ext, 8)) return false;
                payloadLen = 0;
                for (int i = 0; i < 8; ++i)
                    payloadLen = (payloadLen << 8) | ext[i];
            }

            uint8_t maskKey[4] = {};
            if (masked) {
                if (!readExact(maskKey, 4)) return false;
            }

            size_t prevSize = out.size();
            // 🎯T143: bound the wire-supplied length by wire::kMaxMessageSize
            // before allocating — both a single oversized frame and unbounded
            // fragmented-continuation accumulation. Without this a hostile 127-
            // length frame drives out.resize() to an ~8 EB allocation (bad_alloc
            // DoS).
            if (!detail::wsPayloadWithinCap(payloadLen, prevSize)) {
                open_ = false;
                return false;
            }
            out.resize(prevSize + payloadLen);

            if (payloadLen > 0) {
                if (!readExact(out.data() + prevSize, payloadLen)) return false;

                if (masked) {
                    for (size_t i = 0; i < payloadLen; ++i)
                        out[prevSize + i] ^= maskKey[i % 4];
                }
            }

            // Handle control frames
            if (opcode == 0x08) {  // Close
                open_ = false;
                return false;
            }
            if (opcode == 0x09) {  // Ping -> Pong
                sendFrame(0x0A, out.data() + prevSize, payloadLen);
                out.resize(prevSize);  // Remove ping payload
                // If no more data is available, return false (non-blocking)
                // instead of blocking on the next readExact. This prevents
                // the main thread from hanging after handling a ping.
                {
                    asio::error_code ec;
                    if (socket_.available(ec) == 0 || ec) return false;
                }
                continue;
            }
            if (opcode == 0x0A) {  // Pong -- ignore
                out.resize(prevSize);
                {
                    asio::error_code ec;
                    if (socket_.available(ec) == 0 || ec) return false;
                }
                continue;
            }

            if (fin) break;  // Complete message
        }

        return true;
    }

    void setSendTimeout(int ms) override {
        struct timeval tv;
        tv.tv_sec = ms / 1000;
        tv.tv_usec = (ms % 1000) * 1000;
        setsockopt(socket_.native_handle(), SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof(tv));
    }

    void setRecvTimeout(int ms) override {
        struct timeval tv;
        tv.tv_sec = ms / 1000;
        tv.tv_usec = (ms % 1000) * 1000;
        setsockopt(socket_.native_handle(), SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
    }

    void setNoDelay() {
        asio::error_code ec;
        socket_.set_option(tcp::no_delay(true), ec);
    }

    std::unique_ptr<asio::io_context> ownedIo_;  // before socket_ so it outlives it
    tcp::socket socket_;
    std::mutex writeMtx_;
    bool serverSide_;
    std::vector<char> preread_;  // bytes over-read during HTTP upgrade
    bool open_ = true;
};

std::shared_ptr<WsConnection> connectWebSocket(
    const std::string& host, uint16_t port, const std::string& path,
    int connectTimeoutMs)
{
    try {
        auto io = std::make_unique<asio::io_context>();
        tcp::resolver resolver(*io);
        auto endpoints = resolver.resolve(host, std::to_string(port));

        tcp::socket socket(*io);

        if (connectTimeoutMs > 0) {
            // Async connect with timeout
            asio::steady_timer timer(*io);
            timer.expires_after(std::chrono::milliseconds(connectTimeoutMs));

            asio::error_code connectEc;
            bool timedOut = false;

            asio::async_connect(socket, endpoints,
                [&](const asio::error_code& ec, const tcp::endpoint&) {
                    connectEc = ec;
                    timer.cancel();
                });

            timer.async_wait([&](const asio::error_code& ec) {
                if (!ec) {  // timer expired (not canceled)
                    timedOut = true;
                    socket.close();
                }
            });

            io->run();

            if (timedOut) {
                SPDLOG_WARN("WebSocket connect timed out after {}ms", connectTimeoutMs);
                return nullptr;
            }
            if (connectEc) {
                SPDLOG_WARN("WebSocket connect failed: {}", connectEc.message());
                return nullptr;
            }

            // Reset io_context for subsequent synchronous operations
            io->restart();
        } else {
            asio::connect(socket, endpoints);
        }

        // Generate a client key (16 bytes base64-encoded)
        std::string clientKey = sha1::base64(
            reinterpret_cast<const uint8_t*>("spyder-ws-client"), 16);

        // Send WebSocket upgrade request
        std::ostringstream req;
        req << "GET " << path << " HTTP/1.1\r\n"
            << "Host: " << host << ":" << port << "\r\n"
            << "Upgrade: websocket\r\n"
            << "Connection: Upgrade\r\n"
            << "Sec-WebSocket-Key: " << clientKey << "\r\n"
            << "Sec-WebSocket-Version: 13\r\n"
            << "\r\n";
        auto reqStr = req.str();
        asio::write(socket, asio::buffer(reqStr));

        // Read upgrade response
        asio::streambuf buf;
        asio::read_until(socket, buf, "\r\n\r\n");

        std::istream is(&buf);
        std::string line;
        std::getline(is, line);
        if (line.find("101") == std::string::npos) {
            SPDLOG_WARN("WebSocket upgrade rejected: {}", line);
            return nullptr;
        }

        // Drain remaining header lines
        while (std::getline(is, line) && line != "\r" && !line.empty()) {}

        // Extract any bytes read_until consumed beyond the HTTP headers.
        // These belong to the first WebSocket frame and must not be lost.
        std::vector<char> preread;
        if (buf.size() > 0) {
            auto data = buf.data();
            auto* p = static_cast<const char*>(data.data());
            preread.assign(p, p + buf.size());
        }

        return std::make_shared<TcpWsConnection>(
            std::move(io), std::move(socket), std::move(preread));
    } catch (const std::exception& e) {
        SPDLOG_WARN("WebSocket connect failed: {}", e.what());
        return nullptr;
    }
}

} // namespace spyder

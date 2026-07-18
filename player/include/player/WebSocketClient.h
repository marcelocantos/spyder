// WebSocket client: connection interface and connectWebSocket() helper.
#pragma once

#include <player/Linalg.h>

#include <cstdint>
#include <memory>
#include <string>
#include <vector>

namespace spyder {

// A WebSocket connection (upgraded from HTTP).
class WsConnection {
public:
    virtual ~WsConnection() = default;
    virtual void sendBinary(const void* data, size_t len) = 0;
    virtual void sendText(const std::string& text) = 0;
    virtual bool recvBinary(std::vector<char>& out) = 0;  // blocks, returns false on close
    virtual void close() = 0;
    virtual bool isOpen() const = 0;
    virtual size_t available() = 0;  // TCP bytes available (nonzero = frame ready)
    virtual void setSendTimeout(int ms) { (void)ms; }
    virtual void setRecvTimeout(int ms) { (void)ms; }
};

// Connect to a WebSocket endpoint as a client. Returns null on failure.
// connectTimeoutMs > 0 caps the TCP connect phase; 0 = OS default (~75s).
std::shared_ptr<WsConnection> connectWebSocket(
    const std::string& host, uint16_t port, const std::string& path,
    int connectTimeoutMs = 0);

} // namespace spyder

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// Browser WsConnection backend (🎯T101.2). The wire runs over the browser
// WebSocket API via emscripten/websocket.h; the browser owns framing,
// masking, fragmentation reassembly, and ping/pong, so this backend is a
// message queue with Asyncify-friendly blocking semantics:
//
//   - onmessage appends whole binary messages to a FIFO.
//   - available() reports queued bytes (nonzero = frame ready), matching
//     the poll contract PlayerWireBridge::pump relies on.
//   - recvBinary() pops the head; when the queue is empty it blocks by
//     yielding to the browser event loop (emscripten_sleep), which is how
//     the connect()-time SessionConfig wait works under ASYNCIFY.
//
// Everything runs on the browser main thread — no locking.

#ifdef __EMSCRIPTEN__

#include <player/WebSocketClient.h>

#include <emscripten/emscripten.h>
#include <emscripten/websocket.h>
#include <spdlog/spdlog.h>

#include <deque>
#include <vector>

namespace spyder {

namespace {

class EmWsConnection : public WsConnection {
public:
    explicit EmWsConnection(EMSCRIPTEN_WEBSOCKET_T ws) : ws_(ws) {}

    ~EmWsConnection() override { close(); }

    void sendBinary(const void* data, size_t len) override {
        if (!open_) return;
        if (emscripten_websocket_send_binary(ws_, const_cast<void*>(data),
                                             static_cast<uint32_t>(len)) < 0)
            open_ = false;
    }

    void sendText(const std::string& text) override {
        if (!open_) return;
        if (emscripten_websocket_send_utf8_text(ws_, text.c_str()) < 0)
            open_ = false;
    }

    bool recvBinary(std::vector<char>& out) override {
        // Block until a message arrives or the socket dies. Yielding via
        // emscripten_sleep lets the browser deliver onmessage callbacks;
        // requires ASYNCIFY (the wasm leg links with -sASYNCIFY).
        while (queue_.empty()) {
            if (!open_) return false;
            emscripten_sleep(4);
        }
        out = std::move(queue_.front());
        queue_.pop_front();
        queuedBytes_ -= out.size();
        return true;
    }

    void close() override {
        if (ws_ >= 0) {
            emscripten_websocket_close(ws_, 1000, "player close");
            emscripten_websocket_delete(ws_);
            ws_ = -1;
        }
        open_ = false;
    }

    bool isOpen() const override { return open_; }

    size_t available() override {
        // Passive: onmessage callbacks are delivered during the main
        // loop's per-iteration yield (RAF in player_core). Yielding here
        // via setTimeout(0) was catastrophic under WebKit's hidden-page
        // timer throttling (1 s per yield → 0.3 Hz main loop).
        return queuedBytes_;
    }

    // Callback plumbing (static trampolines bind through userData).
    void onOpen() { open_ = true; }
    void onMessage(const uint8_t* data, uint32_t len, bool isBinary) {
        if (!isBinary) return;  // wire is binary-only; ignore text frames
        queue_.emplace_back(reinterpret_cast<const char*>(data),
                            reinterpret_cast<const char*>(data) + len);
        queuedBytes_ += len;
    }
    void onCloseOrError() { open_ = false; }

    bool opened() const { return open_; }

private:
    EMSCRIPTEN_WEBSOCKET_T ws_ = -1;
    std::deque<std::vector<char>> queue_;
    size_t queuedBytes_ = 0;
    bool open_ = false;
};

EM_BOOL wsOnOpen(int, const EmscriptenWebSocketOpenEvent*, void* user) {
    static_cast<EmWsConnection*>(user)->onOpen();
    return EM_TRUE;
}

EM_BOOL wsOnMessage(int, const EmscriptenWebSocketMessageEvent* e, void* user) {
    static_cast<EmWsConnection*>(user)->onMessage(e->data, e->numBytes,
                                                  !e->isText);
    return EM_TRUE;
}

EM_BOOL wsOnError(int, const EmscriptenWebSocketErrorEvent*, void* user) {
    SPDLOG_WARN("WebSocket: error event");
    static_cast<EmWsConnection*>(user)->onCloseOrError();
    return EM_TRUE;
}

EM_BOOL wsOnClose(int, const EmscriptenWebSocketCloseEvent* e, void* user) {
    SPDLOG_WARN("WebSocket: close event code={} clean={} reason={}",
                e->code, int(e->wasClean), e->reason);
    static_cast<EmWsConnection*>(user)->onCloseOrError();
    return EM_TRUE;
}

} // namespace

std::shared_ptr<WsConnection> connectWebSocket(
    const std::string& host, uint16_t port, const std::string& path,
    int connectTimeoutMs)
{
    if (!emscripten_websocket_is_supported()) {
        SPDLOG_ERROR("WebSocket: not supported in this browser context");
        return nullptr;
    }

    const std::string url =
        "ws://" + host + ":" + std::to_string(port) + path;

    EmscriptenWebSocketCreateAttributes attrs;
    emscripten_websocket_init_create_attributes(&attrs);
    attrs.url = url.c_str();
    attrs.createOnMainThread = EM_TRUE;

    EMSCRIPTEN_WEBSOCKET_T ws = emscripten_websocket_new(&attrs);
    if (ws <= 0) {
        SPDLOG_ERROR("WebSocket: create failed for {} ({})", url, ws);
        return nullptr;
    }

    auto conn = std::make_shared<EmWsConnection>(ws);
    emscripten_websocket_set_onopen_callback(ws, conn.get(), wsOnOpen);
    emscripten_websocket_set_onmessage_callback(ws, conn.get(), wsOnMessage);
    emscripten_websocket_set_onerror_callback(ws, conn.get(), wsOnError);
    emscripten_websocket_set_onclose_callback(ws, conn.get(), wsOnClose);

    // Block (yielding) until open or timeout. 0 = the native path's "OS
    // default"; use a generous 10 s cap in the browser.
    const int capMs = connectTimeoutMs > 0 ? connectTimeoutMs : 10000;
    for (int waited = 0; !conn->opened(); waited += 10) {
        if (waited >= capMs) {
            SPDLOG_WARN("WebSocket: connect timed out after {}ms ({})",
                        capMs, url);
            conn->close();
            return nullptr;
        }
        unsigned short readyState = 0;
        emscripten_websocket_get_ready_state(ws, &readyState);
        if (readyState == 3 /* CLOSED */) {
            SPDLOG_WARN("WebSocket: connect failed ({})", url);
            conn->close();
            return nullptr;
        }
        emscripten_sleep(10);
    }
    SPDLOG_INFO("WebSocket: connected ({})", url);
    return conn;
}

} // namespace spyder

#endif // __EMSCRIPTEN__

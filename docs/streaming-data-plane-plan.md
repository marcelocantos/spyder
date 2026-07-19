# T92.2 dev streaming data plane — implementation plan

Status (2026-07-05): **T91.4 relay DONE + committed** (spyder side). The ge
server streaming, the browser player, and input translation remain — scoped
here to the line so the next focused session executes without re-investigation.

Boundary principle: **ge owns the codec + render + input; spyder is a
payload-agnostic byte pipe.** LAN/trusted dev only, no authn (same trust model
as the app-channel). A future production stream is a separate clean-slate
design — do NOT accommodate it here.

**Multi-rung (🎯T97 / ge 🎯T128):** the relay does **not** inspect
`MessageHeader.magic`. H.264 (`SP2V`) and command-stream (`SP2S`) are end-to-end
rungs negotiated by ge player + server; spyder only pipes binary messages and
counts opaque frames/bytes. Class-1 oracle:
`TestRelay_PipesCommandStreamMagic`.

## Architecture (what plugs where)

```
ge server instance ──H.264──▶ spyder relay ──H.264──▶ dashboard browser player
(STREAM mode)    ◀──input── (T91.4, done) ◀──input── (WebCodecs)
```

## DONE — spyder relay (T91.4), `internal/streamrelay/relay.go`

Implements the stream relay role (formerly ged's), speaking ge's existing brokered wire:
- `GET /ws/server?name=<name>` — server control (JSON sideband)
- `GET /ws/server/wire/<id>` — per-session video wire (binary)
- `GET /stream/player/<name>` — browser player attaches (binary)
- `GET /stream/servers` — JSON list for the dashboard

On player attach: allocate session id → `{"type":"player_attached","session_id":id}`
to the server control socket → server dials the matching wire → pipe frames
wire→player and input player→wire verbatim → `player_detached` on close. Wired
into the daemon mux. Oracle: `relay_test.go` (test-double server+player).
Uses `github.com/coder/websocket`.

## ge WIRE PROTOCOL (mapped — do not re-derive)

Binary messages are `wire::MessageHeader{uint32 magic, uint32 length}` + payload
(`include/ge/Protocol.h`):
- `kVideoStreamMagic` "SP2V" (server→player): header + `uint8 flags`(bit0=keyframe)
  + `uint32 seq` + H.264 bytes. Encoder output is **complete encoded frames**
  (CMSampleBuffer data), VideoToolbox H.264 High profile, low-latency
  (`VideoEncoder_apple.mm`). NOTE: verify Annex-B vs AVCC + whether SPS/PPS ride
  keyframes — the browser `VideoDecoder` config depends on it.
- `kSdlEventMagic` "SP2I" (player→server): header + raw `SDL_Event` struct.
- `kDeviceInfoMagic` "SP2D" (player→server): header + `wire::DeviceInfo`
  {width,height,pixelRatio,deviceClass} — the player tells the server its dims;
  the server renders at `width/2`.
- `kSessionConfigMagic` "SP2C" (server→player): header + `wire::SessionConfig`
  {sensors,orientation}.
- Sideband JSON (server→spyder): `{"type":"hello","name","pid","version"}`;
  spyder→server: `player_attached` / `player_detached` with `session_id`.

Server dials (currently hardcoded `localhost:42069` in
`SessionHost_brokered.mm:65,108`). WebSocket client = `connectWebSocket(host,
port, path, timeoutMs)` → `WsConnection` (sendBinary/sendText/recvBinary/
available/isOpen), `include/ge/WebSocketClient.h`.

## TODO — ge server streaming (the hard core)

The brokered render capture is **stubbed** (`ServerWireBridge.mm`
`submitCaptureBlit`/`readCapturedFrame` are no-ops — the T34 sokol offscreen
readback was never finished). Rather than finish per-session offscreen render,
use the **direct-mirror** path: the DIRECT render already presents each frame,
and `SokolContext::captureNextFrame(sink)` (the primitive `app_screenshot`
uses) delivers RGBA on the game thread. So:

1. **Stream hook (COMMON)** — `src/render/ScreenshotBridge.h` + impl in
   `DirectRenderHost.mm` (where the screenshot-bridge globals live):
   add `setStreamFrameSink(sink)`, `streamActive()`, `deliverStreamFrame(rgba,w,h)`.
   In the swapchain-pass teardown (`DirectRenderHost.mm:472-479`), beside the
   `screenshotArmed()` arm, add `else if (streamActive()) captureNextFrame(deliverStreamFrame)`.
   ⚠ Capture is likely **BGRA** (Metal swapchain), `VideoEncoder::encode` wants
   BGRA — confirm the sink's byte order; the `rgba` param name may be a
   misnomer. Swap only if needed.

2. **StreamClient (BROKERED, new)** — `include/ge/StreamClient.h` +
   `src/stream/StreamClient.mm`; add to `GE_SRC_BROKERED` in
   `tools/ge-sources.mk`. Responsibilities:
   - Background thread: `connectWebSocket(host,port,"/ws/server?name="+name)`;
     send hello; poll sideband for `player_attached` → open
     `/ws/server/wire/<id>`; send `SessionConfig`; loop `recvBinary` for
     `DeviceInfo` (→ set encoder dims) and `SDL_Event` (→ input callback).
   - `pushFrame(rgba,w,h)` (game thread, from `deliverStreamFrame`): lazily
     build `VideoEncoder(w,h,60, onFrame)`; `encode(pixels, w*4)`. The `onFrame`
     callback frames per `kVideoStreamMagic` and `wire->sendBinary`.
   - ⚠ Threading: encoder callback fires on a VT thread; the wire is also read
     on the bg thread. Guard `sendBinary` with a mutex, or post frames to the
     bg thread via a queue. Don't block the game thread on the socket.
   - First cut: single player/session.

3. **STREAM mode** — `sample/tiltbuggy/src/main.cpp` (mirror the FACTORY
   block): if `getenv("STREAM")`, construct a `StreamClient(host,port,name)`
   with an input handler that `SDL_PushEvent`s the received events, install its
   frame sink, and fall through to the normal `ge::run` (direct). Spyder injects
   `STREAM=<daemon-host:port>` at launch (the relay lives on the daemon's
   HTTP addr, e.g. 127.0.0.1:3030). The local window shows the game; the remote
   player mirrors it and drives it.

4. **Oracle (spyder, class-1)** — a gated live test: `launch_app(desktop, env
   STREAM=<addr>)`, then connect to `/stream/player/<name>` and assert
   ordered, valid H.264 access units arrive (check NAL start codes / non-empty
   keyframe first), and that a synthetic input reaches the game (observe via
   `app_state`). No browser needed for the class-1 gate.

## TODO — dashboard browser player (WebCodecs)

A "Stream" panel in `internal/dashboard/index.html`:
- List servers from `GET /stream/servers`; on select, `new WebSocket(
  "ws://"+location.host+"/stream/player/"+name)` (binary).
- Parse each message: `MessageHeader` (magic,length) → for `kVideoStreamMagic`,
  strip flags+seq → feed the H.264 to `VideoDecoder` (WebCodecs) as an
  `EncodedVideoChunk` (type: key/delta from the flag) → draw the `VideoFrame`
  to a `<canvas>`. Decoder `configure({codec:"avc1.<profile>", ...})` — build
  the description from SPS/PPS if AVCC, or feed Annex-B directly (WebCodecs
  accepts Annex-B when no `description` is given).
- On first frame, send a `kDeviceInfoMagic` message with the canvas dims so the
  server sizes its render.
- Input: canvas keyboard/mouse listeners → **translate to `SDL_Event` bytes**
  (`kSdlEventMagic` + the struct) → WebSocket. This is the fiddly part — the
  wire carries raw `SDL_Event` structs (arch/layout-specific). Options: (a)
  hand-pack the few event types needed (SDL_EVENT_MOUSE_*, KEY_*) matching
  SDL3's struct layout; (b) add a small JSON input format to ServerWireBridge's
  event path so the browser sends `{type,x,y,...}` and the server builds the
  SDL_Event — cleaner, a small ge change. Prefer (b).

## Risks / open questions

- H.264 format (Annex-B vs AVCC, SPS/PPS placement) — gates the WebCodecs config.
- BGRA/RGBA byte order from the capture sink.
- Per-frame 60fps GPU→CPU readback + encode cost (dev-acceptable, but watch it).
- Threading around the shared wire (frame send vs input recv).
- Input struct layout across the browser boundary → prefer a JSON input format (b).

## Sequencing

relay (done) → ge server streaming (1–3) → class-1 oracle (4) → browser player
→ input (prefer JSON format) → perceptual acceptance in the dashboard. the old ged daemon is fully retired (T91.4 was its last pin).

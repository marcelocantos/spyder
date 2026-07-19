#pragma once

#include <player/Linalg.h>

#include <SDL3/SDL_video.h>

#include <bit>
#include <cstdint>
#include <cstddef>

static_assert(std::endian::native == std::endian::little, "Little-endian required");

// Wire protocol for the streaming dev mode (H.264 baseline + command-stream rung).
// The server either encodes H.264 (SP2V) or serialises a sokol command stream
// (SP2S); the player decodes/replays and forwards SDL input over the same
// spyder-brokered WebSocket. Rung negotiation is end-to-end (see DeviceInfo
// capabilities + SessionConfig.transport); the relay is magic-agnostic.
//
// The Dawn wire protocol that previously lived here has been removed along
// with the rest of the Dawn/WebGPU dependency.
namespace wire {

// Magic numbers for message type identification (ASCII: "SP2x")
constexpr uint32_t kDeviceInfoMagic     = 0x53503244;  // "SP2D" — player → relay: player dimensions/class
constexpr uint32_t kSdlEventMagic       = 0x53503249;  // "SP2I" — player → server: SDL input event
constexpr uint32_t kSessionEndMagic     = 0x5350324D;  // "SP2M" — relay → player: server disconnected
constexpr uint32_t kServerAssignedMagic = 0x5350324E;  // "SP2N" — relay → player: assigned server name
constexpr uint32_t kCommandStreamMagic  = 0x53503253;  // "SP2S" — server → player: cmdstream ops (T128)
constexpr uint32_t kSqlpipeMsgMagic     = 0x53503254;  // "SP2T" — bidirectional sqlpipe messages
constexpr uint32_t kVideoStreamMagic    = 0x53503256;  // "SP2V" — server → relay: H.264 NALs
constexpr uint32_t kStreamStartMagic    = 0x53503257;  // "SP2W" — relay → player: start streaming
constexpr uint32_t kStreamStopMagic     = 0x53503258;  // "SP2X" — relay → player: stop streaming
constexpr uint32_t kSafeAreaMagic       = 0x53503245;  // "SP2E" — player → server: safe area update
constexpr uint32_t kLifecycleMagic      = 0x5350324C;  // "SP2L" — player → server: viewer lifecycle
constexpr uint32_t kAspectLockMagic     = 0x53503260;  // "SP2`" — server → player: lock aspect ratio
constexpr uint32_t kSessionConfigMagic  = 0x53503243;  // "SP2C" — server → player: session requirements
constexpr uint32_t kArmStateMagic       = 0x53503241;  // "SP2A" — server → primary glass: AccelSynth arm state (🎯T158)
constexpr uint32_t kFrameMetaMagic      = 0x53503246;  // "SP2F" — server → player: per-frame emit metadata (🎯T159)

// 🎯T158: server → primary glass. The server owns AccelSynth and its arm
// policy; the glass follows this signal for relative-mouse delivery only
// (no tilt semantics on the glass). Sent on transitions, primary seat only.
struct ArmState {
    uint32_t magic = kArmStateMagic;
    uint8_t  armed = 0;   // 1 = synth armed for this seat's gesture
    uint8_t  _pad[3] = {};
};

// 🎯T159: server → player, immediately before each cmdstream frame on the
// same ordered stream. serverUs is unix-epoch microseconds at emit; the
// glass logs (seq, serverUs, presentUs) for absolute end-to-end latency.
// Same-host runs (loopback oracle) share a clock and check a documented
// tolerance; cross-device runs are informative (NTP skew applies).
struct FrameMeta {
    uint32_t magic = kFrameMetaMagic;
    uint32_t seq = 0;       // cmdstream frame sequence this precedes
    uint64_t serverUs = 0;  // emit time, unix epoch microseconds
};

// v7: DeviceInfo.capabilities + SessionConfig.transport (command-stream ladder).
// v8: dual safe rects on DeviceInfo / SafeAreaUpdate (draw vs ui).
// v9: SP2A arm-state (delivery plumbing) + SP2F frame metadata (latency
//     telemetry). Both optional: pre-v9 peers simply never see/send them.
constexpr uint16_t kProtocolVersion = 9;
constexpr size_t   kMaxMessageSize = 512 * 1024 * 1024;  // 512MB (matches ged/bridge.go)

// DeviceInfo.capabilities bits (player → server).
constexpr uint8_t kCapCommandStream = 1u << 0;  // player can replay SP2S
constexpr uint8_t kCapDualSafe      = 1u << 1;  // drawSafe* fields present and meaningful
constexpr uint8_t kCapHasAccelerometer = 1u << 2;  // glass has a real accelerometer (sensor authority)

// SessionConfig.transport (server → player): selected rung after intersection.
constexpr uint8_t kTransportH264          = 0;
constexpr uint8_t kTransportCommandStream = 1;

// Sent by player after connecting to the game server (via the stream relay).
//
// Safe rects are in window pixels. Two contracts (🎯T154):
//   safe*     — ui-safe (cutouts + gesture / tappable chrome)
//   drawSafe* — draw-safe (display cutouts only); if drawSafeW==0, draw = ui
// Older peers omit trailing fields; server must tolerate short payloads.
struct DeviceInfo {
    uint32_t magic = kDeviceInfoMagic;
    uint16_t version = kProtocolVersion;
    uint16_t width;           // Device width in pixels
    uint16_t height;          // Device height in pixels
    uint16_t pixelRatio;      // Device pixel ratio (e.g., 3 for retina)
    uint8_t  deviceClass = 0; // 0=unknown, 1=phone, 2=tablet, 3=desktop
    uint8_t  orientation = 0; // SDL_DisplayOrientation value (0-4)
    uint16_t safeX = 0;       // UI-safe left edge in pixels
    uint16_t safeY = 0;       // UI-safe top edge in pixels
    uint16_t safeW = 0;       // UI-safe width (0 = full width)
    uint16_t safeH = 0;       // UI-safe height (0 = full height)
    // v7+: player capability advertisement (kCap*).
    uint8_t  capabilities = 0;
    uint8_t  _capPad[3] = {};
    // v8+: draw-safe (cutouts only). drawSafeW==0 → server uses safe* for both.
    uint16_t drawSafeX = 0;
    uint16_t drawSafeY = 0;
    uint16_t drawSafeW = 0;
    uint16_t drawSafeH = 0;
};

// Safe area update (player → server, sent on orientation / chrome change).
// v8 adds drawSafe*; short payloads leave draw = ui.
struct SafeAreaUpdate {
    uint32_t magic = kSafeAreaMagic;
    uint16_t safeX;       // UI-safe
    uint16_t safeY;
    uint16_t safeW;
    uint16_t safeH;
    uint16_t drawSafeX = 0;
    uint16_t drawSafeY = 0;
    uint16_t drawSafeW = 0;
    uint16_t drawSafeH = 0;
};

// Viewer lifecycle (player → server). 🎯T154: game lifecycle follows the glass.
constexpr uint8_t kLifeForeground   = 1;
constexpr uint8_t kLifeBackground   = 2;
constexpr uint8_t kLifeBackPressed  = 3;
constexpr uint8_t kLifeMemory       = 4;
constexpr uint8_t kLifeAudioLost    = 5;
constexpr uint8_t kLifeAudioGained  = 6;

struct ViewerLifecycle {
    uint32_t magic = kLifecycleMagic;
    uint8_t  kind = 0;       // kLife*
    uint8_t  memoryLevel = 0; // MemoryPressureLevel when kind == kLifeMemory
    uint8_t  _pad[2] = {};
};

// Server → player: lock window aspect ratio. Send 0.0 to unlock.
struct AspectLock {
    uint32_t magic = kAspectLockMagic;
    float ratio;  // width/height (e.g. 0.6948 for 954:1373), 0 = unlock
};

// Server → player: session requirements (sensors, orientation, transport,
// chrome policy).
//
// Handshake order (invariant):
//   1. SessionHostConfig in spyder::run is fixed at process start (seed).
//   2. SessionConfig is the first app payload on the wire (server → player).
//   3. Player applies it fully, then measures DeviceInfo once from the
//      configured surface. Safe rects are not computed before step 2/3.
// Mid-session SafeAreaUpdate is for orientation/resize only.
//
// flags (v8+ / 🎯T154): former pad byte. Older peers leave it 0.
struct SessionConfig {
    uint32_t magic = kSessionConfigMagic;
    uint8_t  sensors;       // Bitmask: kSensorAccelerometer
    uint8_t  orientation;   // kOrientation* value to lock, 0 = no lock
    uint8_t  transport = kTransportH264; // kTransport* selected rung
    uint8_t  flags = 0;     // kSessionFlag*
};

constexpr uint8_t kSensorAccelerometer = 1;

// SessionConfig.flags
constexpr uint8_t kSessionFlagImmersive       = 1u << 0; // hide status/nav chrome
constexpr uint8_t kSessionFlagNoScreenSaver   = 1u << 1; // keep display awake

// Orientation constants — assigned from SDL_DisplayOrientation.
//
// As of v0.31.0 (🎯T36) these constants are authoritative — passing
// e.g. `kOrientationPortrait` makes the engine narrow
// `supportedInterfaceOrientations` to portrait at runtime, so iOS
// will rotate the UI to portrait at launch even if the device is held
// in landscape and the plist allows all four orientations. The plist's
// `UISupportedInterfaceOrientations` becomes the fallback (used when
// the consumer hasn't requested a specific lock), not the gate.
//
// On iPadOS 26+ the swizzled `prefersInterfaceOrientationLocked`
// (Apple TN3192) freezes the post-launch orientation against the
// multitasking swivel gesture — same mechanism as before, the
// difference is the SPECIFIC orientation now matches what the
// consumer asked for.
//
// "Either landscape, lock at launch" (the tilt-game case where the
// player flips the device freely) is `kOrientationAnyLandscape`.
// Use it when accelerometer-driven gameplay needs the launch
// orientation to win regardless of left/right.
constexpr uint8_t kOrientationLandscape        = SDL_ORIENTATION_LANDSCAPE;
constexpr uint8_t kOrientationLandscapeFlipped = SDL_ORIENTATION_LANDSCAPE_FLIPPED;
constexpr uint8_t kOrientationPortrait         = SDL_ORIENTATION_PORTRAIT;
constexpr uint8_t kOrientationPortraitFlipped  = SDL_ORIENTATION_PORTRAIT_FLIPPED;
// "Lock at launch to whichever landscape the device is in; reject
// mid-play rotations." Distinct from the specific constants above,
// which force one specific landscape.
constexpr uint8_t kOrientationAnyLandscape     = 0xFE;

// Header for binary wire messages.
struct MessageHeader {
    uint32_t magic;
    uint32_t length;  // Payload length in bytes
};

} // namespace wire

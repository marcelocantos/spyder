// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

#pragma once
#include <string>

// Spyder player core — H.264 stream glass.
// Connects to the stream relay (spyder serve) at host:port, pairs with the
// server registered under `serverName`, decodes H.264 via VideoDecoder,
// renders via SDL, forwards input. Blocks until quit.
// Returns 0 on success, non-zero on error.
// 🎯T156.7 headless oracle mode: full wire bridge (SessionConfig apply,
// DeviceInfo report incl. capability bits, SP2I forwarding, SP2T) with no
// GPU/GUI presentation. Scripted device-local input is injected at the
// upstream-event boundary; declared device facts are configurable per run.
struct PlayerOptions {
    bool headless = false;
    std::string scriptPath;        // InputScript file (see src/InputScript.h)
    std::string tracePath;         // JSONL trace of injected/observed traffic
    int accelOverride = -1;        // -1 auto, 0 declare-none, 1 declare-present
    int deviceClassOverride = -1;  // wire deviceClass (1 phone, 2 tablet, 3 desktop)
    bool exitAfterScript = false;  // quit once the script has fully played
    int initialW = 0, initialH = 0; // glass geometry override (oracle parity)
};

int playerCore(const std::string& host, int port,
               const std::string& serverName = "server",
               const PlayerOptions& opts = {});

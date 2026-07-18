// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// Compile-time / runtime oracle for the stream wire layout the spyder
// player speaks. Ge's server encoder must match these sizes and magics
// (protocol-only coupling). Run via `make -C player check-wire`.

#include <wire/Protocol.h>

#include <cstdio>
#include <cstdlib>
#include <type_traits>

#define CHECK(cond)                                                            \
    do {                                                                       \
        if (!(cond)) {                                                         \
            std::fprintf(stderr, "check-wire-layout FAIL: %s (%s:%d)\n", #cond,  \
                         __FILE__, __LINE__);                                  \
            std::exit(1);                                                      \
        }                                                                      \
    } while (0)

int main() {
    using namespace wire;

    // Magics — must match the game server's wire encoder (SP2* on the wire).
    CHECK(kVideoStreamMagic == 0x53503256u); // "SP2V"
    CHECK(kSdlEventMagic == 0x53503249u);    // "SP2I"
    CHECK(kSessionConfigMagic != 0);
    CHECK(kDeviceInfoMagic != 0);

    // Struct sizes — drift here means silent wire breakage.
    CHECK(sizeof(MessageHeader) == 8);
    CHECK(sizeof(SessionConfig) >= 8);
    CHECK(sizeof(DeviceInfo) >= 16);
    CHECK(std::is_trivially_copyable_v<MessageHeader>);
    CHECK(std::is_trivially_copyable_v<SessionConfig>);
    CHECK(std::is_trivially_copyable_v<DeviceInfo>);

    std::printf("check-wire-layout OK "
                "(MessageHeader=%zu SessionConfig=%zu DeviceInfo=%zu)\n",
                sizeof(MessageHeader), sizeof(SessionConfig), sizeof(DeviceInfo));
    return 0;
}

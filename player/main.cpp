// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

#include "player_core.h"
#include <SDL3/SDL_main.h>
#include <cstdio>
#include <cstring>
#include <string>

int main(int argc, char* argv[]) {
    std::string name = "server";
    std::string host = "localhost";
    int port = 3030;
    PlayerOptions opts;
    for (int i = 1; i < argc; i++) {
        if (std::strcmp(argv[i], "--name") == 0 && i + 1 < argc) {
            name = argv[++i];
        } else if (std::strcmp(argv[i], "--host") == 0 && i + 1 < argc) {
            host = argv[++i];
        } else if (std::strcmp(argv[i], "--port") == 0 && i + 1 < argc) {
            port = std::atoi(argv[++i]);
        } else if (std::strcmp(argv[i], "--headless") == 0) {
            opts.headless = true;
        } else if (std::strcmp(argv[i], "--script") == 0 && i + 1 < argc) {
            opts.scriptPath = argv[++i];
        } else if (std::strcmp(argv[i], "--trace") == 0 && i + 1 < argc) {
            opts.tracePath = argv[++i];
        } else if (std::strcmp(argv[i], "--accel") == 0 && i + 1 < argc) {
            opts.accelOverride = std::atoi(argv[++i]);
        } else if (std::strcmp(argv[i], "--device-class") == 0 && i + 1 < argc) {
            const char* v = argv[++i];
            opts.deviceClassOverride = std::strcmp(v, "phone") == 0    ? 1
                                       : std::strcmp(v, "tablet") == 0 ? 2
                                       : std::strcmp(v, "desktop") == 0 ? 3
                                                                        : -1;
        } else if (std::strcmp(argv[i], "--exit-after-script") == 0) {
            opts.exitAfterScript = true;
        } else if (std::strcmp(argv[i], "--geometry") == 0 && i + 1 < argc) {
            int w = 0, h = 0;
            if (std::sscanf(argv[++i], "%dx%d", &w, &h) == 2 && w > 0 && h > 0) {
                opts.initialW = w;
                opts.initialH = h;
            }
        }
    }
    return playerCore(host, port, name, opts);
}

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// Double-buffered frame timing logger. Producer appends to the write
// buffer with zero contention. Background thread swaps buffers,
// analyses the read buffer, and logs a summary.
#pragma once

#include <player/Linalg.h>

#include <SDL3/SDL.h>
#include <spdlog/spdlog.h>

#include <vector>

#ifdef __EMSCRIPTEN__

// Web: no pthreads in the wasm leg — analyse inline from record() on a
// ~2 s cadence. Same API and summary output as the threaded variant; the
// analysis itself is a few hundred µs every 2 s, invisible at frame rate.
template <typename Entry>
struct FrameLog {
    using AnalyseFn = void (*)(const std::vector<Entry>&, uint64_t freq);

    std::vector<Entry> buffer;
    AnalyseFn analyze;
    uint64_t freq;
    uint64_t lastDumpTicks;

    FrameLog(AnalyseFn analyze, int reserveSize = 4096)
        : analyze(analyze), freq(SDL_GetPerformanceFrequency()),
          lastDumpTicks(SDL_GetPerformanceCounter()) {
        buffer.reserve(reserveSize);
    }

    void record(const Entry& e) {
        buffer.push_back(e);
        const uint64_t now = SDL_GetPerformanceCounter();
        if (now - lastDumpTicks >= freq * 2) {
            analyze(buffer, freq);
            buffer.clear();
            lastDumpTicks = now;
        }
    }
};

#else // !__EMSCRIPTEN__

#include <mutex>
#include <thread>

template <typename Entry>
struct FrameLog {
    using AnalyseFn = void (*)(const std::vector<Entry>&, uint64_t freq);

    std::vector<Entry> buffers[2];
    int writeIdx = 0;
    std::mutex swapMutex;
    uint64_t freq;
    bool running = true;
    std::thread dumper;

    FrameLog(AnalyseFn analyze, int reserveSize = 4096)
        : freq(SDL_GetPerformanceFrequency()) {
        buffers[0].reserve(reserveSize);
        buffers[1].reserve(reserveSize);
        dumper = std::thread([this, analyze] {
            while (running) {
                SDL_Delay(2000);
                std::vector<Entry>* readBuf;
                {
                    std::lock_guard lock(swapMutex);
                    readBuf = &buffers[1 - writeIdx];
                    writeIdx = 1 - writeIdx;
                }
                if (!readBuf->empty()) {
                    analyze(*readBuf, freq);
                    readBuf->clear();
                }
            }
        });
    }

    ~FrameLog() {
        running = false;
        if (dumper.joinable()) dumper.join();
    }

    void record(const Entry& e) {
        std::lock_guard lock(swapMutex);
        buffers[writeIdx].push_back(e);
    }
};

#endif // __EMSCRIPTEN__

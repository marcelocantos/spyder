// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

#include <player/Signal.h>
#include <player/appchannel.h>

#include <spdlog/spdlog.h>

#include <atomic>
#include <csignal>
#include <cstdio>

#if defined(__APPLE__)
#include <TargetConditionals.h>
#endif

#if !defined(_WIN32)
#include <unistd.h>  // write, STDERR_FILENO
#endif

// 🎯T136 Best-effort backtrace. <execinfo.h> exists on macOS / iOS / Linux. On
// Android the header is includable but backtrace() / backtrace_symbols_fd() are
// __INTRODUCED_IN(33), so they're undeclared below spyder minSdk — gate on the API
// level too (Android's own tombstone supplies the backtrace there regardless).
#if defined(__has_include)
#  if __has_include(<execinfo.h>) && (!defined(__ANDROID__) || __ANDROID_API__ >= 33)
#    include <execinfo.h>
#    define SPYDER_HAVE_EXECINFO 1
#  endif
#endif

// SIGINT / SIGTERM graceful quit is desktop-only — iOS / Android manage app
// lifecycle, so there is nothing to quit. The 🎯T136 crash handlers below are
// NOT gated this way: a fatal-signal last-gasp is exactly what iOS / Android
// need (otherwise the only trace is an IPS file).
#if defined(__ANDROID__)
#define SPYDER_SIGNAL_SUPPORTED 0
#elif defined(__APPLE__) && TARGET_OS_IPHONE
#define SPYDER_SIGNAL_SUPPORTED 0
#else
#define SPYDER_SIGNAL_SUPPORTED 1
#endif

#if SPYDER_SIGNAL_SUPPORTED
static std::atomic<bool> g_quit{false};
static void handler(int) { g_quit = true; }
#endif

namespace spyder {

void installSignalHandlers() {
#if SPYDER_SIGNAL_SUPPORTED
    std::signal(SIGINT, handler);
    std::signal(SIGTERM, handler);
#endif
}

bool shouldQuit() {
#if SPYDER_SIGNAL_SUPPORTED
    return g_quit;
#else
    return false;
#endif
}

// ── 🎯T136 Crash diagnostics ──────────────────────────────────────

namespace {

std::atomic<bool> g_crashDiag{true};

const char* signalName(int sig) {
    switch (sig) {
        case SIGSEGV: return "SIGSEGV";
        case SIGABRT: return "SIGABRT";
        case SIGBUS:  return "SIGBUS";
        case SIGILL:  return "SIGILL";
        case SIGFPE:  return "SIGFPE";
        default:      return "signal";
    }
}

// The fatal crash signals spyder installs a last-gasp handler for.
constexpr int kCrashSignals[] = {SIGSEGV, SIGABRT, SIGBUS, SIGILL, SIGFPE};

void crashHandler(int sig) {
    // A second fault while reporting (e.g. the logging path itself crashed)
    // must not recurse — go straight to the default handler.
    static std::atomic_flag inCrash = ATOMIC_FLAG_INIT;
    if (inCrash.test_and_set()) {
        std::signal(sig, SIG_DFL);
        std::raise(sig);
        return;
    }

    // 1. Guaranteed async-signal-safe path: a stderr line + a raw backtrace
    //    (backtrace / backtrace_symbols_fd avoid the malloc that
    //    backtrace_symbols would do). This lands even if step 2 wedges.
    const char* name = signalName(sig);
#if !defined(_WIN32)
    char line[96];
    int n = std::snprintf(line, sizeof line,
                          "\nspyder: fatal signal %s (%d) — last-gasp report:\n", name, sig);
    if (n > 0) { ssize_t w = ::write(STDERR_FILENO, line, static_cast<size_t>(n)); (void)w; }
#ifdef SPYDER_HAVE_EXECINFO
    void* frames[64];
    int fn = backtrace(frames, 64);
    backtrace_symbols_fd(frames, fn, STDERR_FILENO);
#endif
#endif

    // 2. Best-effort structured forward through the app-channel logger (and the
    //    native os_log / logcat sinks), flushed so it reaches spyder before we
    //    die. spdlog is not strictly async-signal-safe, but the stderr write
    //    above already secured a trace, so a wedge here loses nothing extra.
    SPDLOG_CRITICAL("spyder: fatal signal {} ({})", name, sig);
    spdlog::default_logger()->flush();
    spyder::appchannel::flush();

    // 3. Restore the default handler and re-raise so the OS still writes its
    //    own crash report (.ips on Apple, tombstone on Android, core on Linux).
    std::signal(sig, SIG_DFL);
    std::raise(sig);
}

}  // namespace

void installCrashHandlers() {
    if (!g_crashDiag.load(std::memory_order_relaxed)) return;
    for (int s : kCrashSignals) std::signal(s, crashHandler);
}

bool crashDiagnosticsEnabled() { return g_crashDiag.load(std::memory_order_relaxed); }
void setCrashDiagnosticsEnabled(bool on) { g_crashDiag.store(on, std::memory_order_relaxed); }

void reportCallbackException(const char* callback, const char* what) {
    SPDLOG_CRITICAL("spyder: uncaught exception in {}: {}",
                    callback ? callback : "?", what ? what : "?");
    spdlog::default_logger()->flush();
    // Drain the app-channel push queue so the record reaches spyder before the
    // exception propagates on to std::terminate.
    spyder::appchannel::flush();
}

} // namespace spyder

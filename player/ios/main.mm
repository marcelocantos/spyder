// iOS spyder player entry point.
// Discovers the stream relay, then runs the shared player core.
//
// Address resolution order:
//   1. argv: -stream_addr host:port / -server_name name
//   2. Documents/stream_addr (+ Documents/server_name) — written by spyder
//      via house_arrest before launch (reliable on physical devices)
//   3. NSUserDefaults (simctl mirrors -stream_addr there)
//   4. STREAM_ADDR / SERVER_NAME env
//   5. Simulator: localhost; device: QR scan
//
// Example (simulator):  xcrun simctl launch <udid> com.spyder.player -stream_addr 192.168.1.217:3030
// Example (device):     spyder launch-app Jevons com.spyder.player with STREAM_ADDR set

#include <TargetConditionals.h>
#include "player_core.h"
#include "QRScanner.h"
#include <SDL3/SDL_main.h>
#include <spdlog/spdlog.h>
#include <spdlog/sinks/base_sink.h>

#import <Foundation/Foundation.h>

#include <cstdlib>
#include <cstring>
#include <string>
#include <thread>

// HTTP PUT sink — sends each log line to a logging server on the host.
// Uses the stream-relay host (or localhost for simulator).
static std::string g_logHost = "192.168.1.217";

template<typename Mutex>
class http_sink : public spdlog::sinks::base_sink<Mutex> {
protected:
    void sink_it_(const spdlog::details::log_msg& msg) override {
        spdlog::memory_buf_t formatted;
        spdlog::sinks::base_sink<Mutex>::formatter_->format(msg, formatted);
        std::string body = fmt::to_string(formatted);

        std::string urlCpp = "http://" + g_logHost + ":9999/log";
        std::thread([body = std::move(body), urlCpp = std::move(urlCpp)] {
            @autoreleasepool {
                NSString* urlStr = [NSString stringWithUTF8String:urlCpp.c_str()];
                NSURL* url = [NSURL URLWithString:urlStr];
                NSMutableURLRequest* req = [NSMutableURLRequest requestWithURL:url];
                req.HTTPMethod = @"PUT";
                req.HTTPBody = [NSData dataWithBytes:body.c_str() length:body.size()];
                req.timeoutInterval = 1.0;

                dispatch_semaphore_t sem = dispatch_semaphore_create(0);
                [[NSURLSession.sharedSession dataTaskWithRequest:req
                    completionHandler:^(NSData*, NSURLResponse*, NSError*) {
                        dispatch_semaphore_signal(sem);
                    }] resume];
                dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, NSEC_PER_SEC));
            }
        }).detach();
    }
    void flush_() override {}
};

// Default matches spyder's loopback stream relay (spyder serve).
static constexpr uint16_t kDefaultPort = 3030;

int main(int argc, char* argv[]) {
    auto sink = std::make_shared<http_sink<std::mutex>>();
    auto logger = std::make_shared<spdlog::logger>("player", sink);
    logger->set_level(spdlog::level::info);
    spdlog::set_default_logger(logger);

    SPDLOG_INFO("spyder player (iOS) starting...");

    std::string host;
    uint16_t port = kDefaultPort;
    std::string serverName = "tiltbuggy";

    // Helper: parse "host:port" or "host" into host/port.
    auto parseAddr = [&](const std::string& s) {
        host = s;
        if (auto colon = s.rfind(':'); colon != std::string::npos) {
            host = s.substr(0, colon);
            port = static_cast<uint16_t>(std::stoi(s.substr(colon + 1)));
        }
    };

    // Priority 1: argv (appservice / simctl launch args).
    // Physical-device env injection is unreliable for sandboxed UI apps;
    // spyder also passes -stream_addr as a process argument.
    for (int i = 1; i < argc; i++) {
        const char* a = argv[i];
        if (!a) continue;
        if ((std::strcmp(a, "-stream_addr") == 0 || std::strcmp(a, "--stream_addr") == 0)
            && i + 1 < argc) {
            parseAddr(argv[++i]);
            SPDLOG_INFO("stream_addr argv: {}:{}", host, port);
        } else if ((std::strcmp(a, "-server_name") == 0 || std::strcmp(a, "--server_name") == 0)
                   && i + 1 < argc) {
            serverName = argv[++i];
        }
    }

    // Priority 2: Documents files written by spyder (house_arrest/AFC).
    // This is the reliable path on physical devices when CoreDevice drops
    // argv/env for sandboxed UI apps.
    if (host.empty()) {
        @autoreleasepool {
            NSArray* paths = NSSearchPathForDirectoriesInDomains(
                NSDocumentDirectory, NSUserDomainMask, YES);
            NSString* docs = paths.firstObject;
            if (docs) {
                NSString* addrPath =
                    [docs stringByAppendingPathComponent:@"stream_addr"];
                NSError* err = nil;
                NSString* addr = [NSString stringWithContentsOfFile:addrPath
                                                          encoding:NSUTF8StringEncoding
                                                             error:&err];
                if (addr) {
                    NSString* trimmed =
                        [addr stringByTrimmingCharactersInSet:
                                  [NSCharacterSet whitespaceAndNewlineCharacterSet]];
                    if (trimmed.length > 0) {
                        parseAddr(std::string(trimmed.UTF8String));
                        SPDLOG_INFO("stream_addr Documents: {}:{}", host, port);
                    }
                }
                NSString* namePath =
                    [docs stringByAppendingPathComponent:@"server_name"];
                NSString* name = [NSString stringWithContentsOfFile:namePath
                                                          encoding:NSUTF8StringEncoding
                                                             error:nil];
                if (name) {
                    NSString* trimmed =
                        [name stringByTrimmingCharactersInSet:
                                  [NSCharacterSet whitespaceAndNewlineCharacterSet]];
                    if (trimmed.length > 0) {
                        serverName = std::string(trimmed.UTF8String);
                        SPDLOG_INFO("server_name Documents: {}", serverName);
                    }
                }
            }
        }
    }

    // Priority 3: NSUserDefaults (simctl stores -stream_addr there).
    if (host.empty()) {
        @autoreleasepool {
            NSUserDefaults* defs = [NSUserDefaults standardUserDefaults];
            NSString* addr = [defs stringForKey:@"stream_addr"];
            if (addr && addr.length > 0) {
                parseAddr(std::string(addr.UTF8String));
                SPDLOG_INFO("stream_addr UserDefaults: {}:{}", host, port);
                [defs removeObjectForKey:@"stream_addr"];
            }
            NSString* name = [defs stringForKey:@"server_name"];
            if (name && name.length > 0) {
                serverName = std::string(name.UTF8String);
                [defs removeObjectForKey:@"server_name"];
            }
        }
    }

    // Priority 4: STREAM_ADDR / SERVER_NAME env (works when the host injects it).
    if (host.empty()) {
        if (const char* addr = std::getenv("STREAM_ADDR")) {
            parseAddr(std::string(addr));
            SPDLOG_INFO("STREAM_ADDR env: {}:{}", host, port);
        }
    }
    if (const char* n = std::getenv("SERVER_NAME")) {
        if (n[0]) serverName = n;
    }

    // Priority 5: platform fallback.
    if (host.empty()) {
#if TARGET_OS_SIMULATOR
        SPDLOG_INFO("Simulator: using localhost:{}", kDefaultPort);
        host = "localhost";
        port = kDefaultPort;
#else
        // Physical device with no address → QR scan.
        SPDLOG_INFO("No stream_addr (argv/Documents/env); opening QR scanner");
        spyder::ScanResult result = spyder::scanQRCode();
        host = result.host;
        port = result.port;
#endif
    }

    SPDLOG_INFO("server name: {}", serverName);

    return playerCore(host, port, serverName);
}

// Android spyder player entry point.
// Discovers the stream relay, then runs the shared player core.
//
// Address resolution order (no QR / camera discovery):
//   1. Intent extra stream_addr (and server_name)
//   2. debug.spyder.stream_addr system property
//   3. Emulator only: 10.0.2.2:3030 (host loopback alias)
// Physical device with no address exits non-zero (use spyder launch_player
// or am start --es stream_addr host:port).
//
//   adb shell am start -n com.spyder.player/.PlayerActivity \
//     --es stream_addr "10.0.2.2:3030" --es server_name tiltbuggy
//   adb shell setprop debug.spyder.stream_addr "192.168.1.100:3030"

#include "player_core.h"
#include <SDL3/SDL_main.h>
#include <spdlog/spdlog.h>
#include <spdlog/sinks/android_sink.h>
#include <sys/system_properties.h>

#include <jni.h>
#include <SDL3/SDL.h>

#include <string>

namespace {

// Default matches spyder's loopback stream relay (spyder serve).
constexpr int kDefaultPort = 3030;

struct Addr {
    std::string host;
    uint16_t port = 0;
};

bool isEmulator() {
    char value[PROP_VALUE_MAX] = {};
    __system_property_get("ro.kernel.qemu", value);
    return value[0] == '1';
}

// Parse "host:port" or "host" into host/port.
Addr parseAddr(const std::string& addr) {
    auto colon = addr.rfind(':');
    if (colon != std::string::npos) {
        uint16_t port = static_cast<uint16_t>(std::stoi(addr.substr(colon + 1)));
        return {addr.substr(0, colon), port};
    }
    return {addr, static_cast<uint16_t>(kDefaultPort)};
}

// Retrieve stream_addr intent extra from PlayerActivity via JNI.
// Returns empty string if absent or on JNI error.
std::string intentStreamAddr() {
    JNIEnv* env = static_cast<JNIEnv*>(SDL_GetAndroidJNIEnv());
    if (!env) return {};

    jclass cls = env->FindClass("com/spyder/player/PlayerActivity");
    if (!cls) { env->ExceptionClear(); return {}; }

    jmethodID mid = env->GetStaticMethodID(cls, "getStreamAddr", "()Ljava/lang/String;");
    if (!mid) { env->ExceptionClear(); env->DeleteLocalRef(cls); return {}; }

    jobject obj = env->CallStaticObjectMethod(cls, mid);
    env->DeleteLocalRef(cls);
    if (!obj) return {};

    const char* chars = env->GetStringUTFChars(static_cast<jstring>(obj), nullptr);
    std::string result = chars ? chars : "";
    env->ReleaseStringUTFChars(static_cast<jstring>(obj), chars);
    env->DeleteLocalRef(obj);
    return result;
}

// Check debug.spyder.stream_addr system property for direct connection.
// Set via: adb shell setprop debug.spyder.stream_addr "192.168.1.100:3030"
// Clear:  adb shell setprop debug.spyder.stream_addr ""
Addr directAddressProp() {
    char value[PROP_VALUE_MAX] = {};
    __system_property_get("debug.spyder.stream_addr", value);
    if (value[0] == '\0') return {};
    return parseAddr(std::string(value));
}

} // namespace

int main(int argc, char* argv[]) {
    (void)argc;
    (void)argv;
    auto logger = spdlog::android_logger_mt("spyder", "SpyderPlayer");
    spdlog::set_default_logger(logger);
    spdlog::set_level(spdlog::level::info);

    SPDLOG_INFO("spyder player (Android) starting...");

    // Server catalogue name (matches server catalogue registration / appName).
    // Override: adb … --es server_name "tiltbuggy"
    std::string serverName = "tiltbuggy";
    {
        JNIEnv* env = static_cast<JNIEnv*>(SDL_GetAndroidJNIEnv());
        if (env) {
            jclass cls = env->FindClass("com/spyder/player/PlayerActivity");
            if (cls) {
                jmethodID mid = env->GetStaticMethodID(
                    cls, "getServerName", "()Ljava/lang/String;");
                if (mid) {
                    jobject obj = env->CallStaticObjectMethod(cls, mid);
                    if (obj) {
                        const char* chars =
                            env->GetStringUTFChars(static_cast<jstring>(obj), nullptr);
                        if (chars && chars[0]) serverName = chars;
                        if (chars) env->ReleaseStringUTFChars(
                            static_cast<jstring>(obj), chars);
                        env->DeleteLocalRef(obj);
                    }
                } else {
                    env->ExceptionClear();
                }
                env->DeleteLocalRef(cls);
            } else {
                env->ExceptionClear();
            }
        }
    }
    SPDLOG_INFO("server name: {}", serverName);

    // Priority 1: stream_addr intent extra.
    {
        std::string addr = intentStreamAddr();
        if (!addr.empty()) {
            auto r = parseAddr(addr);
            SPDLOG_INFO("Intent stream_addr: {}:{}", r.host, r.port);
            return playerCore(r.host, r.port, serverName);
        }
    }

    // Priority 2: System property override.
    {
        auto direct = directAddressProp();
        if (!direct.host.empty()) {
            SPDLOG_INFO("Direct connection via debug.spyder.stream_addr: {}:{}",
                        direct.host, direct.port);
            return playerCore(direct.host, direct.port, serverName);
        }
    }

    // Priority 3: Emulator auto-connect — Android's alias for the host loopback.
    if (isEmulator()) {
        SPDLOG_INFO("Emulator detected — connecting to 10.0.2.2:{}", kDefaultPort);
        return playerCore("10.0.2.2", kDefaultPort, serverName);
    }

    // Physical device with no inject: fail (QR discovery removed).
    SPDLOG_ERROR(
        "No stream_addr (intent / debug.spyder.stream_addr). "
        "Launch via spyder launch_player or "
        "am start … --es stream_addr host:port. "
        "QR discovery has been removed.");
    return 2;
}

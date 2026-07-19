#include <player/Resource.h>
#include <SDL3/SDL.h>
// Slim brokered player (tools/android) does not link SOKOL_IMPL — define
// SPYDER_PLAYER_NO_SOKOL so we don't pull sokol_gfx just for shader-path suffixes.
#if !defined(SPYDER_PLAYER_NO_SOKOL)
#include "sokol_gfx.h"
#endif
#if defined(__APPLE__)
#include <TargetConditionals.h>
#endif

namespace spyder {

std::string resource(const std::string& relativePath) {
    // Already absolute — return unchanged.
    if (!relativePath.empty() && relativePath[0] == '/') {
        return relativePath;
    }

    static const std::string base = [] {
#if defined(__ANDROID__)
        // On Android, assets live inside the APK. SDL_IOFromFile handles
        // them via AssetManager when given a relative path — no prefix needed.
        return std::string();
#endif
        auto p = SDL_GetBasePath();
        if (!p) return std::string();
        std::string dir(p);
#if (defined(__APPLE__) && TARGET_OS_IOS)
        // SDL_GetBasePath() returns the app bundle Resources/ directory.
        return dir;
#else
        // SDL_GetBasePath() returns the binary's directory, e.g. "/path/to/bin/".
        // Go up one level to the project root (convention: binary lives in bin/).
        if (dir.size() > 1 && dir.back() == '/') dir.pop_back();
        auto pos = dir.rfind('/');
        return pos != std::string::npos ? dir.substr(0, pos + 1) : std::string();
#endif
    }();

    return base + relativePath;
}

namespace {
const char* shaderProfileSuffix() {
#if defined(SPYDER_PLAYER_NO_SOKOL)
    // Brokered SDL player: no local sokol backend. Shader-asset suffixes are
    // unused on this path; keep a stable string if anything calls shaderDir().
    return "-gles";
#elif defined(__ANDROID__)
    switch (sg_query_backend()) {
    case SG_BACKEND_VULKAN: return "-spirv";
    case SG_BACKEND_GLES3:  return "-gles";
    default: break;  // shouldn't happen on Android
    }
    return "-gles";   // safer fallback (no SPIR-V mismatch)
#else
    return "";        // Apple → Metal, single canonical "shaders/" dir
#endif
}
}

std::string shaderDir() {
    return std::string("build/shaders") + shaderProfileSuffix();
}

std::string renderShaderDir() {
    return std::string("build/spyder/shaders") + shaderProfileSuffix();
}

} // namespace spyder

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// Android implementation of spyder::resolveFont.
//
// No fonts.xml parsing and no AAssetManager — system fonts on Android live
// under /system/fonts/ with stable filenames across devices (API 24+), so a
// hardcoded candidate list keyed on logical name is sufficient. We probe
// candidates in order and pick the first that's readable.

#include <player/FontLoader.h>
#include <player/Resource.h>

#include <spdlog/spdlog.h>

#include <unistd.h>
#include <mutex>
#include <stdexcept>
#include <string>
#include <unordered_map>
#include <vector>

namespace spyder {
namespace {

// Candidate filesystem paths for each logical font name. First readable path
// wins. Paths listed in decreasing order of preference: modern Roboto first,
// legacy Droid as fallback for older devices.
const std::vector<const char*>& candidatesFor(const std::string& name) {
    static const std::vector<const char*> kSansSerif = {
        "/system/fonts/Roboto-Regular.ttf",
        "/system/fonts/DroidSans.ttf",
    };
    static const std::vector<const char*> kSansSerifBold = {
        "/system/fonts/Roboto-Bold.ttf",
        "/system/fonts/DroidSans-Bold.ttf",
    };
    static const std::vector<const char*> kSerif = {
        "/system/fonts/NotoSerif-Regular.ttf",
        "/system/fonts/DroidSerif-Regular.ttf",
    };
    static const std::vector<const char*> kSerifBold = {
        "/system/fonts/NotoSerif-Bold.ttf",
        "/system/fonts/DroidSerif-Bold.ttf",
    };
    static const std::vector<const char*> kMonospace = {
        "/system/fonts/RobotoMono-Regular.ttf",
        "/system/fonts/DroidSansMono.ttf",
    };
    static const std::vector<const char*> kMonospaceBold = {
        "/system/fonts/RobotoMono-Bold.ttf",
        "/system/fonts/DroidSansMono.ttf",  // no bold variant of DroidSansMono
    };
    static const std::vector<const char*> kEmpty = {};

    if (name == "sans-serif")      return kSansSerif;
    if (name == "sans-serif-bold") return kSansSerifBold;
    if (name == "serif")           return kSerif;
    if (name == "serif-bold")      return kSerifBold;
    if (name == "monospace")       return kMonospace;
    if (name == "monospace-bold")  return kMonospaceBold;
    return kEmpty;
}

FontRef resolveSystemFont(const std::string& name) {
    const auto& candidates = candidatesFor(name);
    if (candidates.empty()) {
        throw std::runtime_error(
            "spyder::resolveFont: unknown system font name '" + name + "'");
    }
    for (const char* path : candidates) {
        if (access(path, R_OK) == 0) {
            return FontRef{path, 0};
        }
    }
    throw std::runtime_error(
        "spyder::resolveFont: no readable candidate for system font '" + name + "'");
}

} // namespace

FontRef resolveFont(const std::string& uri) {
    // Cache positive and negative results: each system-font resolve
    // probes multiple candidate paths via access(), and the answer
    // never changes within a process. Empty FontRef in the cache is
    // the failure marker — on hit we re-throw without re-running the
    // resolver.
    static std::unordered_map<std::string, FontRef> cache;
    static std::mutex cacheMutex;
    {
        std::lock_guard<std::mutex> lock(cacheMutex);
        if (auto it = cache.find(uri); it != cache.end()) {
            if (it->second.path.empty()) {
                throw std::runtime_error(
                    "spyder::resolveFont: '" + uri + "' previously failed to resolve");
            }
            return it->second;
        }
    }

    constexpr const char* kSystemPrefix = "system:";
    constexpr const char* kFilePrefix = "file:";

    FontRef result;
    try {
        if (uri.starts_with(kSystemPrefix)) {
            result = resolveSystemFont(uri.substr(strlen(kSystemPrefix)));
        } else if (uri.starts_with(kFilePrefix)) {
            result = FontRef{uri.substr(strlen(kFilePrefix)), 0};
        } else {
            result = FontRef{spyder::resource(uri), 0};
        }
    } catch (...) {
        std::lock_guard<std::mutex> lock(cacheMutex);
        cache.emplace(uri, FontRef{});
        throw;
    }

    std::lock_guard<std::mutex> lock(cacheMutex);
    cache.emplace(uri, result);
    return result;
}

} // namespace spyder

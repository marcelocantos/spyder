// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// Core Text implementation of spyder::resolveFont for macOS and iOS.

#include <player/FontLoader.h>
#include <player/Resource.h>

#import <CoreText/CoreText.h>
#import <Foundation/Foundation.h>

#include <spdlog/spdlog.h>

#include <mutex>
#include <stdexcept>
#include <unordered_map>

namespace spyder {
namespace {

// Map logical font names to Core Text family names.
// The Apple system sans-serif is San Francisco on modern iOS/macOS,
// but that's only accessible via CTFontCreateUIFontForLanguage.
// Helvetica is a safe, long-standing alternative that's on all
// Apple platforms.
struct FamilySpec {
    const char* family;
    CTFontSymbolicTraits traits;
};

FamilySpec familyForName(const std::string& name) {
    if (name == "sans-serif")      return {"Helvetica", 0};
    if (name == "sans-serif-bold") return {"Helvetica", kCTFontBoldTrait};
    if (name == "serif")           return {"Times New Roman", 0};
    if (name == "serif-bold")      return {"Times New Roman", kCTFontBoldTrait};
    if (name == "monospace")       return {"Menlo", 0};
    if (name == "monospace-bold")  return {"Menlo", kCTFontBoldTrait};
    return {nullptr, 0};
}

// Find the face index within a .ttc font collection that matches the
// given PostScript name.
int faceIndexForPSName(const std::string& path, CFStringRef targetPSName) {
    CFStringRef pathStr = CFStringCreateWithCString(
        kCFAllocatorDefault, path.c_str(), kCFStringEncodingUTF8);
    if (!pathStr) return 0;
    CFURLRef url = CFURLCreateWithFileSystemPath(
        kCFAllocatorDefault, pathStr, kCFURLPOSIXPathStyle, false);
    CFRelease(pathStr);
    if (!url) return 0;

    CFArrayRef descs = CTFontManagerCreateFontDescriptorsFromURL(url);
    CFRelease(url);
    if (!descs) return 0;

    int found = 0;
    CFIndex count = CFArrayGetCount(descs);
    for (CFIndex i = 0; i < count; i++) {
        auto desc = (CTFontDescriptorRef)CFArrayGetValueAtIndex(descs, i);
        CFStringRef psName = (CFStringRef)CTFontDescriptorCopyAttribute(
            desc, kCTFontNameAttribute);
        if (psName) {
            if (CFStringCompare(psName, targetPSName, 0) == kCFCompareEqualTo) {
                found = int(i);
                CFRelease(psName);
                break;
            }
            CFRelease(psName);
        }
    }
    CFRelease(descs);
    return found;
}

FontRef resolveSystemFont(const std::string& name) {
    // Logical names (sans-serif / serif / monospace, with optional
    // -bold) map to a known family + traits via familyForName. Anything
    // else is passed through to Core Text as a literal family or
    // PostScript name — Core Text accepts both via CTFontCreateWithName
    // — so callers can resolve arbitrary system fonts by name (e.g.
    // "Krungthep", "Helvetica Neue").
    FamilySpec spec = familyForName(name);
    const char* lookupName = spec.family ? spec.family : name.c_str();

    CFStringRef familyStr = CFStringCreateWithCString(
        kCFAllocatorDefault, lookupName, kCFStringEncodingUTF8);
    if (!familyStr) {
        throw std::runtime_error(
            "spyder::resolveFont: CFStringCreateWithCString failed for '" + name + "'");
    }

    CTFontRef font = CTFontCreateWithName(familyStr, 12.0, nullptr);
    CFRelease(familyStr);
    if (!font) {
        throw std::runtime_error(
            "spyder::resolveFont: Core Text could not resolve system font '" + name + "'");
    }

    // Loud fallback detection: CTFontCreateWithName silently substitutes
    // a default (usually Helvetica) when the requested family/PS name
    // isn't installed. Compare the returned face's family against what
    // we asked for and fail the resolution if they diverge.
    if (!spec.family) {
        CFStringRef gotFamily = CTFontCopyFamilyName(font);
        bool matched = false;
        if (gotFamily) {
            // Match either the input as-is or the input with spaces
            // stripped (Core Text returns "HelveticaNeue" for the family
            // even when caller passed "Helvetica Neue").
            CFStringRef wantStrict = CFStringCreateWithCString(
                kCFAllocatorDefault, name.c_str(), kCFStringEncodingUTF8);
            if (wantStrict
                && CFStringCompare(gotFamily, wantStrict, 0)
                       == kCFCompareEqualTo) {
                matched = true;
            }
            if (wantStrict) CFRelease(wantStrict);
            // Try Core Text's PostScript-name lookup too, since callers
            // may pass either family or PS name.
            if (!matched) {
                CFStringRef gotPS = CTFontCopyPostScriptName(font);
                if (gotPS) {
                    CFStringRef wantPS = CFStringCreateWithCString(
                        kCFAllocatorDefault, name.c_str(),
                        kCFStringEncodingUTF8);
                    if (wantPS
                        && CFStringCompare(gotPS, wantPS, 0)
                               == kCFCompareEqualTo) {
                        matched = true;
                    }
                    if (wantPS) CFRelease(wantPS);
                    CFRelease(gotPS);
                }
            }
            CFRelease(gotFamily);
        }
        if (!matched) {
            CFRelease(font);
            throw std::runtime_error(
                "spyder::resolveFont: requested system font '" + name +
                "' is not installed — Core Text substituted a default");
        }
    }

    if (spec.traits) {
        CTFontRef bold = CTFontCreateCopyWithSymbolicTraits(
            font, 0, nullptr, spec.traits, spec.traits);
        if (bold) {
            CFRelease(font);
            font = bold;
        }
    }

    CFURLRef url = (CFURLRef)CTFontCopyAttribute(font, kCTFontURLAttribute);
    CFStringRef psName = CTFontCopyPostScriptName(font);
    CFRelease(font);
    if (!url || !psName) {
        if (url) CFRelease(url);
        if (psName) CFRelease(psName);
        throw std::runtime_error(
            "spyder::resolveFont: Core Text returned no URL/PSName for '" + name + "'");
    }

    char pathBuf[1024];
    if (!CFURLGetFileSystemRepresentation(url, true,
                                          reinterpret_cast<UInt8*>(pathBuf),
                                          sizeof(pathBuf))) {
        CFRelease(url);
        CFRelease(psName);
        throw std::runtime_error(
            "spyder::resolveFont: CFURLGetFileSystemRepresentation failed for '" + name + "'");
    }
    CFRelease(url);

    FontRef ref;
    ref.path = pathBuf;
    ref.faceIndex = faceIndexForPSName(ref.path, psName);
    CFRelease(psName);
    return ref;
}

} // namespace

FontRef resolveFont(const std::string& uri) {
    // Cache positive and negative results: Core Text resolution is
    // ~1 ms per call (CTFontCreateWithName + family/PS-name compare +
    // CTFontManagerCreateFontDescriptorsFromURL for the TTC face index)
    // and the answer never changes within a process. Empty FontRef in
    // the cache is the failure marker — on hit we re-throw without
    // re-running the resolver.
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

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

#include <player/svg.h>

#include <player/FontLoader.h>

#ifndef SPYDER_PLAYER_NO_SOKOL
#include <player/CmdStream.h>
#include "sokol_gfx.h"
#endif
#include <spdlog/spdlog.h>

#include <cstdint>
#include <cstring>
#include <mutex>

namespace spyder {

namespace {

// Register one of the platform's logical font URIs (e.g. "system:sans-serif")
// with lunasvg's font cache as the named family. Best-effort: SVG text
// missing a system default should render unstyled, not abort the rasterize.
// Apps that want a hard failure when a specific family is missing should
// call resolveFont + registerSvgFontFace explicitly and let the throw
// propagate.
void registerPlatformFont(const char* family, bool bold, bool italic, const char* uri) {
    FontRef ref;
    try {
        ref = resolveFont(uri);
    } catch (const std::exception& e) {
        spdlog::warn("spyder::rasterizeSvg: {} — SVG <text font-family=\"{}\"> "
                     "may render unstyled", e.what(), family);
        return;
    }
    if (!lunasvg_add_font_face_from_file(family, bold, italic, ref.path.c_str())) {
        spdlog::warn("spyder::rasterizeSvg: lunasvg rejected font file {} for family {}",
                     ref.path, family);
    }
}

void ensureDefaultFonts() {
    static std::once_flag flag;
    std::call_once(flag, []{
        registerPlatformFont("sans-serif", false, false, "system:sans-serif");
        registerPlatformFont("sans-serif", true,  false, "system:sans-serif-bold");
        registerPlatformFont("serif",      false, false, "system:serif");
        registerPlatformFont("serif",      true,  false, "system:serif-bold");
        registerPlatformFont("monospace",  false, false, "system:monospace");
        registerPlatformFont("monospace",  true,  false, "system:monospace-bold");
    });
}

// lunasvg returns ARGB32 premultiplied. On little-endian (the only target
// spyder supports) that's bytes B,G,R,A per pixel in memory.
// sokol's SG_PIXELFORMAT_RGBA8 expects R,G,B,A. Swap byte 0 and byte 2 in
// each pixel; keep premultiplication.
SvgPixels bitmapToPixels(const lunasvg::Bitmap& bm) {
    SvgPixels out;
    if (bm.isNull()) return out;

    const int w = bm.width();
    const int h = bm.height();
    const int stride = bm.stride();
    out.width  = w;
    out.height = h;
    out.rgba.resize(static_cast<size_t>(w) * static_cast<size_t>(h) * 4);

    const uint8_t* src = bm.data();
    uint8_t*       dst = out.rgba.data();
    for (int y = 0; y < h; ++y) {
        const uint8_t* sRow = src + static_cast<size_t>(y) * stride;
        uint8_t*       dRow = dst + static_cast<size_t>(y) * w * 4;
        for (int x = 0; x < w; ++x) {
            dRow[x * 4 + 0] = sRow[x * 4 + 2];
            dRow[x * 4 + 1] = sRow[x * 4 + 1];
            dRow[x * 4 + 2] = sRow[x * 4 + 0];
            dRow[x * 4 + 3] = sRow[x * 4 + 3];
        }
    }
    return out;
}

SvgBounds boundsFromBox(const lunasvg::Box& box) {
    return {
        .x = box.x,
        .y = box.y,
        .width = box.w,
        .height = box.h,
        .valid = true,
    };
}

#ifndef SPYDER_PLAYER_NO_SOKOL
Sprite uploadPixels(const SvgPixels& pixels) {
    if (pixels.isNull()) return Sprite{};
    sg_image_desc desc{};
    desc.width  = pixels.width;
    desc.height = pixels.height;
    desc.pixel_format = SG_PIXELFORMAT_RGBA8;
    desc.data.mip_levels[0] = (sg_range){
        .ptr  = pixels.rgba.data(),
        .size = pixels.rgba.size(),
    };
    desc.label = "spyder.svg.sprite";
    Sprite out;
    out.tex = sg_make_image(&desc);
    sg_view_desc vd{};
    vd.texture.image = out.tex;
    vd.label = "spyder.svg.sprite.view";
    out.view = sg_make_view(&vd);
    out.width  = pixels.width;
    out.height = pixels.height;
    return out;
}

// Register recipe (preferred) + pixel flatten fallback for cmdstream.
void registerSvgRecipe(const Sprite& out, std::string_view svg,
                       int targetW, int targetH) {
    if (out.tex.id == SG_INVALID_ID) return;
    const int16_t tw = static_cast<int16_t>(targetW < 0 ? -1 : targetW);
    const int16_t th = static_cast<int16_t>(targetH < 0 ? -1 : targetH);
    cmdstream::registerImageSvg(
        out.tex.id, svg, tw, th,
        static_cast<uint16_t>(out.width), static_cast<uint16_t>(out.height));
}
#endif

} // namespace

bool registerSvgFontFace(const std::string& family, bool bold, bool italic,
                         const FontRef& ref) {
    if (ref.path.empty()) return false;
    return lunasvg_add_font_face_from_file(family.c_str(), bold, italic, ref.path.c_str());
}

SvgPixels rasterizeSvgToPixels(std::string_view svg, int targetW, int targetH) {
    ensureDefaultFonts();

    auto doc = lunasvg::Document::loadFromData(svg.data(), svg.size());
    if (!doc) {
        spdlog::error("spyder::rasterizeSvgToPixels: failed to parse SVG ({} bytes)", svg.size());
        return {};
    }

    auto bm = doc->renderToBitmap(targetW, targetH);
    if (bm.isNull()) {
        spdlog::error("spyder::rasterizeSvgToPixels: renderToBitmap returned null ({}x{})", targetW, targetH);
        return {};
    }
    return bitmapToPixels(bm);
}

#ifndef SPYDER_PLAYER_NO_SOKOL
Sprite rasterizeSvg(std::string_view svg, int targetW, int targetH) {
    Sprite out = uploadPixels(rasterizeSvgToPixels(svg, targetW, targetH));
    registerSvgRecipe(out, svg, targetW, targetH);
    return out;
}

Sprite renderSvgDocument(const lunasvg::Document& doc, int targetW, int targetH) {
    // lunasvg::Document::renderToBitmap is non-const in some versions; the
    // const_cast is safe because renderToBitmap doesn't observably mutate
    // the document beyond updating an internal layout cache.
    auto bm = const_cast<lunasvg::Document&>(doc).renderToBitmap(targetW, targetH);
    if (bm.isNull()) {
        spdlog::error("spyder::renderSvgDocument: renderToBitmap returned null ({}x{})", targetW, targetH);
        return Sprite{};
    }
    SvgPixels px = bitmapToPixels(bm);
    Sprite out = uploadPixels(px);
    // Interactive document: no retained SVG string — pixel flatten only.
    // (Mutation-stream SVG verb is a later T128.7 extension.)
    if (out.tex.id != SG_INVALID_ID && !px.isNull()) {
        cmdstream::registerImagePixels(
            out.tex.id,
            static_cast<uint16_t>(px.width),
            static_cast<uint16_t>(px.height),
            px.rgba.data(), px.rgba.size());
    }
    return out;
}
#endif // !SPYDER_PLAYER_NO_SOKOL

SvgBounds measureSvgBounds(std::string_view svg) {
    ensureDefaultFonts();

    auto doc = lunasvg::Document::loadFromData(svg.data(), svg.size());
    if (!doc) {
        spdlog::error("spyder::measureSvgBounds: failed to parse SVG ({} bytes)", svg.size());
        return {};
    }
    return measureSvgBounds(*doc);
}

SvgBounds measureSvgBounds(const lunasvg::Document& doc) {
    ensureDefaultFonts();
    return boundsFromBox(doc.boundingBox());
}

SvgBounds measureSvgElementBounds(std::string_view svg, const std::string& elementId) {
    ensureDefaultFonts();

    auto doc = lunasvg::Document::loadFromData(svg.data(), svg.size());
    if (!doc) {
        spdlog::error("spyder::measureSvgElementBounds: failed to parse SVG ({} bytes)", svg.size());
        return {};
    }
    return measureSvgElementBounds(*doc, elementId);
}

SvgBounds measureSvgElementBounds(const lunasvg::Document& doc, const std::string& elementId) {
    ensureDefaultFonts();
    if (auto element = doc.getElementById(elementId)) {
        return measureSvgElementBounds(element);
    }
    return {};
}

SvgBounds measureSvgElementBounds(const lunasvg::Element& element) {
    ensureDefaultFonts();
    if (!element) return {};
    return boundsFromBox(element.getGlobalBoundingBox());
}

lunasvg::Element hitTestSvgAt(const lunasvg::Document& doc,
                              float x, float y, float radiusPx) {
    // lunasvg::Document::elementFromPoint is non-const in some versions;
    // same const_cast rationale as renderToBitmap above.
    auto& d = const_cast<lunasvg::Document&>(doc);

    // Centre first — a direct hit wins over any ring sample.
    if (auto e = d.elementFromPoint(x, y)) return e;
    if (radiusPx <= 0.0f) return {};

    // 8-point ring at 45° increments. cos/sin computed inline because
    // C++ <cmath> trig isn't constexpr until C++26; the table form
    // (eight entries × two floats) is overkill for code that only runs
    // on a tap.
    constexpr float kSqrt1_2 = 0.70710678118654752440f;  // 1/√2
    constexpr float kOffsets[8][2] = {
        { 1.0f,      0.0f     },  // E
        { kSqrt1_2,  kSqrt1_2 },  // SE
        { 0.0f,      1.0f     },  // S
        {-kSqrt1_2,  kSqrt1_2 },  // SW
        {-1.0f,      0.0f     },  // W
        {-kSqrt1_2, -kSqrt1_2 },  // NW
        { 0.0f,     -1.0f     },  // N
        { kSqrt1_2, -kSqrt1_2 },  // NE
    };
    for (const auto& o : kOffsets) {
        if (auto e = d.elementFromPoint(x + o[0] * radiusPx,
                                        y + o[1] * radiusPx)) {
            return e;
        }
    }
    return {};
}

} // namespace spyder

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

#include <player/text.h>

#ifndef SPYDER_PLAYER_NO_SOKOL
#include <player/CmdStream.h>
#include "sokol_gfx.h"
#include <fstream>
#endif
#include <ft2build.h>
#include FT_FREETYPE_H
#include <spdlog/spdlog.h>

#include <algorithm>
#include <cmath>
#include <cstdint>
#include <cstring>
#include <vector>

namespace spyder {

namespace {

FT_Library& ftLibrary() {
    static FT_Library lib = nullptr;
    if (lib == nullptr) {
        FT_Error err = FT_Init_FreeType(&lib);
        if (err != 0) {
            spdlog::error("spyder::rasterizeText: FT_Init_FreeType failed (error {})", err);
            lib = nullptr;
        }
    }
    return lib;
}

} // namespace

namespace {

TextPixels rasterizeTextFace(const std::string& text,
                             FT_Face face,
                             float sizePt,
                             la::float4 color) {
    TextPixels out;
    if (!face || text.empty()) return out;

    const FT_F26Dot6 sizeFixed = static_cast<FT_F26Dot6>(sizePt * 64.0f + 0.5f);
    FT_Error err = FT_Set_Char_Size(face, 0, sizeFixed, 72, 72);
    if (err != 0) {
        spdlog::error("spyder::rasterizeText: FT_Set_Char_Size failed (error {})", err);
        return out;
    }

    int totalAdvance = 0;
    int maxAscent    = 0;
    int maxDescent   = 0;

    for (unsigned char ch : text) {
        FT_UInt glyphIdx = FT_Get_Char_Index(face, static_cast<FT_ULong>(ch));
        err = FT_Load_Glyph(face, glyphIdx, FT_LOAD_RENDER);
        if (err != 0) continue;

        FT_GlyphSlot slot = face->glyph;
        int bearingY = static_cast<int>(slot->bitmap_top);
        int descent  = static_cast<int>(slot->bitmap.rows) - bearingY;
        maxAscent  = std::max(maxAscent,  bearingY);
        maxDescent = std::max(maxDescent, descent);
        totalAdvance += static_cast<int>(slot->advance.x >> 6);
    }

    if (totalAdvance <= 0 || maxAscent + maxDescent <= 0) {
        spdlog::error("spyder::rasterizeText: measured empty glyph metrics for '{}'", text);
        return out;
    }

    const int canvasW = totalAdvance;
    const int canvasH = maxAscent + maxDescent;

    out.rgba.resize(static_cast<size_t>(canvasW) * static_cast<size_t>(canvasH) * 4, 0);
    out.width  = canvasW;
    out.height = canvasH;

    const float cr = std::max(0.0f, std::min(1.0f, color.x));
    const float cg = std::max(0.0f, std::min(1.0f, color.y));
    const float cb = std::max(0.0f, std::min(1.0f, color.z));
    const float ca = std::max(0.0f, std::min(1.0f, color.w));

    int penX = 0;

    for (unsigned char ch : text) {
        FT_UInt glyphIdx = FT_Get_Char_Index(face, static_cast<FT_ULong>(ch));
        err = FT_Load_Glyph(face, glyphIdx, FT_LOAD_RENDER);
        if (err != 0) {
            penX += static_cast<int>(sizeFixed >> 6) / 2;
            continue;
        }

        FT_GlyphSlot slot = face->glyph;
        const FT_Bitmap& bm = slot->bitmap;

        const int blitX = penX + static_cast<int>(slot->bitmap_left);
        const int blitY = maxAscent - static_cast<int>(slot->bitmap_top);

        for (int row = 0; row < static_cast<int>(bm.rows); ++row) {
            const int dstY = blitY + row;
            if (dstY < 0 || dstY >= canvasH) continue;

            for (int col = 0; col < static_cast<int>(bm.width); ++col) {
                const int dstX = blitX + col;
                if (dstX < 0 || dstX >= canvasW) continue;

                const uint8_t* srcRow = (bm.pitch >= 0)
                    ? bm.buffer + static_cast<size_t>(row) * static_cast<size_t>(bm.pitch)
                    : bm.buffer + static_cast<size_t>(bm.rows - 1 - row) * static_cast<size_t>(-bm.pitch);

                const float alpha         = static_cast<float>(srcRow[col]) / 255.0f;
                const float combinedAlpha = ca * alpha;
                uint8_t* dst = out.rgba.data() +
                    (static_cast<size_t>(dstY) * static_cast<size_t>(canvasW) + static_cast<size_t>(dstX)) * 4;
                dst[0] = static_cast<uint8_t>(cr * combinedAlpha * 255.0f + 0.5f);
                dst[1] = static_cast<uint8_t>(cg * combinedAlpha * 255.0f + 0.5f);
                dst[2] = static_cast<uint8_t>(cb * combinedAlpha * 255.0f + 0.5f);
                dst[3] = static_cast<uint8_t>(combinedAlpha * 255.0f + 0.5f);
            }
        }

        penX += static_cast<int>(slot->advance.x >> 6);
    }
    return out;
}

#ifndef SPYDER_PLAYER_NO_SOKOL
std::vector<uint8_t> readFileBytes(const std::string& path) {
    std::ifstream in(path, std::ios::binary);
    if (!in) return {};
    return std::vector<uint8_t>(std::istreambuf_iterator<char>(in),
                                std::istreambuf_iterator<char>());
}
#endif

} // namespace

TextPixels rasterizeTextToPixels(const std::string& text,
                                 const FontRef& font,
                                 float sizePt,
                                 la::float4 color) {
    TextPixels out;

    if (text.empty()) return out;
    if (font.path.empty()) {
        spdlog::error("spyder::rasterizeText: FontRef has empty path");
        return out;
    }

    FT_Library lib = ftLibrary();
    if (lib == nullptr) return out;

    FT_Face face = nullptr;
    FT_Error err = FT_New_Face(lib, font.path.c_str(),
                               static_cast<FT_Long>(font.faceIndex), &face);
    if (err != 0) {
        spdlog::error("spyder::rasterizeText: FT_New_Face failed for '{}' face {} (error {})",
                      font.path, font.faceIndex, err);
        return out;
    }

    out = rasterizeTextFace(text, face, sizePt, color);
    FT_Done_Face(face);
    return out;
}

TextPixels rasterizeTextToPixelsFromMemory(const std::string& text,
                                           const void* fontBytes, size_t fontN,
                                           int faceIndex,
                                           float sizePt,
                                           la::float4 color) {
    TextPixels out;
    if (text.empty() || !fontBytes || fontN == 0) return out;

    FT_Library lib = ftLibrary();
    if (lib == nullptr) return out;

    FT_Face face = nullptr;
    FT_Error err = FT_New_Memory_Face(
        lib,
        static_cast<const FT_Byte*>(fontBytes),
        static_cast<FT_Long>(fontN),
        static_cast<FT_Long>(faceIndex),
        &face);
    if (err != 0) {
        spdlog::error("spyder::rasterizeText: FT_New_Memory_Face failed (error {})", err);
        return out;
    }
    out = rasterizeTextFace(text, face, sizePt, color);
    FT_Done_Face(face);
    return out;
}

#ifndef SPYDER_PLAYER_NO_SOKOL
Sprite rasterizeText(const std::string& text,
                     const FontRef& font,
                     float sizePt,
                     la::float4 color) {
    auto pixels = rasterizeTextToPixels(text, font, sizePt, color);
    if (pixels.isNull()) return Sprite{};

    sg_image_desc desc{};
    desc.width        = pixels.width;
    desc.height       = pixels.height;
    desc.pixel_format = SG_PIXELFORMAT_RGBA8;
    desc.data.mip_levels[0] = (sg_range){
        .ptr  = pixels.rgba.data(),
        .size = pixels.rgba.size(),
    };
    desc.label = "spyder.text.sprite";

    Sprite out;
    out.tex = sg_make_image(&desc);

    sg_view_desc vd{};
    vd.texture.image = out.tex;
    vd.label = "spyder.text.sprite.view";
    out.view = sg_make_view(&vd);

    out.width  = pixels.width;
    out.height = pixels.height;

    // 🎯T128.7: ship font + string recipe (not RGBA) when streaming.
    if (out.tex.id != SG_INVALID_ID) {
        auto fontBytes = readFileBytes(font.path);
        if (!fontBytes.empty()) {
            cmdstream::registerImageText(
                out.tex.id, text,
                fontBytes.data(), fontBytes.size(), font.faceIndex,
                sizePt, color.x, color.y, color.z, color.w,
                static_cast<uint16_t>(out.width),
                static_cast<uint16_t>(out.height));
        } else {
            cmdstream::registerImagePixels(
                out.tex.id,
                static_cast<uint16_t>(out.width),
                static_cast<uint16_t>(out.height),
                pixels.rgba.data(), pixels.rgba.size());
        }
    }
    return out;
}
#endif // !SPYDER_PLAYER_NO_SOKOL

} // namespace spyder

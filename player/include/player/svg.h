// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

#pragma once

#include <player/FontLoader.h>
#ifndef SPYDER_PLAYER_NO_SOKOL
#include <player/sprite.h>
#endif

#include <lunasvg.h>

#include <cstdint>
#include <string>
#include <string_view>
#include <vector>

namespace spyder {

// CPU-side raster output: width * height * 4 bytes of RGBA8 with
// premultiplied alpha. `rgba` is empty on failure (parse error, OOM,
// zero-area target). Errors are logged via spdlog at the point of failure.
struct SvgPixels {
    std::vector<uint8_t> rgba;
    int width  = 0;
    int height = 0;

    constexpr bool isNull() const { return rgba.empty(); }
};

// Layout-space bounds reported by lunasvg after SVG layout. `valid` is false
// on parse failure or when a requested element is not present.
struct SvgBounds {
    float x = 0.0f;
    float y = 0.0f;
    float width = 0.0f;
    float height = 0.0f;
    bool valid = false;

    constexpr bool isNull() const { return !valid; }
};

// Rasterize an SVG document string to RGBA8 premultiplied pixels at the
// requested pixel dimensions. `targetW` / `targetH` of -1 mean "use the
// SVG's intrinsic size". Background is fully transparent.
SvgPixels rasterizeSvgToPixels(std::string_view svg, int targetW = -1, int targetH = -1);

#ifndef SPYDER_PLAYER_NO_SOKOL
// Rasterize an SVG document string and upload to a sokol texture, returning
// the result as a `Sprite`. Sprite is null on failure.
//
// The render backend must be initialized before calling.
Sprite rasterizeSvg(std::string_view svg, int targetW = -1, int targetH = -1);

// Render an existing `lunasvg::Document` into a Sprite.
Sprite renderSvgDocument(const lunasvg::Document& doc, int targetW, int targetH);
#endif

// Measure an SVG document or element after lunasvg layout. Bounds are in the
// SVG document's own coordinate system. Element bounds are global bounds, so
// parent/element transforms are already applied. No raster texture is created.
SvgBounds measureSvgBounds(std::string_view svg);
SvgBounds measureSvgBounds(const lunasvg::Document& doc);
SvgBounds measureSvgElementBounds(std::string_view svg, const std::string& elementId);
SvgBounds measureSvgElementBounds(const lunasvg::Document& doc, const std::string& elementId);
SvgBounds measureSvgElementBounds(const lunasvg::Element& element);

// ─────────────────────────────────────────────────────────────────────
// SVG font registration
// ─────────────────────────────────────────────────────────────────────
//
// SVGs that use `<text>` elements need fonts registered in lunasvg's
// font cache before rasterizeSvg* runs. the player handles this in two layers:
//
// 1. Lazy default registration. On the first rasterize call, the player
//    registers system defaults for sans-serif / serif / monospace
//    (regular and bold) via `spyder::resolveFont("system:...")`. Best-
//    effort — if no candidate, that family is silently unregistered.
// 2. App overrides. Call `registerSvgFontFace` before / alongside
//    rasterize to register custom faces.
//
// Apple TTC limitation: Apple system fonts ship as `.ttc` collections
// and lunasvg's public C API drops the face index, so requesting bold
// on an Apple system font produces synthetic "faux-bold" rather than
// the designed Bold cut. Custom fonts (separate `.ttf` per weight) and
// non-Apple platforms are unaffected. Dev-time only; ship custom
// fonts for production typography.
bool registerSvgFontFace(const std::string& family, bool bold, bool italic,
                         const FontRef& ref);

// ─────────────────────────────────────────────────────────────────────
// Hit testing with fingertip tolerance (🎯T53)
// ─────────────────────────────────────────────────────────────────────
//
// lunasvg's native `Document::elementFromPoint(x, y)` is pixel-precise:
// the touch must land on the rasterized geometry of the element. For
// touch UI on real fingers, pixel-precise is too strict — the user
// taps in the visual neighbourhood of a button, not on its pixel grid.
//
// `hitTestSvgAt` samples a small ring of points around the input,
// returns the first element it finds. Use it where the lunasvg native
// API would have been called — it's a strictly more forgiving
// drop-in. Tolerance is in the same coord system as the input (SVG
// pixels, post `inverse(modelToWorld)` → unit-square × sprite.size).
//
// `radiusPx == 0` reduces to a single `elementFromPoint` call. The
// sample pattern is centre + 8 ring points at 45° increments at the
// given radius. The centre is consulted first, so a hit on the
// element under the fingertip is preferred over a neighbour even
// when both are within radius. The cost is at most 9 lunasvg calls,
// each ~O(rendered-element-count) — fine for touch handling at 60fps.
//
// Returns an invalid `lunasvg::Element` (operator bool == false) on
// no-hit.
lunasvg::Element hitTestSvgAt(const lunasvg::Document& doc,
                              float x, float y, float radiusPx);

} // namespace spyder

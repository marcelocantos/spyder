// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// Minimal spyder::Sprite RAII for SPYDER_PLAYER_NO_SOKOL (brokered player).
// Recipe materialize only needs *ToPixels APIs; Sprite returns are null.

#include <player/sprite.h>

namespace spyder {

namespace detail {
namespace {
uint64_t g_spriteReleases = 0;
}
uint64_t spriteReleaseCount() { return g_spriteReleases; }
}  // namespace detail

void Sprite::destroy() {
    tex = {};
    view = {};
    width = 0;
    height = 0;
}

Sprite::~Sprite() { destroy(); }

Sprite::Sprite(Sprite&& o) noexcept
    : tex(o.tex), view(o.view), width(o.width), height(o.height) {
    o.tex = {};
    o.view = {};
    o.width = 0;
    o.height = 0;
}

Sprite& Sprite::operator=(Sprite&& o) noexcept {
    if (this != &o) {
        destroy();
        tex = o.tex;
        view = o.view;
        width = o.width;
        height = o.height;
        o.tex = {};
        o.view = {};
        o.width = 0;
        o.height = 0;
    }
    return *this;
}

void Sprite::draw(const la::float4x4&) const {}

SpriteBatch::SpriteBatch() = default;
SpriteBatch::~SpriteBatch() = default;

void SpriteBatch::clear() { quads_.clear(); }

void SpriteBatch::addSprite(const la::float4x4&, const Sprite&, uint32_t) {}

void SpriteBatch::addSprite(const la::float4x4&, const Sprite&, Rect, uint32_t) {}

void SpriteBatch::submit(const la::float4x4&) {}

}  // namespace spyder

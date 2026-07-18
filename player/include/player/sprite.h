// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// Lean sprite types for SPYDER_PLAYER_NO_SOKOL (spyder player). No sokol
// dependency — GPU handles are opaque integers and draw is a no-op.
#pragma once

#include <player/Linalg.h>

#include <cstdint>
#include <vector>

namespace spyder {

struct Rect {
    float x = 0, y = 0, w = 0, h = 0;
};

struct Sprite {
    struct { uint32_t id = 0; } tex;
    struct { uint32_t id = 0; } view;
    int width = 0;
    int height = 0;

    Sprite() = default;
    ~Sprite();
    Sprite(Sprite&&) noexcept;
    Sprite& operator=(Sprite&&) noexcept;
    Sprite(const Sprite&) = delete;
    Sprite& operator=(const Sprite&) = delete;

    bool isNull() const { return tex.id == 0; }
    void destroy();
    void draw(const la::float4x4& mvp) const;
};

namespace detail {
uint64_t spriteReleaseCount();
}

class SpriteBatch {
public:
    SpriteBatch();
    ~SpriteBatch();
    void clear();
    void addSprite(const la::float4x4&, const Sprite&, uint32_t = 0xffffffffu);
    void addSprite(const la::float4x4&, const Sprite&, Rect, uint32_t = 0xffffffffu);
    void submit(const la::float4x4&);

private:
    struct Quad {};
    std::vector<Quad> quads_;
};

} // namespace spyder

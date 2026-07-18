// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
#pragma once

#ifdef __OBJC__
#import <QuartzCore/CAMetalLayer.h>

@interface CaptureMetalLayer : CAMetalLayer
@end
#endif

#include <SDL3/SDL.h>
#include <cstdint>
#include <cstddef>

namespace capture {

void enableCapture(SDL_MetalView view);
bool hasDrawable();
bool readLastFrame(uint8_t* dst, int width, int height, size_t bytesPerRow);

} // namespace capture

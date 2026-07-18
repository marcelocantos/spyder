// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

#import "player_capture_apple.h"
#import <objc/runtime.h>
#import <Metal/Metal.h>
#import <SDL3/SDL_metal.h>
#include <spdlog/spdlog.h>

// Store last drawable in a global (safe — only one capture layer).
static id<CAMetalDrawable> g_lastDrawable = nil;
static CAMetalLayer* g_captureLayer = nil;

@implementation CaptureMetalLayer

- (id<CAMetalDrawable>)nextDrawable {
    id<CAMetalDrawable> d = [super nextDrawable];
    g_lastDrawable = d;
    return d;
}

@end

namespace capture {

void enableCaptureLayer(void* rawLayer) {
    CAMetalLayer* layer = (__bridge CAMetalLayer*)rawLayer;
    if (!layer) {
        SPDLOG_ERROR("enableCaptureLayer: null layer");
        return;
    }
    layer.framebufferOnly = NO;
    object_setClass(layer, [CaptureMetalLayer class]);
    g_captureLayer = layer;
    SPDLOG_INFO("Capture enabled via layer ({}x{})",
                (int)layer.drawableSize.width,
                (int)layer.drawableSize.height);
}

void enableCapture(SDL_MetalView view) {
    CAMetalLayer* layer = (__bridge CAMetalLayer*)SDL_Metal_GetLayer(view);
    if (!layer) {
        SPDLOG_ERROR("enableCapture: SDL_Metal_GetLayer returned null");
        return;
    }
    layer.framebufferOnly = NO;
    object_setClass(layer, [CaptureMetalLayer class]);
    g_captureLayer = layer;
    SPDLOG_INFO("Capture enabled ({}x{})",
                (int)layer.drawableSize.width,
                (int)layer.drawableSize.height);
}

bool hasDrawable() {
    return g_lastDrawable != nil;
}

bool readLastFrame(uint8_t* dst, int width, int height, size_t bytesPerRow) {
    id<CAMetalDrawable> drawable = g_lastDrawable;
    if (!drawable) return false;
    id<MTLTexture> texture = drawable.texture;
    if (!texture) return false;

    NSUInteger readW = MIN((NSUInteger)width, texture.width);
    NSUInteger readH = MIN((NSUInteger)height, texture.height);

    [texture getBytes:dst
          bytesPerRow:bytesPerRow
           fromRegion:MTLRegionMake2D(0, 0, readW, readH)
          mipmapLevel:0];

    return true;
}

} // namespace capture

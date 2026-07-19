// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
#pragma once

#include <cstdint>

#ifdef __cplusplus
extern "C" {
#endif

// Force the app to a specific orientation. Values match SessionConfig /
// SDL_ORIENTATION_* (and 0xFE = AnyLandscape). 0 = no-op.
// On platforms without native orientation control, this is a no-op.
void playerForceOrientation(uint8_t orientation);

// Returns the physical device orientation as SDL_DisplayOrientation.
// On iOS, reads UIDevice.current.orientation (independent of interface lock).
// On other platforms, returns SDL_ORIENTATION_PORTRAIT.
int playerGetPhysicalOrientation();

// Content-presentation compensation for portrait chassis + landscape session.
//
// When the physical display (UIScreen.nativeBounds) is portrait but the game
// requests landscape, iPadOS 26.4's Simulator host scale-to-fits the landscape
// UIKit glass into the portrait frame (black letterbox). iOS 18.6 instead
// rotates the landscape glass to full-bleed.
//
// To match full-bleed presentation without rotating the Simulator chrome, the
// iOS path may:
//   1. Drive UIKit in *portrait* so the window fills nativeBounds, and
//   2. Keep a landscape Metal drawable with a −90° view transform so content
//      appears as landscape-rotated-into-portrait (M4 oracle class).
//
// playerContentRotationCwDegrees() is 90 in that mode (else 0). Callers that
// size the swapchain (SokolContext) must honour it: drawable = landscape
// (swap of window pixels), and apply playerApplyLandscapeOnPortraitChassis().
int playerContentRotationCwDegrees(void);

// Apply (or refresh) the landscape-on-portrait-chassis view transform to the
// SDL/Metal UIView for the given portrait window pixel size. No-op if
// playerContentRotationCwDegrees() == 0 or view is null.
void playerApplyLandscapeOnPortraitChassis(void* uiView /* UIView* */,
                                           int portraitPixelW,
                                           int portraitPixelH);

#ifdef __cplusplus
}
#endif

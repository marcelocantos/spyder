// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// Display-cutout insets only (camera notch / Dynamic Island / punch-hole).
// Desktop/iOS stubs return zeros; Android JNI returns displayCutout insets.
#pragma once

namespace spyder {

// Matches spyder::SafeAreaInsets field order (cutout insets in px here).
struct SafeAreaInsets {
    float y0 = 0;
    float y1 = 0;
    float x0 = 0;
    float x1 = 0;
};

// Pixel units. Zero on desktop and platforms without a cutout query.
SafeAreaInsets queryDisplayCutoutInsets();

} // namespace spyder

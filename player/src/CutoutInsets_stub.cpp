// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// Non-Android stub for queryDisplayCutoutInsets. iOS / desktop / wire-
// mode hosts populate cutouts via SDL's safe-area path (or zeros),
// not via this helper. Returns zeros — the caller's iOS / Apple path
// uses SDL_GetWindowSafeArea directly for both rects.

#include "CutoutInsets.h"

namespace spyder {

SafeAreaInsets queryDisplayCutoutInsets() { return {}; }

} // namespace spyder

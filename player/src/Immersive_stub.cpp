// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// Desktop (and non-iOS Apple) impl of spyder::applyImmersive — no-op.
// iOS uses Immersive_apple.mm; Android uses Immersive_android.cpp.

#include "Immersive.h"

namespace spyder {

void applyImmersive(bool /*enabled*/) {}

} // namespace spyder

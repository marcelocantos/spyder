// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// Engine-internal: apply the SessionHostConfig.immersive flag.
// Platform impls live in Immersive_android.cpp / Immersive_apple.mm /
// Immersive_stub.cpp.
#pragma once

namespace spyder {

// Hide system chrome so direct and stream glass share a deterministic
// pixel surface (status bar clock/battery are non-deterministic).
// Android: WindowInsetsController. iOS: swizzle prefersStatusBarHidden.
// Desktop: no-op. See SessionHostConfig.immersive.
void applyImmersive(bool enabled);

} // namespace spyder

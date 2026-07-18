// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// No-op orientation stub for platforms where SDL_HINT_ORIENTATIONS suffices
// (Android) or orientation lock is not applicable (desktop).

#include "player_orientation.h"

void playerForceOrientation(uint8_t) {}

int playerGetPhysicalOrientation() { return 3; } // SDL_ORIENTATION_PORTRAIT

int playerContentRotationCwDegrees() { return 0; }

void playerApplyLandscapeOnPortraitChassis(void*, int, int) {}

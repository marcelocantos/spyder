// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// Re-exports linalg's full alias set into `spyder::la`. Player headers
// include this so code can write `spyder::la::float2` /
// `spyder::la::float4x4` / `spyder::la::int3`.
// `using namespace spyder::la;` brings the short forms in unqualified.
//
// Sub-namespace `la` (not directly into `spyder::`) is deliberate —
// keeps linalg's 96 aliases out of `spyder::` autocomplete and marks
// types that came from linalg rather than the player surface.
#pragma once

#include <linalg.h>

namespace spyder::la {
using namespace linalg::aliases;

// Re-export common matrix/vector ops so `spyder::la::mul`, `spyder::la::inverse`,
// etc. resolve without a separate `using linalg::...` at the call site.
// linalg's free functions live in the `linalg` namespace itself (not
// `aliases`), so they're not picked up by the `using namespace` above.
using linalg::mul;
using linalg::inverse;
using linalg::transpose;
using linalg::dot;
using linalg::cross;
using linalg::length;
using linalg::length2;
using linalg::normalize;
using linalg::distance;
using linalg::lerp;
using linalg::clamp;
using linalg::minelem;
using linalg::maxelem;
}

// A semantic alias for a straight-alpha RGBA colour (components in [0, 1]).
// Promoted to `spyder::` (not `spyder::la`) because it's a domain concept, not a raw
// linalg alias — used across the rendering surface (spyder::debug, text, …).
namespace spyder { using Color = la::float4; }

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
#pragma once

#include <player/Linalg.h>

#include <string>

namespace spyder {

// A reference to a font file suitable for TTF_OpenFont /
// TTF_OpenFontIndex / stb_truetype etc.
struct FontRef {
    std::string path;       // Absolute file path
    int faceIndex = 0;      // For .ttc collections; 0 for single-face .ttf
};

// Resolve a font URI to a FontRef.
//
// Supported schemes:
//   "system:<name>"   System-provided font by logical name (sans-serif,
//                     sans-serif-bold, serif, serif-bold, monospace,
//                     monospace-bold) or platform-native family / PS
//                     name (e.g. "Helvetica Neue", "Krungthep" on
//                     Apple).
//   "file:<path>"     Explicit absolute file path.
//   "<path>"          Relative path, resolved via spyder::resource().
//
// Throws std::runtime_error if a `system:` URI cannot be resolved
// (logical name unknown on this platform, or named family / PS name
// not installed). `file:` and bundle-resource paths are pass-through —
// no readability check; the caller's first read will surface a missing
// file in its own loud way.
//
// Results are memoized for the lifetime of the process; a URI that
// failed once will throw immediately on subsequent calls.
FontRef resolveFont(const std::string& uri);

} // namespace spyder

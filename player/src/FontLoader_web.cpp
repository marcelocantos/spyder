// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// Browser FontLoader (🎯T101). The browser sandbox has no system font
// files to hand out, and the SP2S MakeText path ships the font blob over
// the wire (content-addressed) — resolveFont is only reached by SVG text
// and other system-font consumers, which degrade without it.

#include <player/FontLoader.h>

#include <stdexcept>

namespace spyder {

FontRef resolveFont(const std::string& uri) {
    if (uri.rfind("file:", 0) == 0) return {uri.substr(5), 0};
    // No system font database in the browser sandbox; MakeText uses
    // wire-shipped font blobs and never calls this.
    throw std::runtime_error("FontLoader(web): cannot resolve '" + uri +
                             "' — no system fonts in the browser sandbox");
}

} // namespace spyder

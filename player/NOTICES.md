# Third-Party Notices — spyder player

The spyder player (`player/` → `bin/player`) bundles and links against
the following third-party libraries. All paths are relative to
`player/`. This vendor tree is a snapshot of the
[ge](https://github.com/squz/ge) engine's vendored dependencies, pruned
to what the player builds against; ge's `NOTICES.md` is the upstream of
this file.

---

## Licensing gaps and warnings

### Triangle (J. R. Shewchuk) — header only

`vendor/include/triangle.h` has a **non-standard, restrictive licence**:
free for private, research, and institutional use; commercial
distribution only by direct arrangement with the author. The player
ships only the header — the implementation (`triangle.c`) is not
vendored and nothing in `player/src/` includes `triangle.h`, so no
Triangle code is compiled into `bin/player`. The header is retained
solely to keep the vendor tree in sync with ge.

### FFmpeg (prebuilt, Android only)

FFmpeg is vendored as prebuilt static libraries under
`vendor/ffmpeg/lib/android-arm64/` (libavcodec, libavutil, libswscale),
used only by `src/VideoDecoder_ffmpeg.cpp` (Android H.264 leg). The
build was configured **without GPL components** (`CONFIG_GPL 0` in
`vendor/ffmpeg/include/config.h`), so **LGPL-2.1** applies. The player
links these statically; LGPL §6(d) is satisfied by providing the full
corresponding source at `vendor/github.com/FFmpeg/FFmpeg` together with
the build configuration recorded in `vendor/ffmpeg/include/config.h`,
which lets users rebuild and relink a modified FFmpeg.

---

## Vendored libraries

### FFmpeg
- Source: https://github.com/FFmpeg/FFmpeg (full source vendored at
  `vendor/github.com/FFmpeg/FFmpeg`; prebuilt subset at `vendor/ffmpeg/`)
- Licence: LGPL-2.1-or-later (compiled without GPL components)
- Copyright: © 2000–2024 the FFmpeg developers.
- Notice: See _Licensing gaps and warnings_ above. Licence text:
  `vendor/github.com/FFmpeg/FFmpeg/COPYING.LGPLv2.1`.

### SDL (SDL3)
- Source: https://github.com/libsdl-org/SDL (source at
  `vendor/github.com/libsdl-org/SDL`; lifted headers and prebuilt libs
  at `vendor/SDL/` and `vendor/sdl3/`)
- Licence: zlib
- Copyright: © 1997–2025 Sam Lantinga.
- Notice: Permission is granted to anyone to use this software for any
  purpose, including commercial applications, and to alter it and
  redistribute it freely, subject to: (1) the origin is not
  misrepresented, (2) altered source versions are clearly marked, and
  (3) the notice is not removed from source distributions. See
  `vendor/github.com/libsdl-org/SDL/LICENSE.txt`.

### SDL_image (SDL3_image)
- Source: https://github.com/libsdl-org/SDL_image
- Licence: zlib
- Copyright: © 1997–2026 Sam Lantinga.
- Notice: Same terms as SDL above. See
  `vendor/github.com/libsdl-org/SDL_image/LICENSE.txt`.

### SDL_ttf (SDL3_ttf)
- Source: https://github.com/libsdl-org/SDL_ttf
- Licence: zlib
- Copyright: © 1997–2025 Sam Lantinga.
- Notice: Same terms as SDL above. See
  `vendor/github.com/libsdl-org/SDL_ttf/LICENSE.txt`. Pulls in
  FreeType, HarfBuzz, plutosvg, and plutovg (attributed below).

### FreeType (via SDL_ttf, and lifted headers at vendor/freetype/)
- Source: https://github.com/freetype/freetype
- Licence: FreeType Licence (FTL, BSD-style) OR GPL-2.0 — licensee's
  choice; the player consumes under FTL.
- Copyright: © 1996–2024 David Turner, Robert Wilhelm, Werner Lemberg
  and the FreeType Project.
- Notice: Portions of this software are copyright © The FreeType
  Project (www.freetype.org). All rights reserved. See
  `vendor/github.com/libsdl-org/SDL_ttf/external/freetype/LICENSE.TXT`.

### HarfBuzz (via SDL_ttf)
- Source: https://github.com/harfbuzz/harfbuzz
- Licence: "Old MIT" (MIT-style)
- Copyright: © 2010–2022 Google, Inc. and contributors.
- Notice: See `vendor/github.com/libsdl-org/SDL_ttf/external/harfbuzz/COPYING`.

### plutosvg / plutovg (via SDL_ttf)
- Source: https://github.com/sammycage/plutosvg,
  https://github.com/sammycage/plutovg
- Licence: MIT
- Copyright: © 2020–2025 Samuel Ugochukwu.
- Notice: See `vendor/github.com/libsdl-org/SDL_ttf/external/plutosvg/LICENSE`
  and `.../external/plutovg/LICENSE`.

### lunasvg (and nested plutovg, with FreeType/stb portions)
- Source: https://github.com/sammycage/lunasvg (at `vendor/lunasvg/`)
- Licence: MIT. The nested plutovg's `plutovg-ft-*` files derive from
  FreeType under the FTL; its `plutovg-stb-*` files are Sean Barrett's
  stb libraries (MIT OR public domain).
- Copyright: © 2020–2025 Samuel Ugochukwu; FreeType portions © The
  FreeType Project; stb portions © Sean Barrett.
- Notice: See `vendor/lunasvg/LICENSE` and the FTL/stb licence texts
  within the nested plutovg tree.

### asio
- Source: https://github.com/chriskohlhoff/asio (headers at `vendor/asio/`)
- Licence: Boost Software Licence 1.0 (BSL-1.0)
- Copyright: © 2003–2025 Christopher M. Kohlhoff.
- Notice: Distributed under the Boost Software License, Version 1.0.
  See http://www.boost.org/LICENSE_1_0.txt.

### spdlog
- Source: https://github.com/gabime/spdlog (at `vendor/spdlog/`)
- Licence: MIT
- Copyright: © 2016–present Gabi Melman and spdlog contributors.
- Notice: See `vendor/spdlog/LICENSE`.

### deepparser (liteparser)
- Source: https://github.com/marcelocantos/deepparser (at `vendor/deepparser/`)
- Licence: MIT
- Copyright: deepparser authors — see the file headers.

---

## Single-header and amalgamation files (vendor/include/, vendor/src/)

### doctest.h
- Source: https://github.com/doctest/doctest — MIT.
- Copyright: © 2016–2023 Viktor Kirilov.

### earcut.hpp
- Source: https://github.com/mapbox/earcut.hpp — ISC.
- Copyright: © Mapbox contributors.

### linalg.h
- Source: https://github.com/sgorsten/linalg — The Unlicense (public domain).

### lz4.h / lz4.c
- Source: https://github.com/lz4/lz4 — BSD 2-Clause.
- Copyright: © Yann Collet. All rights reserved. See file headers.

### minimp3.h / minimp3_ex.h
- Source: https://github.com/lieff/minimp3 — CC0-1.0 (public domain dedication).

### nlohmann/json (json.hpp)
- Source: https://github.com/nlohmann/json — MIT.
- Copyright: © 2013–2023 Niels Lohmann.

### sha1.h
- Written in-house; based on RFC 3174. Public domain.

### sqlift.h
- Source: https://github.com/squz/sqlift — Apache-2.0.
- Copyright: © 2026 Marcelo Cantos.

### sqlpipe.h / sqlpipe.cpp
- Source: https://github.com/marcelocantos/sqlpipe (dist amalgamation) —
  Apache-2.0.
- Copyright: © 2026 The sqlpipe Authors. Bundles sqlift + sqldeep.

### sqlite3.h / sqlite3.c
- Source: https://www.sqlite.org/ — public domain. "The author
  disclaims copyright to this source code."

### stb_image.h / stb_image_write.h
- Source: https://github.com/nothings/stb — public domain (alt: MIT).
- Copyright: Sean Barrett and contributors.

### triangle.h
- Source: http://www.cs.cmu.edu/~quake/triangle.html —
  **non-standard licence; see warning at the top of this file.**
- Copyright: © 1996, 2005 Jonathan Richard Shewchuk.

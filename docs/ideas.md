# Ideas

Parking lot for product and tech ideas that were looked at and are not current work. An entry stays here until a concrete requirement reopens it.

## libgav1 / AV1 (stream player)

**Status:** parked (2026-10-01)

H.264 decode in the stream player (`player/`, `bin/player`) is frozen — do not extend it. SP2S (command stream) is the default transport.

[libgav1](https://chromium.googlesource.com/codecs/libgav1/) was evaluated as a possible parallel AV1 software decode path.

**Recommendation:** spike-first only if the product still needs a compressed-video rung beside SP2S. Otherwise reject and leave it parked.

**Blocker:** does it solve a problem we need solved? As of 2026-10-01, no clear need. Parked pending a concrete compressed-video requirement (for example a thin client over slow links).

**If reopened:**

- Tiny Colossus cmake spike: libgav1 with `LIBGAV1_THREADPOOL_USE_STD_MUTEX=1`, plus an IVF harness.
- Leave the VideoToolbox, FFmpeg, and stub H.264 paths untouched.
- Consider a hardware AV1 and dav1d bake-off.
- Add a new `Av1Decoder` and a `kTransportAV1` wire. Do not edit `VideoDecoder_*`.

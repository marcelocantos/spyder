<!--
Copyright 2026 Marcelo Cantos
SPDX-License-Identifier: Apache-2.0
-->

# Recording a ged golden corpus (🎯T91.1)

The `gedharness` (`cmd/gedharness`, `internal/gedharness`) records `ged`'s
control-plane behaviour as a golden corpus, then diffs spyder's app-channel
against it as each capability migrates (🎯T91.2+). `ged` is the runnable
reference, so a recording needs a **ge server (game) connected to ged over
the brokered sideband**. This runbook captures the exact steps — several are
non-obvious.

## Prerequisites (one-time, in the `ge` repo)

The shipped `bin/tiltbuggy` is a `GE_DIRECT_ONLY` (standalone) build and the
shipped `bin/ged` was stale — both had to be revived. These are **ge-repo
changes** (the T34 bridge revival); commit them there as you see fit.

1. **Enable the brokered build** (`ge/Module.mk`):
   - `ge/SRC = $(ge/SRC_DIRECT) $(ge/SRC_BROKERED)` (was `$(ge/SRC_DIRECT)`).
   - Drop `-DGE_DIRECT_ONLY` from `ge/CXXFLAGS_BASE` (~line 270). Desktop is
     dev-only (no desktop distribution), so brokered is the right default;
     the stale `Module.mk:99` "still reference bgfx" comment is wrong (bgfx
     was retired in T38 — the remaining mentions are comments).

2. **Sync the drifted RenderHost interface** (`ge/src/bridge/ServerWireBridge.*`
   + `SessionHost_brokered.mm`): the sokol port (T34/T101) unified
   `beginFrame()`/`endFrame()` into `refreshFrame(float dt)`. `ServerWireBridge`
   still declared the old pair as `override`. Fix: `beginFrame` →
   `refreshFrame(float dt) override`; `endFrame` → a non-virtual
   `captureFrame(uint32_t)`; update the run-loop call sites in
   `SessionHost_brokered.mm` (~lines 186/195). Rendering stays stubbed and is
   gated on an active player session, so the control-plane sideband is
   unaffected. (~14 lines across 4 files.)

3. **Build**:
   ```sh
   make -C ge/sample/tiltbuggy          # produces a brokered bin/tiltbuggy
   ( cd ge/ged && go build -o ../../bin/ged . )   # ged protocol v6 (source), not the stale v5 binary
   ```
   `ged`'s `//go:embed web/dist/*` needs at least one file; if the dashboard
   UI isn't built, stub `ged/web/dist/index.html` (dashboard.go tolerates an
   empty FS).

## Record

```sh
# 1. start ged (headless)
ge/../bin/ged --no-open --port 42069 &

# 2. start the brokered fixture (headless — no window)
ge/sample/tiltbuggy/bin/tiltbuggy --brokered &
#    ged /api/info should now show {"connected":true,"servers":[{"name":"tiltbuggy",...}]}

# 3. record
go run ./cmd/gedharness record --url http://localhost:42069 \
    --fixture tiltbuggy --out testdata/gedcorpus/tiltbuggy_baseline.json
```

## Known limitations / next steps

- **tiltbuggy defines no tweaks**, so `tweaks:[]` is correct. Exercising
  `tweak_list/get/set/reset` needs a fixture that has tweaks (add a couple to
  tiltbuggy, or use another sample).
- **logs / tweak_* have no plain-HTTP route in the recorder yet.** ged serves
  the full control plane (info, tweak_list/get/set/reset, logs) as **MCP tools**
  at `/mcp` (mark3labs/mcp-go, which spyder already vendors) — the canonical
  interface spyder's app-channel must replicate. Driving those via an mcp-go
  client (session handshake) is the recorder's next completion; the current
  recorder uses the `/api/info` + `/api/tweaks` REST subset.
- **Producing log/tweak activity** needs an active session — connect a player
  (`ge/tools/player.cpp` → `bin/player`) so the game loop runs.

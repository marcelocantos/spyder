# Plateau P — closed control plane (2026-07-11)

**One control plane, pure engine, one trusted loop.**

| Layer | Owner |
|---|---|
| Simulation, render, encode, wire schema, app-channel client | **ge** |
| Devices, launch, reserve, inspect, tweak, logs, dashboard, stream relay | **spyder** |

## Closed in this plateau

- ged daemon retired (ge 🎯T145); spyder is the only control plane.
- App-channel tweaks/logs/info for direct-mode apps (T91.2/T91.3).
- Spyder dashboard for inspect UX (T91.5).
- H.264 opaque relay + server-mode + native player (T91.4, T92.2.2/2.3).
- `app_exec` single MCP entry; desktop + factory launch paths.
- Graph honesty: deferred futures (pigeon, command-stream, health platform,
  app-channel extensions, browser player polish) set_aside until a deliberate
  next theme.

## Supported developer loop

1. `brew services start spyder` (or `spyder serve`)
2. Launch app (device / sim / desktop) via spyder
3. Inspect via `app_exec` or `/dashboard` (tweaks, logs, screenshot)
4. Optional: server-mode stream → spyder relay → `bin/player`

## Next themes (pick one; do not run in parallel)

| Theme | Examples |
|---|---|
| R1 Reliability | T89 tunnel re-establish, os_log depth |
| R2 Ship a game | multimaze / IAP leaves on ge |
| R3 Stream polish | browser player, color path, UDS |
| R4 Remote transport | pigeon — only when you reopen it |
| R5 Engine ergonomics | ge::app wrapper, debug layer, audio |

Pigeon remains **out of scope** until declared ready.

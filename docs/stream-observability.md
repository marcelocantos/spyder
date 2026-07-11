# Stream path observability

Cross-cutting contract for attributing lag on the brokered H.264 path
without packet capture. Spyder is the hub; ge's server and player are
the endpoints. Target: spyder 🎯T96, ge 🎯T149.

## Topology

```
game server (GE_SERVER_BUILD)          player (native / browser)
  │ GE_SERVER=host:port                  │ stream_addr / --host
  │ /ws/server?name=…                    │ /ws/wire or /stream/player/<name>
  ▼                                      ▼
              spyder streamrelay :3030
                 session_id = sN
```

All three parties must be greppable by the same `session_id`.

## Path class

Every peer address is classified once at attach:

| Class      | Meaning                                      |
|------------|----------------------------------------------|
| `loopback` | 127.0.0.0/8 or ::1                           |
| `lan`      | RFC1918 / ULA / link-local                   |
| `public`   | Routable public address                      |
| `unknown`  | Unparseable host                             |

This answers "local vs internet" without guessing from logs.

## Spyder: `GET /stream/sessions`

JSON:

```json
{
  "sessions": [
    {
      "session_id": "s2",
      "server_name": "tiltbuggy",
      "player_remote": "192.168.1.193:40936",
      "player_path_class": "lan",
      "server_remote": "127.0.0.1:51606",
      "server_path_class": "loopback",
      "wire_remote": "127.0.0.1:52469",
      "wire_path_class": "loopback",
      "age_ms": 45000,
      "frames_s2p": 1200,
      "bytes_s2p": 4800000,
      "frames_p2s": 40,
      "bytes_p2s": 3200,
      "max_frame_bytes_s2p": 180000,
      "last_frame_bytes_s2p": 4200,
      "avg_frame_bytes_s2p": 4000,
      "fps_avg_s2p": 26.7,
      "bytes_per_sec_avg_s2p": 106666.7,
      "fps_avg_p2s": 0.9,
      "bytes_per_sec_avg_p2s": 71.1,
      "fps_1s_s2p": 60.1,
      "bytes_per_sec_1s_s2p": 2400000,
      "fps_1s_p2s": 1.0,
      "bytes_per_sec_1s_p2s": 80
    }
  ]
}
```

Opaque-pipe metrics (no H.264 decode — one WebSocket binary message = one "frame"):

| Field | Meaning |
|-------|---------|
| `*_s2p` | server → player (video messages) |
| `*_p2s` | player → server (input messages) |
| `frames_*` / `bytes_*` | lifetime totals |
| `fps_avg_*` / `bytes_per_sec_avg_*` | lifetime rates since attach |
| `fps_1s_*` / `bytes_per_sec_1s_*` | ~1s rolling window (use for hitch spotting) |
| `max_frame_bytes_s2p` | largest video message (keyframe bloat) |
| `last_frame_bytes_s2p` | most recent video message size |
| `avg_frame_bytes_s2p` | `bytes_s2p / frames_s2p` |

Attach/detach log lines include the same remotes and path classes.

Catalogue `GET /stream/servers` is unchanged (name + session count).

## Ge app-channel (🎯T149) — preferred for encode/player metrics

**Wire is already MessagePack** (length-prefixed), not JSON text. In-process
APIs use `nlohmann::json` then `to_msgpack`.

**Identity** is the app-channel connection (spyder session). Payloads do
**not** re-stamp device/app id.

| Path | When | Contents |
|------|------|----------|
| `perfEmit` / existing `perf` push | ~1 Hz with game perf | Scalars: `stream_fps`, `stream_bps`, `stream_mk`, `stream_w`/`h`; player: `player_max_gap_ms`, `player_max_pump_ms`, … |
| `push("stream_stats", …)` | ~1–2 Hz window | Compact keys: server `role,name,w,h,dt,fps,bps,n,nk,np,ak,mk,ap,mp` optional `sid` (relay pipe id only); player `role,n,dec,gaps,mg,mp,…` |
| `app_state` slice `stream` | pull | Last server snapshot |

Optional `sid` = streamrelay pipe id (`sN`) for joining `/stream/sessions` —
not app-channel identity.

## Ge logs (secondary)

`StreamStats` / `PlayerLog` spdlog lines remain for grepping when no channel.

## Attribution playbook

| Symptom                         | First check                                      |
|---------------------------------|--------------------------------------------------|
| Player `maxGap` / `pump max` ↑  | `/stream/sessions` `max_frame_bytes_s2p` + path  |
| `player_path_class=public`      | Accidental WAN / VPN hairpin                     |
| `wire_path_class=loopback` OK, player LAN, big max frame | Encode bitrate / resolution / keyframe size |
| Seq gaps on player, low relay bytes | Drop before relay (server send) or after (player decode) — compare rates |

## Non-goals

- Full OpenTelemetry export (can layer later on the same counters)
- Decoding H.264 inside spyder
- Production multi-tenant auth (dev LAN only, as streamrelay itself)

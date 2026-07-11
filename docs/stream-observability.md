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
      "frames_s2p": 1200,
      "bytes_s2p": 4800000,
      "frames_p2s": 40,
      "bytes_p2s": 3200,
      "max_frame_bytes_s2p": 180000,
      "age_ms": 45000
    }
  ]
}
```

- `*_s2p` = server → player (video)
- `*_p2s` = player → server (input)
- `max_frame_bytes_s2p` flags keyframe bloat (large spikes → Wi-Fi stalls)

Attach/detach log lines include the same remotes and path classes.

Catalogue `GET /stream/servers` is unchanged (name + session count).

## Ge server (🎯T149)

While a player is attached, log ~every 2s:

- `session_id` (from `player_attached`)
- encode `WxH`, nominal fps
- avg/max keyframe bytes, avg/max P-frame bytes
- frames encoded in the window

## Ge player (🎯T149)

PlayerLog (or successor) includes:

- `session_id` once known (optional: relay can inject via a small text
  control message later; until then player logs dialed `host:port`)
- existing pump / decode / gap metrics
- dialed address (path is inferable; spyder remains source of truth)

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

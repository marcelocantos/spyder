# Entropy audit — spyder (2026-08-22)

Full-mode audit (architecture + redundancy + SDLC) plus explicit hygiene
validation. Production code was not modified.

## Executive summary

- **Snapshot:** `/Users/marcelo/work/github.com/marcelocantos/spyder`,
  branch `master`, commit `837d201e52e734dbbac7a03eb7ddadfb780e108e`
  (`Add USB link-speed reporting and ceiling/anomaly to devices() (#155)`).
- **Initial dirty state (recorded before any write):** clean.
  `git status --porcelain=v1 -b` showed only `## master...origin/master`.
- **Date:** 2026-08-22.
- **Scope:** Go control plane (root + `internal/*` + `cmd/`), player
  first-party C++ (`player/` excluding vendor), CLI/MCP/REST surfaces,
  CI/release, docs, Starlark recipes. Languages judged: Go, C++, Bash,
  SQLite (poolstore), a small web/wasm player, Swift/Kotlin fixture apps.
- **Headline mechanism:** one verb table is the runtime source of truth
  (`Handler.toolHandlers`), but the *published* contracts around it —
  MCP schema text, `agents-guide.md`, `STABILITY.md`, and the laptop
  `TEST-REPORT.json` attestation — have already drifted from the shipped
  path. The package DAG is acyclic and the REST/MCP dispatch seam is
  sound; entropy sits in competing documents and in a hub package that
  every new verb must edit.
- **Highest-consequence findings:** ENT-001 (`max_duration_ms` 120 s vs
  10 min), ENT-002 (`go test ./...` failed on this snapshot while
  `TEST-REPORT.json` still says pass), ENT-003 (`STABILITY.md` still
  catalogues per-verb MCP tools after 🎯T88 made `app_exec` the only
  advertised MCP tool).
- **Unverified residue:** live device tests (`SPYDER_LIVE_*` not set for
  this run), `make -C player check-wire` / player-web build, govulncheck,
  clone detection, parent `go.work` blocked default `go list` /
  staticcheck without `GOWORK=off`.

## Scope and exclusions

**In scope**

- Root Go module `github.com/marcelocantos/spyder` (32 packages).
- `player/` first-party sources (~8.8k lines of `.cpp/.h/.mm/.c` outside
  vendor).
- `scripts/lib/*.star` and `internal/scriptlib/recipes/*.star`.
- `.github/workflows/release.yml`, `Makefile`, `TEST-REPORT.json`,
  `STABILITY.md`, `CLAUDE.md`, `agents-guide.md`, `docs/plateau-p.md`.
- Fixture apps under `ios/` and `android/` (tracked source only).

**Named exclusions (not silent omissions)**

| Tree | Role | Why skipped as production code |
|---|---|---|
| `player/vendor/` (1068 of 1475 tracked files; LFS `.a`; git submodules for FFmpeg/SDL) | Vendored third-party | Clone/complexity metrics would be dominated by upstream |
| `player/build/`, `player/web/dist/` (gitignored) | Generated | Not in the snapshot; browser player is opt-in (`internal/playerweb`) |
| `player/android/app/` build artefacts, `android/BouncingBall/app/build/` | Generated | Workspace listing is local build output; git tracks ~12 Android / ~15 iOS fixture files |
| `devimages/`, `selfIdentity.plist`, `bin/` | Runtime / go-ios scratch | gitignored |
| `.claude/worktrees/` | Local agent worktrees | gitignored via `.claude/` |
| Parent `../go.work` | Workspace file outside this repo | Forces `GOWORK=off` for module commands |

No `AGENTS.md` at repo root (`CLAUDE.md` + `agents-guide.md` only). No
`hygiene.yaml`. No prior `docs/audits/` reports.

## Commands run

All from the repo root unless noted. `GOWORK=off` was required because
`/Users/marcelo/work/github.com/marcelocantos/go.work` does not list this
module; without it `go list ./...` fails with “directory prefix . does not
contain modules listed in go.work”.

| Command | Version / notes | Exit | Shipped vs auxiliary | Limitation |
|---|---|---|---|---|
| `git rev-parse --abbrev-ref HEAD; git rev-parse HEAD; git status --porcelain=v1 -b` | git | 0 | provenance | Snapshot identity |
| `go version` | `go1.26.4 darwin/arm64` | 0 | toolchain | — |
| `GOWORK=off go list ./...` | 32 packages | 0 | auxiliary | Workspace env |
| `gofmt -l .` / `make fmt-check` | Makefile `fmt-check` | 2 | shipped gate | 8 files differ (alignment) |
| `GOWORK=off go vet ./...` | go 1.26.4 | 0 | shipped (`make vet`) | — |
| `GOWORK=off go test -timeout 5m ./...` | hermetic unit path | 1 | **shipped** (`make test`, `CLAUDE.md`) | `TestDesktopAdapter_LaunchLifecycle` failed; isolated rerun passed |
| `GOWORK=off go test -timeout 30s -count=1 ./internal/device/ -run TestDesktopAdapter_LaunchLifecycle` | isolation check | 0 | auxiliary | Confirms load-sensitive flake |
| `~/.claude/skills/hygiene/hygiene_check.py` | uv-run validator | 1 | hygiene skill | `FileNotFoundError: hygiene.yaml` |
| Internal import-graph script (`go list -f Imports`) | Python 3.13.0 | 0 (cycles none) | auxiliary | Fan-in print aborted after cycle check; edges complete |
| `git log --since=2026-02-01 --name-only` churn | 206 commits | 0 | auxiliary | Deleted packages (`autoawake`, `pmd3bridge`) inflate historical counts |
| `diff -rq scripts/lib internal/scriptlib/recipes` | — | 0 (identical) | auxiliary | No test owns this equality |
| `staticcheck ./internal/mcp` | 2025.1.1 | n/a | auxiliary | Failed: module not in parent `go.work`; not retried under `GOWORK=off` |
| `jscpd` / `govulncheck` | unavailable | n/a | — | Not installed; not added |
| `make -C player check-wire` | not run | — | player oracle | Residue |
| Live `go test -run '_Live$'` | not run | — | live device path | `SPYDER_LIVE_UDID` unset this session |

`TEST-REPORT.json` (committed attestation, **not** re-run as `make test-report`
because that rewrites a tracked file): `generated_at` 2026-08-16T00:54:05Z,
`overall: pass`, one commit behind HEAD.

## Observed architecture

### Deployable units

1. **`bin/spyder`** — HTTP daemon (`serve`) + CLI proxies. Homebrew service
   is the production MCP. Default bind `127.0.0.1:3030`; brew sets
   `SPYDER_ADDR=:3030`.
2. **`bin/ios`** — bundled go-ios CLI, spawned as `ios tunnel start --userspace`.
3. **`bin/spyder-killusbmuxd`** — privileged helper for `doctor --fix`.
4. **`bin/player`** — C++ stream glass (SP2S default; H.264 frozen). Not in
   `release.yml`.
5. **Browser player** — same tree via Emscripten; served from disk at
   `/player/` when `player/web/dist` exists. Not in the Homebrew tarball
   (`internal/playerweb/playerweb.go` points at 🎯T102).

### Declared vs observed

**Agree**

- Spyder owns inventory, reservations, device adapters, app-channel, stream
  relay, dashboard; it does not own UI automation, xcodebuild, or ge’s
  renderer. Protocol-only coupling with ge is visible in `player/include/wire/Protocol.h`
  and `internal/streamrelay`.
- MCP advertised surface is `app_exec` only (`Definitions()`).
- REST `POST /api/v1/<tool>` and MCP dispatch share `Handler.Dispatch`
  (`internal/rest/rest.go`).
- CLI device tools proxy REST (`cli.go` `postTool`); they are not a second
  implementation of device logic.
- `device.Adapter` is the platform seam (iOS in-process go-ios, Android `adb`,
  desktop local exec).
- `health.Model` is an in-process entity SoT; wedge diagnosis is bridged via
  `wedge.DoctorFinding` (🎯T99.6).
- Internal Go import graph is a DAG (no cycles). Direction:
  `main` → `daemon` → `mcp` → {`device`, `appchannel`, `reservations`, …};
  `health` has zero internal imports.

**Inferred from code (not fully documented)**

- `internal/mcp` is the session-state hub: 54 Go files, 103 verbs in
  `toolHandlers()`, 17 internal imports. `tools.go` is 2040 lines; `cli.go`
  is 1870 lines.
- Starlark builtins are the same map as REST tool names (`TestExec_BuiltinCoversEveryAdvertisedVerb`).
- CLI exposes 36 subcommands, a convenience subset of the 103 verbs (no
  CLI↔verb parity test).
- Player lives in-tree as a second product (vendored SDL/FFmpeg/LFS) with
  a compile-time layout oracle (`make -C player check-wire`) that is not
  part of `make test`.

**Contradictions**

- `CLAUDE.md` Architecture: `main.go` subcommands are `serve` / `run` /
  `version`. Observed: `doctor`, `status`, `help-agent`, plus 36 CLI
  proxies (`main.go` usage string).
- `STABILITY.md` “MCP tools” table lists `devices`, `screenshot`, … as MCP
  tools. Observed MCP wire: `app_exec` only. REST still uses those names.
- `appExecDefinition` description says max 120 s; `maxExecDuration` is
  10 minutes; `agents-guide.md` still says 120 000 ms.
- `docs/plateau-p.md` still lists “H.264 opaque relay” as the plateau
  stream story; `CLAUDE.md` (2026-07-19) deprecates H.264 in place for SP2S.
- Bundled Starlark is `//go:embed recipes/*.star`; the documented repo
  mirror is `scripts/lib/*.star` (currently byte-identical, unenforced).

**Unknown intent (owner)**

- Whether `max_duration_ms` should be 120 s (agent-slot protection) or
  10 min (Unity cold-boot recipes). Code comments argue for 10 min; the
  MCP description and agent guide were not updated together.
- Whether `scripts/lib` is meant to stay a human-editable duplicate of
  `recipes/`, or should become the embed root.
- Whether the player belongs in this repo indefinitely, or should be a
  sibling once 🎯T102 packaging exists.

```
CLI  --HTTP POST-->  REST /api/v1/<verb>  -->  mcp.Handler.Dispatch
MCP /mcp  --app_exec Starlark-->  same toolHandlers() map
                                       |
                    +------------------+------------------+
                    |                  |                  |
                 device.Adapter    appchannel.Manager   reservations
                 (ios/android/desktop)                  inventory
                    |                  |
                  goios / adb      TCP MessagePack
                    |
              iostunnel (ios child)
```

## Dimension vector

| Dimension | State | Evidence summary | Change from baseline |
|---|---|---|---|
| Architecture topology | concern | Acyclic internal DAG; `mcp` is a 17-edge hub; player is a second in-tree product | n/a (first full audit) |
| Redundancy / sources of truth | concern | `toolHandlers` is a real SoT for verbs; duration, MCP catalogue, recipes, Protocol.h, plateau docs compete | n/a |
| Change amplification | concern | New verb: handler + `toolHandlers` + `legacyDefinitions` + agents-guide ± CLI ± deadline table | n/a |
| Local code quality | concern | `gofmt` dirty on HEAD; 2k-line files; `HandlerOption` functional options | n/a |
| Correctness / verification | concern | Shipped `go test ./...` failed this run; `TEST-REPORT.json` still `pass`; no per-PR CI by design | n/a |
| Security / dependencies | concern | No auth on MCP/REST; brew binds `:3030`; go-ios `replace` pin; no vuln scanner | n/a |
| Build / release / operations | concern | Tag-only `release.yml` (darwin-arm64); player/wasm not released; CLI may auto-spawn a second daemon | n/a |
| Documentation / governance | concern | `STABILITY.md` / `CLAUDE.md` / duration docs drift; hygiene undeclared | n/a |

Do not collapse this vector to a score.

## Findings

### ENT-001: `max_duration_ms` has three disagreeing authorities

- **Priority:** P1
- **Dimensions:** Redundancy / sources of truth; Documentation / governance; Correctness / verification
- **Status:** observed fact
- **Evidence:**
  - `internal/mcp/app_exec.go:46` `maxExecDuration = 10 * time.Minute`
  - `internal/mcp/app_exec.go:74-75` caps caller `max_duration_ms` at that constant
  - `internal/mcp/app_exec.go:613` MCP tool description: “Caps: wall-clock timeout (default 30s, **max 120s**)”
  - `internal/mcp/app_exec.go:623-624` parameter description: “capped at **600000**”
  - `internal/mcp/app_channel.go:1504` `run_script` schema: “max 600000”
  - `agents-guide.md:213` and `:252`: max **120 000 ms / 120 s**
- **Mechanism:** An agent that trusts the advertised MCP description or
  `agents-guide.md` will refuse or split scripts that the daemon will
  actually run for ten minutes. Conversely, a caller that passes
  `max_duration_ms=600000` is within the code and the parameter schema
  but outside the description and the agent guide. The two strings in
  `appExecDefinition()` already disagree with each other.
- **Blast radius:** every `app_exec` / `run_script` caller (MCP, REST,
  CLI `spyder run-script`). Dispatch deadline for these tools is
  `maxExecDuration + 5s` (`internal/mcp/dispatch_deadline.go:44-48`), so
  the *runtime* follows the 10-minute constant.
- **Counterevidence checked:** comment at `app_exec.go:43-45` explains
  Unity cold-boot recipes needing >2 min — that is the intended raise.
  No test asserts the advertised cap equals `maxExecDuration`.
- **Smallest coherent remediation:** pick one ceiling. Make
  `maxExecDuration` the only literal; generate or test-lock the MCP
  description, `run_script` schema, and the two `agents-guide.md`
  sentences against it.
- **Verification:** a test that parses `appExecDefinition()` /
  `legacyDefinitions()` for `max_duration_ms` and asserts the documented
  cap equals `int64(maxExecDuration / time.Millisecond)`, plus a grep
  oracle on `agents-guide.md` for that same number.
- **Ratchet candidate:** that test in `internal/mcp/app_exec_test.go`
  (already owns `TestExec_BuiltinCoversEveryAdvertisedVerb`).

### ENT-002: Shipped hermetic suite is red on HEAD; attestation still green

- **Priority:** P1
- **Dimensions:** Correctness / verification; Build / release / operations
- **Status:** observed fact (failure); inference (flake vs product bug)
- **Evidence:**
  - `GOWORK=off go test -timeout 5m ./...` exit 1.
    `TestDesktopAdapter_LaunchLifecycle` (`internal/device/desktop_test.go:18-52`):
    `stdout line 'ready' was not captured into LogRange (got 0 lines: [])`.
  - Same test isolated: `go test ./internal/device/ -run TestDesktopAdapter_LaunchLifecycle`
    exit 0 in 1.985s.
  - Test comment at `desktop_test.go:23-24` already records a prior
    full-suite flake of the same assertion.
  - `TEST-REPORT.json:3,8-10,19`: `generated_at` 2026-08-16, `go-unit`
    `status: pass`, `overall: pass`. HEAD is one commit later (`837d201e`,
    2026-08-21).
  - `CLAUDE.md:132-141` and `scripts/test-report.sh`: the laptop report
    *is* the merge attestation; there is no per-PR CI and no freshness
    check (SHA ratchet was removed).
  - During the failing package run, `goios`/`wedge` logged real USB
    devices and wrote
    `~/.spyder/wedge-snapshots/20260821T224706Z-no-such-device-udid.json`
    — unit tests are not fully hermetic against the host device stack.
- **Mechanism:** The only standing “did tests run?” artefact can be green
  while `make test` is red. A load-sensitive desktop log-capture test is
  in the default `./...` set, so full-suite noise becomes a false
  attestation or a false merge blocker depending on who last ran
  `make test-report`.
- **Blast radius:** every merge that treats `TEST-REPORT.json` as
  evidence; desktop `LogRange` (🎯T85) if the failure is a real race in
  `DesktopAdapter.LaunchApp` (`desktop.go:144-167` starts the process
  before the scan goroutines).
- **Counterevidence checked:** `go vet ./...` passed. Isolated desktop
  test passed. Live suite was not re-run. `hermeticity_test.go` only
  covers CLI `postTool` vs `~/.spyder`, not goios/wedge.
- **Smallest coherent remediation:** make `LaunchApp` attach scanners
  before `Start()` (or `Ready` the pipe), and fail the unit test closed
  if `LogRange` is empty after a short wait; keep `TEST-REPORT.json`
  regeneration on the same commit as the fix. Optionally skip or stub
  wedge snapshot writes when `SPYDER_LIVE_*` is unset.
- **Verification:** `go test -count=20 ./internal/device/ -run TestDesktopAdapter_LaunchLifecycle`
  under `go test ./...` load, plus a freshness policy for
  `TEST-REPORT.json` vs `HEAD`.
- **Ratchet candidate:** `make test` / `scripts/test-report.sh` must fail
  if `go-unit` is red; a cheap check that `TEST-REPORT.json` is not older
  than HEAD for files that `go test` exercises (the previous SHA check
  fought squash-merge — key off `git merge-base` or “report commit ==
  HEAD” at `/push` time instead).

### ENT-003: Stability catalogue still describes the pre-T88 MCP wire

- **Priority:** P1
- **Dimensions:** Documentation / governance; Redundancy / sources of truth
- **Status:** observed fact
- **Evidence:**
  - `internal/mcp/server.go:713-720` `Definitions()` returns only
    `appExecDefinition()` (🎯T88.3). Comment at `:711-712` still says
    “complete MCP tool definition list — core tools plus visual-regression
    tools…”.
  - `TestDefinitions_SingleEntryPoint` (`internal/mcp/app_exec_test.go:318-328`)
    locks that wire.
  - `STABILITY.md:8-16` commits 1.0 compatibility to “the MCP tool
    surface (names, input schemas, output shapes)”.
  - `STABILITY.md:40-47` table titled **MCP tools** starts with `devices`,
    `screenshot` (output still “MCP image content block”), etc.
  - `internal/mcp/tools.go:397-405` 🎯T114: screenshot default is a
    filesystem path, not inline PNG.
  - REST still accepts `/api/v1/<tool>` (`internal/rest/rest.go:3-14,46-49`).
- **Mechanism:** The compatibility document an agent or human uses to
  decide “what is MCP” is the old N-tool surface. Callers following
  `STABILITY.md` will look for MCP tools that are not advertised.
  Screenshot output shape in that table predates 🎯T114.
- **Blast radius:** MCP clients, 1.0 lock-in planning, any consumer of
  `STABILITY.md` as the public-surface list.
- **Counterevidence checked:** `agents-guide.md` and `README.md` correctly
  describe `app_exec` as the single MCP tool. Verb names remain REST +
  Starlark builtins. `legacyDefinitions()` is an intentional in-repo
  schema oracle, not the wire.
- **Smallest coherent remediation:** retitle the `STABILITY.md` table to
  “REST / Starlark verbs”; add a one-row MCP section for `app_exec`;
  update screenshot output to the 🎯T114 path default; fix the stale
  `Definitions()` comment.
- **Verification:** a test (or docs grep in `make bullseye`) that
  `STABILITY.md` does not list a heading `### MCP tools` with names other
  than `app_exec`.
- **Ratchet candidate:** extend `TestDefinitions_SingleEntryPoint` with a
  file assert on `STABILITY.md`, or a small `docs_parity_test.go`.

### ENT-004: Bundled Starlark has an unenforced repo mirror

- **Priority:** P2
- **Dimensions:** Redundancy / sources of truth; Change amplification
- **Status:** observed fact (two trees); inference (will drift)
- **Evidence:**
  - Shipped embed: `internal/scriptlib/lib.go:21-22`
    `//go:embed recipes/*.star`.
  - Documented mirror: `agents-guide.md:277-278`, `README.md` (`scripts/lib/`).
  - `diff -rq scripts/lib internal/scriptlib/recipes` — identical (14 files)
    on this snapshot.
  - `internal/scriptlib/lib_test.go` loads `skeleton` from the embed only;
    no test compares the two directories.
- **Mechanism:** Editing `scripts/lib` (the path the agent guide tells a
  human to version) does not change the binary. Editing `recipes/` without
  copying to `scripts/lib` makes the repo lie about what ships.
- **Blast radius:** 14 durable recipes (`deploy_and_sid`, `yw_mode_smoke`,
  explore/collect/regress set).
- **Counterevidence checked:** user overrides under `~/.spyder/scripts/`
  are a *third* layer by design (user wins). That layer is not the
  problem; the two in-repo copies are.
- **Smallest coherent remediation:** either embed `scripts/lib` directly,
  or add `TestRecipesMatchRepoMirror` that byte-compares the two dirs.
- **Verification:** that test fails if either side is edited alone.
- **Ratchet candidate:** `internal/scriptlib` test; optional
  `make_target` once hygiene is declared.

### ENT-005: `internal/mcp` is the change-amplification hub

- **Priority:** P2
- **Dimensions:** Architecture topology; Change amplification; Local code quality
- **Status:** observed fact (shape); inference (cost of the next verb)
- **Evidence:**
  - 54 Go files under `internal/mcp`; `tools.go` 2040 lines;
    `app_channel.go` 1507; `server.go` 1277.
  - `toolHandlers()` (`server.go:590-708`) lists 103 verbs; CLI
    `cliCommands` (`cli.go:52-90`) lists 36.
  - Import fan-out: `mcp` → appchannel, baselines, device, health,
    inventory, logcapture, network, paths, recording, reservations,
    runs, scriptlib, selector, simemu, streamrelay, usbspeed,
    visualdiff, wedge.
  - Churn since 2026-02-01: `internal/mcp/server.go` 42 commits,
    `internal/device/ios.go` 41, `internal/daemon/daemon.go` 35,
    `internal/mcp/tools.go` 24, `cli.go` 15.
  - `HandlerOption` functional options (`server.go:129-204`) — house
    `go.md` forbids this pattern for new APIs.
- **Mechanism:** A new capability that is “just another verb” still
  touches the hub map, a `legacyDefinitions()` builder (split across
  `server.go` / `tools.go` / `app_channel.go` / `tools_visual.go`),
  often CLI, `agents-guide.md`, and sometimes `dispatch_deadline.go`.
  The parity test (`TestExec_BuiltinCoversEveryAdvertisedVerb`) covers
  handlers↔schemas, not CLI, deadlines, or docs.
- **Blast radius:** every future verb; reviewers of `tools.go`.
- **Counterevidence checked:** REST does not duplicate handlers. The
  verb table comment at `server.go:590-593` is accurate for MCP vs
  Starlark. Splitting packages without moving the table would not
  reduce amplification. Functional options are existing API, not a
  new introduction this audit created.
- **Smallest coherent remediation:** keep one verb table; generate CLI
  stubs and deadline classes from it, or add a test that
  `toolDeadlineClass` names ⊆ `toolHandlers` keys (see ENT-011). Do
  not explode `mcp` into many packages until a second hub appears.
- **Verification:** `TestDeadlineNamesSubsetOfHandlers`; optional CLI
  coverage list as an allow-list, not a 1:1 requirement.
- **Ratchet candidate:** architecture test on the deadline/handler
  subset; later, `gocyclo`/`lines` only if a file-size ratchet is
  adopted deliberately.

### ENT-006: Stream wire layout is a copied header with a local-only oracle

- **Priority:** P2
- **Dimensions:** Redundancy / sources of truth; Architecture topology
- **Status:** observed fact
- **Evidence:**
  - `player/include/wire/Protocol.h:13-64` — magics, `kProtocolVersion = 9`.
  - `player/src/InputScript.h:17-19` — “byte-identical mirror of this
    header (same convention as Protocol.h)”.
  - `player/tools/check-wire-layout.cpp` + `player/Makefile` `check-wire`
    assert sizeof/magics of *this* copy.
  - `docs/streaming-data-plane-plan.md:7-16,39-42` — ge owns
    `include/ge/Protocol.h`; spyder relay is magic-agnostic.
  - `CLAUDE.md` transport note: H.264 deprecated in place; SP2S default.
  - `make test` / `release.yml` do not run `check-wire`. `release.yml`
    does not build `bin/player`.
- **Mechanism:** A ge encoder change that is not copied here compiles
  spyder’s player against a stale layout. `check-wire` cannot see ge.
  H.264 decode remains in the tree (`VideoDecoder_apple.mm`) as frozen
  residue, so stream bugs can still land on the deprecated rung.
- **Blast radius:** every glass (desktop, iOS, Android, wasm) and any
  ge server speaking SP2*.
- **Counterevidence checked:** relay tests pipe opaque magics
  (`TestRelay_PipesCommandStreamMagic` cited in the plan). Deliberate
  duplication of Protocol.h is the stated ge/spyder decoupling. Players
  are “dev tools only, never store-packaged” (`CLAUDE.md`).
- **Smallest coherent remediation:** run `make -C player check-wire` from
  `make test` or `make bullseye`; add a documented cross-repo check
  (hash/size of magics) against ge when both checkouts exist, skip
  closed otherwise.
- **Verification:** `make -C player check-wire` in the laptop
  `TEST-REPORT.json` as its own suite (not CI — player is macOS/C++).
- **Ratchet candidate:** `make_target: check-wire` once hygiene exists.

### ENT-007: Declared `gofmt` gate is red on `master`

- **Priority:** P2
- **Dimensions:** Local code quality; Documentation / governance
- **Status:** observed fact
- **Evidence:** `make fmt-check` exit 2. Files:
  `doctor.go`, `internal/appchannel/protocol.go`,
  `internal/device/ios_forward.go`, `internal/mcp/app_exec.go`,
  `internal/mcp/launch_player.go`,
  `internal/mcp/launch_player_live_test.go`,
  `internal/scriptlib/resolve.go`, `internal/wedge/monitor.go`.
  Diffs inspected: struct-tag alignment only. `Makefile:49-50` and
  `make bullseye` treat this as a blocking gate. `go vet` is green.
- **Mechanism:** The formatting oracle the Makefile claims is not what
  HEAD satisfies. Future `/push` or `make bullseye` fails on noise, or
  people stop running the gate.
- **Blast radius:** `make bullseye` / `pre-release`.
- **Counterevidence checked:** no functional changes in the gofmt diffs.
- **Smallest coherent remediation:** `gofmt -w` those eight files on a
  dedicated commit (out of scope for this audit).
- **Verification:** `make fmt-check` exit 0.
- **Ratchet candidate:** already declared (`fmt-check`); needs to be
  true.

### ENT-008: Production brew bind is unauthenticated on all interfaces

- **Priority:** P2
- **Dimensions:** Security / dependencies; Build / release / operations
- **Status:** observed fact (bind + no auth); accepted-risk documentation exists
- **Evidence:**
  - `main.go:41-47` documents no auth and loopback default.
  - `main.go:162-168` `SPYDER_ADDR` overrides that default.
  - `.github/workflows/release.yml:124` Homebrew service
    `SPYDER_ADDR: ":3030"`.
  - `agents-guide.md:1065-1066` “No authentication / encryption. LAN-only.”
  - `cli.go:146-159` on `ECONNREFUSED` to `127.0.0.1:3030`, CLI may
    `autoStartDaemon()` — a second listener vs the “one daemon only”
    rule in `CLAUDE.md`.
- **Mechanism:** The shipped service (not the loopback default) exposes
  screenshot, launch, deploy, and reservations to any host that can
  reach `:3030`. That is required for LAN glasses and app-channel
  dial-back. CLI auto-start can still produce the IPv4/IPv6 double-bind
  mismatch `CLAUDE.md` warns about.
- **Blast radius:** every brew-installed daemon on a shared network;
  any agent that shells `spyder devices` while brew is in a weird bind
  state.
- **Counterevidence checked:** loopback default is safe; LAN bind is
  explicit and documented; 🎯T76.4 is named as the upgrade path;
  `spyder-killusbmuxd` is correctly split out for sudo.
- **Smallest coherent remediation:** keep LAN bind; add a loopback-only
  mode flag in the formula caveats for laptops on untrusted Wi-Fi;
  make `autoStartDaemon` refuse if *any* process already listens on
  3030 (IPv4 or IPv6).
- **Verification:** a unit test around listen-preflight; manual
  `lsof -nP -iTCP:3030`.
- **Ratchet candidate:** later, optional token on non-loopback binds
  (product change — not a silent audit fix).

### ENT-009: Agent-facing architecture docs lag the tree

- **Priority:** P2
- **Dimensions:** Documentation / governance
- **Status:** observed fact
- **Evidence:**
  - `CLAUDE.md:81-82` — `main.go` subcommands `serve` / `run` / `version`
    only. `main.go:53-93` lists doctor, status, help-agent, and the
    device CLI.
  - `docs/plateau-p.md:15` still headlines H.264 relay as plateau-closed
    stream work; `CLAUDE.md` transport note (2026-07-19) freezes H.264.
  - `internal/goios/session.go:14` still mentions “autoawake convergence
    loop”; package `internal/autoawake` is gone.
- **Mechanism:** New work (and new agents) plan from `CLAUDE.md` /
  plateau and miss doctor/status/CLI/SP2S.
- **Blast radius:** agent sessions using `CLAUDE.md` as the map.
- **Counterevidence checked:** `agents-guide.md` is far closer to the
  product (app_exec, desktop, USB fields). README MCP section matches
  T88.
- **Smallest coherent remediation:** rewrite the Architecture bullet
  list in `CLAUDE.md`; add a one-line plateau erratum for SP2S; drop
  autoawake from goios comments.
- **Verification:** doc grep that `CLAUDE.md` Architecture mentions
  `doctor`/`status` and `app_exec`.
- **Ratchet candidate:** weak (docs); prefer ENT-003’s STABILITY test.

### ENT-010: Player and wasm glass are unreleased second products

- **Priority:** P2
- **Dimensions:** Build / release / operations; Architecture topology
- **Status:** observed fact
- **Evidence:**
  - `Makefile` `player` / `player-web` are separate from `build`.
  - `.github/workflows/release.yml` builds `spyder`, `ios`,
    `spyder-killusbmuxd` only; `go test ./...` on tag; no C++.
  - `internal/playerweb/playerweb.go` — wasm served from disk;
    Homebrew packaging is 🎯T102; package has no tests.
  - 1068 tracked `player/vendor/` files + LFS `.a` + four git
    submodules.
- **Mechanism:** `make test` cannot decide player/wire/web behaviour.
  A broken glass still “releases” via the Go tarball. Vendor volume
  dominates `git ls-files` (1475 tracked files).
- **Blast radius:** stream glass users; clone/CI time; LFS hook
  coupling (`core.hooksPath` warning in global AGENTS.md applies if
  hooks are redirected).
- **Counterevidence checked:** vendor-in-tree matches house `cpp.md`
  (no Homebrew C++ links). Protocol-only coupling with ge is
  deliberate. Dev-only player is an accepted product choice.
- **Smallest coherent remediation:** add a `player` suite to
  `scripts/test-report.sh` (`check-wire` at minimum); leave tarball
  scope until 🎯T102 is chosen.
- **Verification:** `TEST-REPORT.json` grows a `player-wire` suite.
- **Ratchet candidate:** `make_target` for `check-wire`.

### ENT-011: Deadline table still names removed tools

- **Priority:** P3
- **Dimensions:** Local code quality; Change amplification
- **Status:** observed fact
- **Evidence:** `internal/mcp/dispatch_deadline.go:49-59` includes
  `runs_artefacts`, `sim_erase`, `app_channel_start`, `log_stream`,
  `health`. `TestAppChannel_StartRemoved`
  (`internal/mcp/app_channel_test.go:702-708`) asserts
  `app_channel_start` is an unknown tool. None of those names are in
  `toolHandlers()`.
- **Mechanism:** Dead cases are harmless today (`default` is also
  `DeadlineDeviceOp` for most). Re-adding a name with a different
  class, or assuming this list is the verb inventory, will be wrong.
- **Blast radius:** dispatch timeouts for any revived name.
- **Counterevidence checked:** `app_exec`/`run_script` class is
  correct and comments cite the 15 s FastRead bug.
- **Smallest coherent remediation:** delete dead names; test subset.
- **Verification:** see ENT-005 ratchet.
- **Ratchet candidate:** `TestDeadlineNamesSubsetOfHandlers`.

### ENT-012: go-ios is a replace-pinned fork with a documented GC incident

- **Priority:** P3
- **Dimensions:** Security / dependencies; Build / release / operations
- **Status:** observed fact (pin + incident write-up)
- **Evidence:** `go.mod:64-94` replace
  `github.com/marcelocantos/go-ios v1.0.214-0.20260524022121-d78573d186a3`;
  comment records SHA `15d4eb19` being GC’d after rebase; mitigation is
  `spyder-pin-v*` tags. `govulncheck` is not installed. Indirect deps
  include `golang.org/x/crypto v0.48.0`. No Dependabot/renovate config
  in-repo.
- **Mechanism:** Historical spyder tags can stop building if a pin tag
  is missing; supply-chain review is manual.
- **Blast radius:** `go build` of old spyder; all iOS device ops.
- **Counterevidence checked:** process to tag before bumping is written
  in `go.mod` itself. `-mod=mod` for `bin/ios` is documented in the
  Makefile.
- **Smallest coherent remediation:** keep the tag process; add
  govulncheck as a laptop `test-report` suite when the binary is
  present — do not add a dependency from this audit.
- **Verification:** `govulncheck ./...` when available.
- **Ratchet candidate:** hygiene `scanner` item later.

## Redundancy and competing sources of truth

| Fact | Authorities | Drift? |
|---|---|---|
| Verb set | `toolHandlers()` (runtime), `legacyDefinitions()` (schema, tested), CLI list (subset, untested), `STABILITY.md` MCP table (stale), `agents-guide.md` | Yes — STABILITY/MCP |
| `app_exec` duration cap | `maxExecDuration`, MCP description, MCP param desc, agents-guide | **Yes (ENT-001)** |
| Durable recipes | embed `recipes/`, repo `scripts/lib/`, user `~/.spyder/scripts/` | Mirror unenforced (ENT-004); user layer deliberate |
| Device health | `health.Model`, `wedge.DoctorFinding`, `spyder doctor` CLI | Bridged on purpose (T99.6); doctor CLI can run without daemon |
| Stream protocol | ge `Protocol.h`, spyder `player/include/wire/Protocol.h` | Copied; local sizeof oracle only (ENT-006) |
| Screenshot default | T114 path in code + agents-guide; STABILITY “MCP image block” | Yes |
| Inventory | `~/.spyder/inventory.json`; USB ceiling `~/.spyder/usb-speed.json` (ratchet-up, not written back to inventory) | Deliberate split (`agents-guide.md`) |
| Daemon listen address | `defaultAddr` loopback; `SPYDER_ADDR`; brew `:3030`; `SPYDER_DAEMON_URL` for CLI | Documented; ops footgun (ENT-008) |

## Healthy structure worth retaining

- **Acyclic internal DAG.** `health` imports nothing internal; `paths`
  is a leaf; `rest` → `mcp` is one-way. Do not introduce `mcp` → `daemon`.
- **One dispatch path.** REST and Starlark cannot diverge on reservation
  or verb behaviour without failing `TestExec_BuiltinCoversEveryAdvertisedVerb`
  and `TestDefinitions_SingleEntryPoint`.
- **`device.Adapter`.** iOS/Android/desktop share one interface; iOS
  fail-closed on Android-only caps (`input_tap`, `perf_fps`) is
  documented in `agents-guide.md`.
- **Privilege split.** `spyder-killusbmuxd` is a tiny binary so sudoers
  does not whitelist the MCP server (`Makefile`, `cmd/spyder-killusbmuxd`).
- **Loopback default + explicit LAN opt-in** in source (`main.go:41-47`),
  even though brew then opts in (ENT-008).
- **Live tests gated** on `SPYDER_LIVE_UDID` / `SPYDER_LIVE_ANDROID_SERIAL`
  / `SPYDER_LIVE_UDIDS`; `test-report.sh` clears those env vars for
  `go-unit`.
- **Wedge finding shared** with doctor and health (`wedge/doctor_bridge.go`)
  instead of a second diagnosis tree.
- **Player vendor policy** matches house C++ rules (no Homebrew `.dylib`
  links; LFS for `.a`).
- **App-channel protocol** is one MessagePack implementation
  (`internal/appchannel/protocol.go`), not a second copy in the player.

## Hygiene posture

**Hygiene posture not declared.** There is no `hygiene.yaml`. The
validator was invoked from the repo root as required:

```
/Users/marcelo/.claude/skills/hygiene/hygiene_check.py
```

Exit 1:

```
FileNotFoundError: [Errno 2] No such file or directory:
'/Users/marcelo/work/github.com/marcelocantos/spyder/hygiene.yaml'
```

No floors, tiers, or drift vector exist to report. This audit did not
initialize `hygiene.yaml`.

Observed controls that a future declaration could point at (not
validated as hygiene items):

| Would-be item | Reality this run |
|---|---|
| `make_target: test` | exists; **failed** on HEAD (ENT-002) |
| `make_target: vet` | exists; passed |
| `make_target: fmt-check` | exists; **failed** (ENT-007) |
| `make_target: test-report` | exists; committed report is 5 days / 1 commit stale |
| `ci_job: release.yml#build` | tag-only; `go test ./...` on darwin-arm64 |
| `file: LICENSE` | Apache-2.0 present |
| secret scan / govulncheck / SBOM | absent |
| per-PR CI | absent by documented design |

Overlap: ENT-002/ENT-007 are entropy findings that would become hygiene
drift the moment those make targets were declared `state: enforced`.

## Oracle coverage and residue

| Property | Decided by | This run |
|---|---|---|
| Internal package DAG / cycles | auxiliary `go list` graph | no cycles |
| Verb ↔ schema parity | `TestExec_BuiltinCoversEveryAdvertisedVerb` | not individually re-asserted; package `internal/mcp` tests passed |
| MCP is `app_exec` only | `TestDefinitions_SingleEntryPoint` | mcp tests passed |
| Hermetic Go unit suite | shipped `go test ./...` | **FAIL** (ENT-002) |
| `gofmt` | shipped `make fmt-check` | **FAIL** (ENT-007) |
| `go vet` | shipped `make vet` | pass |
| Live iOS/Android | `SPYDER_LIVE_*` `_Live` tests + `TEST-REPORT.json` live suite | **not run**; last attestation 2026-08-16 pass |
| Desktop stdout capture | `TestDesktopAdapter_LaunchLifecycle` | red under `./...`, green isolated |
| Player wire layout | `make -C player check-wire` | **not run** |
| Browser player render | none in this repo’s Go tests; `playerweb` has no tests | **not run** (journeys: owner-visible `/player/` and `/dashboard` have no journey harness here) |
| Recipe mirror identity | none | identical by manual `diff` |
| Duration cap consistency | none | drifted (ENT-001) |
| Vulnerability scan | none (`govulncheck` unavailable) | unknown |
| Hygiene floors | undeclared | n/a |

**Owner residue (intent, not mechanical follow-up)**

1. Should `max_duration_ms` be 120 s (protect the agent slot) or 10 min
   (Unity recipes)? Code comments argue 10 min.
2. Should `scripts/lib` remain a documented duplicate of `recipes/`?
3. Stay with player-in-spyder, or split once 🎯T102 exists?
4. Accept unauthenticated `SPYDER_ADDR=:3030` on brew indefinitely
   (🎯T76.4), or require a token off-loopback?
5. Keep laptop `TEST-REPORT.json` as the only merge oracle, or add a
   macOS hosted job that at least runs `go test ./...` + `gofmt`?

Failed/skipped checks are listed in Commands run. Nothing in this
section is “go run the missing command” disguised as intent — the
player-wire and live-device gaps need devices/toolchains the audit
was instructed not to install.

## Sequenced remediation

1. **Repair the shipped oracles (ENT-002, ENT-007).** Fix or
   serialize the desktop LogRange race; `gofmt -w` the eight files;
   regenerate `TEST-REPORT.json` on that commit. Until `make test` and
   `make fmt-check` are green, other ratchets will lie.
2. **Converge duration and MCP catalogue (ENT-001, ENT-003).** One
   constant for `max_duration_ms`; rewrite `STABILITY.md` MCP section
   to `app_exec` + REST/Starlark verbs; fix the `Definitions()`
   comment.
3. **Lock remaining duplicates (ENT-004, ENT-011, ENT-006).** Recipe
   mirror test; deadline-name subset test; `check-wire` in
   `test-report.sh`.
4. **Do not split `internal/mcp` yet (ENT-005).** Generate or test the
   satellite lists (CLI allow-list, deadlines) from `toolHandlers`
   first. A package explosion without a second hub does not reduce
   amplification.
5. **Ops hardening (ENT-008, ENT-010) after oracles are green.**
   Listen-preflight for CLI auto-start; document brew LAN risk in
   formula caveats; player suite in the attestation. Authn is a
   product decision (residue #4).
6. **Hygiene.yaml only after the make targets are true**, so floors
   do not encode today’s red `fmt-check`/`test` as “enforced”.
7. **Re-run this audit** on the same section definitions and compare.

No architectural rewrite is required to close the P1 mechanisms.
)

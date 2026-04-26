# T38: Spyder CLI rough-edges bundle (ge feedback)

**Target:** 🎯T38 — Spyder CLI fills the rough edges surfaced by ge's
first integration: `is_running` primitive, archived-log query on iOS,
selector-aware `resolve`, `run --on PREDICATE`, `run --timeout DURATION`.

**Date:** 2026-04-26
**Status:** Showcase — five acceptance bullets implemented across the
MCP/REST surface and the CLI proxy. This paper records the before/after
patterns ge's `smoke-test.sh` and `matrix-cell.sh` can adopt to drop
their workarounds.

---

## Origin

Surfaced 2026-04-26 by ge's 🎯T33.1 + 🎯T33.2 implementation work. Each
migration succeeded but flagged distinct rough edges where the script
had to fall back to platform-specific tooling or live with a less-
precise contract. None of these blocked ge's adoption — the migrations
shipped with workarounds — but each is a small future-quality
improvement to the spyder CLI surface.

This bundle lands all five at once because they share a release window
(post-v0.18 candidate) and because the test surface for ge's scripts is
cohesive: the value is "ge can drop its `~/Library/Logs/...` scrape, its
`pidof | grep` heuristic, and its two-phase reserve→release→re-acquire
dance", not any individual primitive.

---

## Bullet 1 — `spyder is-running`

**Before** (ge `smoke-test.sh`):

```bash
# Workaround: device_state.foreground_app only sees the foreground app,
# not backgrounded ones. iOS path is also pending per STABILITY.md, so
# we either skip the check or live with a false negative on backgrounded
# apps.
fg=$(spyder device-state Pippa --json | jq -r .foreground_app)
if [[ "$fg" != "$BUNDLE_ID" ]]; then
  echo "WARN: $BUNDLE_ID not foreground (might be backgrounded; can't tell)"
fi
```

**After:**

```bash
spyder is-running Pippa "$BUNDLE_ID"
case $? in
  0)  echo "running" ;;          # also prints `running pid=<n>`
  20) echo "not installed"; install_app ;;
  22) echo "installed but not running"; relaunch ;;
  *)  exit 1 ;;
esac
```

**Why the codes overlap (20/22):** 22 is `ExitLaunchFailed` for `launch_app`
and `ExitAppNotRunning` for `is-running`. Both mean "the app is not
running" — sharing one code keeps script branching simple. See
`STABILITY.md` exit-codes table.

**iOS implementation:** `pmd3 dvt process-id-for-bundle-id` (already
used internally by `autoawake.isKeepAwakeRunning`). Same call path that
the autoawake supervisor relies on for stay-awake convergence.
**Android implementation:** `adb shell pidof <pkg>`. Both adapters fall
through to a `ListApps` cross-check to distinguish `not_running` from
`not_installed`.

---

## Bullet 2 — Archived-log query on iOS

**Before** (ge `smoke-test.sh`):

```bash
# Workaround: bypass spyder. Scan the host crash dir directly because
# spyder log can only see lines from "now" forward.
recent_crashes=$(find ~/Library/Logs/CrashReporter/MobileDevice/Pippa \
  -newer /tmp/last_smoke_run -name '*.ips' 2>/dev/null)
```

**After:** This bullet was discharged via documentation rather than a
runtime change. `pymobiledevice3` does not expose `OsTraceService` or an
equivalent through a stable CLI surface that spyder can consume
reliably; a runtime archived-log mode is not feasible without a meaningful
investment in the bridge layer.

The hard rule for callers, codified in `STABILITY.md` §"iOS log
live-window contract", is:

- A `since` timestamp **older than the moment the live tail subscribes
  to the device** will silently miss lines that occurred before the
  subscription started.
- For crash detection, prefer the `crashes` tool (which reads the
  host-side aggregate crash dir) over `logs`.
- For continuous monitoring, use `log --follow` (live SSE stream).

ge's existing `~/Library/Logs/CrashReporter/...` scan should migrate to
`spyder crashes Pippa --since <ts>` — same data, normalised, and goes
through the daemon's reservation auth so concurrent test cells don't
race on the crash dir. (No script change needed for this bullet beyond
adopting `crashes`.)

This contract may relax post-1.0 if pmd3 stabilises an OsTraceService
binding that spyder can wrap.

---

## Bullet 3 — Selector-aware `spyder resolve`

**Before** (ge `matrix-cell.sh`):

```bash
# Workaround: spyder resolve exits 1 generic for non-alias inputs, so
# we can't tell "this is a selector predicate, try downstream tools"
# from "this alias is genuinely unknown". Always assume alias-unknown.
if ! spyder resolve "$INPUT" >/dev/null 2>&1; then
  echo "ERROR: unknown device $INPUT" >&2
  exit 1
fi
```

**After:**

```bash
spyder resolve "$INPUT"
case $? in
  0)  : ;;                                       # alias resolved
  11) echo "ERROR: unknown alias $INPUT" >&2; exit 1 ;;
  15) echo "INFO: $INPUT is a predicate, falling through to downstream tooling"
      # Optionally re-resolve via selector to get a concrete device:
      spyder resolve --on "$INPUT" --json | jq -r .alias ;;
  *)  exit 1 ;;
esac
```

**Auto-detection:** A positional argument containing `=` is parsed as a
selector predicate without requiring `--on`, so
`spyder resolve platform=ios,os>=17` works directly. The explicit form
`spyder resolve --on platform=ios,os>=17` is also accepted for
generated-script readability.

**Server side:** the MCP `resolve` tool now accepts `{name?, selector?}`
(exactly one). Selector mode resolves against live devices using the
same `selector.Resolve` path as `reserve`, then projects the matched
device back to its inventory entry.

---

## Bullet 4 — `spyder run --on PREDICATE`

**Before** (ge `matrix-cell.sh`):

```bash
# Workaround: two-phase resolve→release→re-acquire dance. Race window
# (<1ms) where another caller could grab the device between the two
# reserves. Logged as 🎯T33.2's known limitation.
ALIAS=$(spyder reserve --on "$PREDICATE" --as ge --ttl 30 --json | jq -r .device)
spyder release "$ALIAS" --as ge
spyder run --device "$ALIAS" -- "$@"
```

**After:**

```bash
spyder run --on "$PREDICATE" -- "$@"
```

**Atomicity:** the daemon's `reserve` handler holds `h.mu` across
selector resolution AND reservation acquisition. There is no window
where another caller can interleave. The CLI marshals the predicate to
selector JSON, POSTs to `/api/v1/reserve`, reads the alias from the
response, and the local-store renewal/release loop continues against
the same device-keyed file-backed record.

**Race-free, not transaction-free:** if the daemon process dies between
acquire and the CLI reading the alias, the reservation will expire on
TTL. This is the same failure mode the existing `--device` path has.

`--device` and `--on` are mutually exclusive (parser-level error).

---

## Bullet 5 — `spyder run --timeout DURATION`

**Before** (ge `matrix-cell.sh`):

```bash
# Workaround: external watchdog. Cell budget is invisible to spyder;
# opportunistic renewal keeps the reservation alive indefinitely so
# we wrap with `timeout` from coreutils.
gtimeout --signal=TERM 5m spyder run --device "$ALIAS" -- "$@"
```

**After:**

```bash
spyder run --on "$PREDICATE" --timeout 5m -- "$@"
```

**Behaviour on deadline:** the wrapped child is signalled via context
cancellation, the reservation is released, and spyder exits 30
(`ExitTimeout`) — distinct from the child's signal-induced exit (which
would be 124 from the gtimeout pattern, or whatever `SIGTERM` produced
in the child). Scripts can branch cleanly:

```bash
spyder run --on platform=ios --timeout 5m -- ./run-cell.sh
case $? in
  0)  echo "PASS" ;;
  30) echo "TIMEOUT — cell exceeded budget" ;;
  *)  echo "FAIL" ;;
esac
```

**Distinct from per-spyder-call `--timeout`:** the universal
`--timeout` flag bounds the daemon HTTP call (e.g.
`spyder devices --timeout 30s` bounds the *list* call). The `run`
subcommand's `--timeout` bounds the **wrapped child invocation** —
they're orthogonal.

---

## Migration impact for ge

ge's two scripts can drop:

- `device_state.foreground_app` heuristics → `is-running`.
- `~/Library/Logs/CrashReporter/...` scan → `spyder crashes`.
- Two-phase reserve→release→re-acquire dance → `spyder run --on`.
- External `gtimeout` watchdog → `spyder run --timeout`.
- Generic exit 1 fallthrough on `spyder resolve` → exit-15 branch.

The migration is opportunistic: ge can adopt these incrementally as
each script is touched. The pre-v0.19 spyder remains compatible (the
new flags and tools are additive; no existing behaviour changed).

---

## Tests

- `internal/mcp/is_running_test.go` — five cases (running / not_running /
  not_installed / list_apps error / missing args).
- `internal/mcp/resolve_test.go` — selector path, name+selector mutual
  exclusion, malformed selector JSON.
- `main_test.go` — `parseRunArgs` covers `--timeout`,
  `--timeout banana`, `--timeout 0s`, `--on PREDICATE`, and `--device`+
  `--on` mutual exclusion.

The `make bullseye` standing invariants (fmt + vet + build + tests +
clean tree + fresh `TEST-REPORT.json`) gate the showcase.

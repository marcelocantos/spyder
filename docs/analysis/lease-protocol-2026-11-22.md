# spyder lease and reservation protocol: analysis, 2026-11-22

Branch `filler/analysis-2026-11-22`, cut from `filler/fix-hunt-findings-3`
(`dcbf544a`, the reviewed fix stack not yet on `master`). "Old" below means
`master` (`cdd631a1`, v0.112.0; its `main.go` and `internal/reservations`
are identical to the stack's merge base `7142264f`, v0.111.0+6). Read-only
analysis: no product code or test changed. Models, scripts, reproductions
and their output live in `docs/analysis/lease-protocol-2026-11-22/`.

Question: can two jobs ever hold the same device, can a lease outlive its
holder forever, can a crashed daemon or client strand a device, and does an
old `spyder run` binary against a new daemon break any of that?

**Short answer.**

- *Two jobs on one device*: yes, by five confirmed routes. Two are new in
  or made worse by this stack: an old `spyder run` still alive after the
  upgrade erases leases the new code grants (R1), and a group SIGKILL of a
  non-interactive `spyder run` (what `pueue kill` sends) now leaves the
  wrapped job running unleased (R2, a regression of the 🎯T208 fix). The
  other three are a display-name lease keyed by whether the phone happens
  to be connected (R3), a lease that lapses while its holder is suspended
  and is never recovered (R5), and, for simulators, activity renewals
  that never reach the file, so the reaper shuts down a simulator in use
  (R6).
- *A lease outliving its holder forever*: not for device reservations.
  TLC checks that once every holder has finished or died, the book empties
  under every modelled fault (old clients, kills, orphans, sleep, replugs,
  daemon restarts). Two bounded strandings remain: a release by display
  name while the phone is unplugged reports success and leaves the lease
  for its full TTL, up to 24 h (R4); and an orphaned job keeps the device
  busy after its lease lapses (R2). A simulator lease *can* be stranded
  indefinitely: a pending record whose `operation_pid` has been reused by a
  live process is never reaped and cannot be released (R7).
- *Crashed daemon*: harmless for reservations. The daemon keeps no lease
  state in memory and opening the book does not rewrite it; a restart only
  delays agents' renewals (model config `daemon_down`).
- *Old `spyder run` against the new code*: breaks exclusion. The condition
  that opens the hazard is the upgrade, not the daemon restart: R1 uses no
  daemon at all. The deploy rule in `spare-capacity.md` ("restart the
  daemon only when no `spyder run` from the old binary is alive") must
  become "install the new binary only when no `spyder run` is alive".

## 1. What the system is

spyder is one Go binary. `spyder serve` is the daemon (LaunchAgent on
`:3030`, MCP plus REST at `/api/v1/<verb>`); every other subcommand is a
CLI, most of them thin proxies that POST to the daemon. Exclusive use of a
device is recorded in two independent lease books.

**The device reservation book**, `~/.spyder/reservations.json`
(`internal/reservations`). A JSON array of
`{device, owner, expires_at, note, created_at}`; the format is unchanged
since v0.111.0. On this branch the file is the only authority: every
operation (`Acquire`, `Renew`, `Release`, `Authorize`, `Get`, `List`) takes
an exclusive `flock` on `reservations.json.lock`, reloads the file, changes
the map, and if it changed writes a private temp file, fsyncs it and
renames it over the book (`Store.withBook`, `reservations.go:422-459`,
`saveLocked` `:560-619`). Two kinds of process write it:

- the daemon (`daemon.go:333`, `OpenBook`), for `reserve`, `renew`,
  `release`, for the authorisation check on every gated verb
  (`tools.go:944-954`), and for a one-second expiry sweep
  (`notifications.go:79-101`, `List()`);
- `spyder run` (`main.go:418-645`), which opens the book itself. With
  `--device` it acquires, renews every 20 minutes and releases through the
  file; with `--on` it reserves through the daemon and then uses the file
  if the file shows that grant (`main.go:458-473`), otherwise the daemon.

The key of a reservation is computed by a normaliser: the inventory alias
when the ref names an inventoried device, otherwise, for a ref that is not
an iOS identifier or adb transport, the UDID of a connected device whose
name or id matches (a census of usbmux and adb), otherwise the ref
verbatim (`inventory.go:149-198`; daemon `tools.go:644-653`; `spyder run`
caches its key for the whole run, `main.go:439-454`). The owner of a
`spyder run` lease is `<dir>:<pid>@<host>` unless `--as` is given
(`main.go:358-371`); MCP clients pass any owner string.

Leases are advisory towards the job. Gated daemon verbs check the book
(`authorize`), but a `spyder run` job drives the device with its own tools
(xcodebuild, adb, Unity), so nothing checks that a job's lease is still
valid while it works. `spyder run` prints renewal errors and carries on
(`main.go:589-597`).

**The simulator lease book**, `~/.spyder/sim-leases.json`
(`internal/simlease`), owned by the daemon: `sim_acquire` picks, creates
or boots an iOS simulator and records what it did; `sim_release` and a
30-second reaper undo exactly that. Records carry `pending` and
`operation_pid` while a create, boot or undo is in progress. Writes take a
`flock`, re-read the file and merge in this process's records, with
tombstones for removed ids (`simlease.go:206-433`). Activity on a leased
simulator renews it in memory and writes at most every 30 s (`Touch`,
`:1082-1110`). Simulator leases do not gate device verbs; see design
finding F2.

The template pool (`internal/pool`, `poolstore`) also records "holds", but
no verb calls `Pool.Acquire` and no `PoolResolver` is wired, so it is not
part of the lease surface today.

**Processes and long-lived tasks relevant here**: the daemon's HTTP
handlers, its 1 s reservation sweep, its 30 s simulator reaper; each
`spyder run` process with its renewal goroutine and its wrapped job, which
since 🎯T208 runs in its own process group when stdin is not a terminal
(`main.go:606-621`).

## 2. Invariants

Status: *holds by argument* (read, no counterexample possible as written),
*holds in the model to bound N* (TLC, bound in §3), *violated (Rn)* with a
reproduction in §4, *unknown*.

| # | Invariant | Maintained by | How it breaks | Status |
|---|---|---|---|---|
| I1 | Two jobs never use one physical device at the same time. | Conflict check in `Store.Acquire` under the flock (`reservations.go:201-252`); renewal every TTL/3 (`main.go:581-600`). | A holder's lease lapses while it still runs (suspension, failed renewals: R5); the wrapper dies and its job does not (R2); two keys for one device (R3, old normaliser); an old client overwrites the book (R1); an agent cannot renew while the daemon is down. | Holds in the model with new clients and agents and no faults (2 and 3 jobs). Violated: R1, R2, R3, R5; model configs `old_erase`, `old_name`, `orphan`, `flap`, `sleep`, `daemon_down`. |
| I2 | The book never holds two live leases with different owners on one physical device. | One canonical key per device (`ReservationKey`) and `existingKeyLocked` (`reservations.go:351-359`). | The key of a display name depends on whether the census lists the phone at that moment (R3); master keys display names verbatim while this branch keys them by UDID. | Violated (R3; `flap`, `old_name`). |
| I3 | A job that is using a device has a live lease on it. | Same as I1. | Same as I1. | Holds in the model without faults; violated with them (same configs). |
| I4 | After a release that reports success, the releaser holds nothing. | `Store.Release` (`reservations.go:257-282`). | A daemon release by display name while the phone is unplugged keys to the literal name, finds nothing, returns nil (R4). | Violated (R4; `flap_agent`). |
| I5 | Book transitions among new-code writers are serialised and crash-atomic: each operation sees the previous one's result, and a crash leaves the old book or the new one. | flock + reload + private temp + fsync + rename + directory fsync (`reservations.go:422-459`, `:560-619`). | Only a writer that skips the flock: master `spyder run` (shared `reservations.json.tmp`, no lock). | Holds by argument for new writers (and existing tests `TestSharedBook_*`, `TestCrashBeforeRenameLeavesPreviousBook`); violated with an old writer (R1). |
| I6 | A daemon restart loses no lease and grants nothing new by itself. | The daemon keeps no reservation state in memory; `OpenBook` reads, quarantines only a corrupt file, never rewrites a good one (`reservations.go:140-157`). | A corrupt book is moved aside and the daemon starts empty, dropping every live lease. Only an old writer's shared temp file produces a torn book (DH-13; R8 tore it in three of five trials). | Holds by argument and in the model (`DaemonStop`/`DaemonStart`) for new writers; the corrupt-book branch is F5. |
| L1 | A lease never outlives its holder forever: once every holder has finished or died and no orphaned job runs, the book becomes empty. | Expiry; only the owner renews, and nothing renews on behalf of a dead owner. | It would need a renewer that is not the holder. Old clients rewrite others' entries but never extend them. | Holds in the model under every fault (2 jobs, TTL 2; configs `live_old_new`, `live_new_agent`). Bounded strandings: up to 24 h after a "successful" release (R4), and devices used by an orphan after its lease lapsed (R2). |
| L2 | Every job eventually finishes or dies (no lock starvation or deadlock in the book protocol), given a daemon that eventually stays up. | Short critical sections; flock released on close or death. | A daemon that dies on every request forever. | Holds in the model (`live_*_end`). |
| K1 | Every process maps every name of one device to one key, for the life of a lease. | `inventory.ReservationKey` + census, shared by daemon and `spyder run` (DH-09 fix). | Uninventoried device whose census misses (unplugged, rebooting, a census over its 4 s budget) (R3); the daemon recomputes the key per call (R4); case or dash variants of an uninventoried iOS UDID are not folded (§6). | Violated (R3, R4). |
| K2 | A `spyder run` uses one key for acquire, renewals and release. | Per-run key cache (`main.go:439-454`). | — | Holds by argument. |
| O1 | Two jobs never share an owner unless they ask to. | `deriveOwner` includes the pid (`main.go:358-371`). | `--as` shares on purpose; master derived the owner from the directory name only (DH-10), so two old runs from one checkout still share. | Holds by argument for new runs. |
| S1 | A simulator lease renewed by activity is not reaped while the activity continues. | `Manager.Touch` (`simlease.go:1082-1110`); documented in the `sim_acquire` tool text. | The in-memory renewal is written only when 30 s have passed since *any* write, and any reload replaces it with the file's older expiry (R6). | Violated (R6). |
| S2 | A pending simulator lease whose operation died is reaped. | `Reap` → `reapable` → `operationActive` (`kill(pid, 0)`, `simlease.go:1126-1170`). | The PID has been reused by a live process (R7). | Violated (R7). |
| S3 | Two simulator managers over one file never lease one simulator twice and never resurrect a removed record. | `beginBook` before every decision, merge with tombstones (`simlease.go:206-433`). | — | Holds by argument; existing tests (`TestTwoManagersOverOneFile`, `TestReaperKeepsLeaseRenewedByPeer`). Not modelled. |
| S4 | The simulator reaper never undoes a simulator that another live lease holds. | `matchUndoTarget` / `protectedUDIDs` (`simlease.go:987-1052`). | — | Holds by argument; `TestReaperNeverDeletesSimulatorHeldByLiveLease`. |

## 3. Model and results

`tla/Leases.tla` models the reservation book at this branch, with
`tla/MC.tla` supplying job configurations and one `.cfg` per experiment.
`tla/run-all.sh` runs them all (TLC 2.19, two workers); raw output is in
`out/tlc-*.out`, condensed counterexamples in `out/trace-*.txt`, the summary
in `out/run-all.txt`.

**What is modelled.** The file as a map from three keys (`U1` and `N1`, the
UDID and display name of device 1; `U2`, a second device) to
`{owner, rem}`, where `rem` is ticks to expiry. Three client kinds:

- `new`: `spyder run` at this branch. Every operation is two steps, lock
  and reload, then write and unlock, so an unlocked rename can land in
  between, as it can in the code. The key is computed once at acquire.
- `old`: `spyder run` at master. One step loads the book (dropping expired
  entries) and acquires from that copy, writing the whole copy; renewal and
  release change the copy and write all of it, without the lock. Load and
  acquire are merged into one step, which is the old client's best case.
- `agent`: an MCP client of the daemon; each call is keyed afresh and needs
  the daemon up.

Also the daemon's expiry sweep (two steps, under the lock), daemon stop and
start (in-flight agent calls fail; no state is lost because there is none),
time (`Tick` may not pass while an awake holder is due to renew; `Sleep`
may), SIGKILL of a wrapper with or without its job surviving, an orphaned
job ending, and device 1 dropping off USB and returning. A constant
`FixKeying` switches on the fix proposed for D4 and D5: a display name the
census cannot resolve is refused, and the daemon renews and releases under
the key it recorded at acquire.

**What is not modelled**: the old client's shared `.tmp` file (torn
writes), inventory edits, clock steps, the simulator book (R6 and R7 are
confirmed with Go tests instead), and gated daemon verbs (the model's
"using a device" is the job's own work, which is what `spyder run` jobs
do).

**Bounds.** TTL 2 ticks and renewal before age 1 (the code's 60 min and
20 min), two jobs; `big_nofault` uses three jobs, TTL 3, renewal before age
2. Every run finishes in under 20 s. No symmetry reduction (jobs differ by
kind and ref).

| Config | Jobs (kind/ref) | Faults on | Checked | Result |
|---|---|---|---|---|
| `base` | new/U1, new/U1 | kill (job dies too) | I1–I4, L1 | holds, 485 states |
| `base_agent` | new/U1, agent/U1 | kill | I1–I4, L1 | holds, 485 states |
| `big_nofault` | new, new, agent, all U1; TTL 3 | kill | I1–I4, L1 | holds, 18,940 states |
| `live_old_new` | old/U1, new/U1 | all | L1 | holds, 14,912 states |
| `live_old_new_end` | same | all | L2 | holds |
| `live_new_agent` | new/N1, agent/N1 | all | L1 | holds, 178,184 states |
| `live_new_agent_end` | same | all | L2 | holds |
| `orphan` | new/U1, new/U1 | kill, job survives | I1 | violated, 8 states |
| `sleep` | new/U1, new/U1 | sleep | I1 | violated, 7 states |
| `flap` | new/N1, new/U1 | replug | I1 | violated, 6 states |
| `flap_agent` | agent/N1, agent/U1 | replug | I4 | violated, 7 states |
| `old_erase` | old/U1, new/U2, new/U2 | none | I1 | violated, 7 states |
| `old_name` | old/N1, new/U1 | none | I1 | violated, 4 states |
| `daemon_down` | new/U1, agent/U1 | daemon down | I1 | violated, 9 states |
| `fix_flap` | new/N1, new/U1, `FixKeying` | replug | I1, I2, I4, L1 | holds, 562 states |
| `fix_flap_agent` | agent/N1, agent/U1, `FixKeying` | replug | I1, I2, I4, L1 | holds, 562 states |
| `fix_live_new_agent` | new/N1, agent/N1, `FixKeying` | all | I2, I4, L1 | holds, 19,290 states |

Vacuity checks: L1 fails when expiry is removed from the model
(`Dec(e) == e`), and L2 holds only with strong fairness on job steps (with
weak fairness a daemon sweep that takes the lock infinitely often starves a
release, which the real flock does not do).

### Counterexamples mapped to code

`old_erase` (`out/trace-old_erase.txt`), the R1 mechanism:

1. `OldAcquire(a)`: old run A loads the book (empty) and writes
   `{U1: A}` (master `reservations.go:83-99` load, `:129-178` acquire,
   `:341-365` save to `reservations.json.tmp` and rename).
2. `AcqLock(b)`, `AcqWrite(b)`: new run B acquires device 2 under the flock
   (`reservations.go:201-252`); book `{U1: A, U2: B}`.
3. `OldRenew(a)`: A's 20-minute renewal (master `main.go:472-483`) renews
   its copy, which never contained B, and renames the whole copy over the
   book: `{U1: A}`. B's lease is gone; B is still running.
4. `AcqLock(c)`, `AcqWrite(c)`: new run C asks for device 2, finds it
   free, and starts. B and C both run on device 2.

The same erasure happens at A's release (R1 below shows it), and every 20
minutes for as long as A runs.

`old_name`: A (master) keys `--device "Lab iPhone"` verbatim (master
`main.go:376-381`, alias-only normaliser), B (this branch) keys the UDID
(`main.go:441-454`); `existingKeyLocked` looks for B's ref under its own
spelling only, so both are granted.

`flap`: B acquires by UDID while the phone is connected; the phone drops
off USB; A's `spyder run --device "Lab iPhone"` lists devices, misses it,
keys `N1` (`inventory.go:180-197` falls through to `return ref`) and is
granted.

`flap_agent`: agent A reserves "Lab iPhone" while connected (key `U1`),
the phone drops off, A's `release` is keyed `N1`, finds nothing and
returns success (`reservations.go:262-266`); `U1` stays A's.

`sleep`: B holds; time passes past B's expiry without B's renewal (a
suspended process, or a laptop asleep: `time.Ticker` runs on the monotonic
clock while `expires_at` is wall-clock); A acquires the expired slot. B's
next renewal fails with a Conflict, which `spyder run` prints
(`main.go:595-597`), and B's job continues.

`orphan`: A is SIGKILLed; its job survives (own process group); A's lease
expires; B is granted while A's job runs.

`daemon_down`: agent B's renewals fail while the daemon is down; the lease
lapses; new run A takes the device through the file. Expected for a lease
design; launchd restarts the daemon in seconds, so it needs an outage of
about 40 minutes and is not listed as a defect.

## 4. Confirmed defects

Each title is the desired state. Reproductions are in `repro/` (shell,
real binaries built from master and from this branch in a scratch
directory, `SPYDER_DATA_DIR` and `HOME` in a temp directory, device refs
shaped as iOS UDIDs so that no census runs) and `_gorepro/repro_test.go`
(Go, in-memory simctl, temp state; run with
`go test -count=1 -v ./docs/analysis/lease-protocol-2026-11-22/_gorepro/`;
`go test ./...` skips the directory). Each Go test passes while the defect
is present. Output: `out/`.

### D1. No `spyder run` from before the upgrade can erase a lease granted after it — critical (deploy)

*Severity*: critical for the deploy of this stack. Every `spyder run`
started before the binary is replaced keeps its copy of the book and
renames it over the file at every renewal (each 20 minutes) and at exit,
erasing every lease granted since it started, with no error anywhere. Lab
runs last hours.

*Mechanism*: master `reservations.go:83-99` loads once; `:129-240`
decide from that copy; `:341-365` write the whole copy through
`reservations.json.tmp` without the flock this branch relies on. This
branch keeps no copy in memory (the integrator's note is right that master
daemon's copy at least re-wrote its own grants), so an erased lease stays
erased. The hazard opens when `brew upgrade spyder` replaces the binary:
from then on new `spyder run` invocations are new-code writers, whether or
not the daemon has restarted. The daemon is not needed at all.

A second compatibility break of the same kind: master keys a display name
verbatim and this branch keys it by UDID, so an old run and a new run (or
the new daemon) given different names for one uninventoried phone are
both granted (`old_name`).

*Reproduction*: `repro/r1-old-run-erases-lease.sh <spyder-old> <spyder-new>`
(`out/r1-old-run-erases-lease.out`). Old run A holds X; new run B acquires
Y; A exits; the book is `[]` while B runs; new run C is granted Y and runs
("job C is running on Y", exit 0) while B is still running. Model:
`old_erase`, `old_name`.

Master writers also damage the book among themselves, and the new code
turns that damage into a wholesale loss. `repro/r8-old-writers-race.sh`
(`out/r8-old-writers-race.out`) runs 200 master `spyder run … -- true`,
each on its own device: with two at a time, 20 leases are still live
after every run has released (stale copies wrote them back; each blocks
its device for an hour); with four or sixteen at a time the shared
`reservations.json.tmp` produced a book that does not parse in three of
five trials, and later master runs refused to start. A new-code process
that opens such a book moves it aside and starts empty (F5), freeing
every device at once. The branch binary under the same load leaves 0
entries and a parseable book, so the stack fixes this once no master
writer is left.

*Smallest fix*: operational, before any code: install the new binary only
when no `spyder run` is running (`pgrep -fl 'spyder run'` empty and
`pueue status --group lab` idle), not merely restart the daemon then.
Code that would close it for good, if a mixed fleet must be tolerated: an
old writer can be detected by the presence of `reservations.json.tmp` or a
book whose mtime changed without the lock being taken; neither is
reliable, so prefer the drain.

### D2. A group SIGKILL of a non-interactive `spyder run` takes the wrapped job with it — high (regression)

*Severity*: high. `pueue kill` of a lab task, a launchd job stop, or any
group kill now kills the wrapper and leaves the job running in its own
group, with no release and no renewal; the lease lapses within the hour
and the device is granted to the next job while the orphan still drives
it. On master the job was in the wrapper's group and died with it. This
is the 🎯T208 change (`main.go:606-621`) and is not among the residuals
recorded on 🎯T208 (terminal case, normal-exit background workers).

*Mechanism*: `child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}`
when stdin is not a terminal; `child.Cancel` kills that group, but only
if the wrapper lives to run it. pueue starts every task as a process-group
leader (observed: `pgid == pid` for every task under `pueued`), so its
kill reaches the wrapper's group and not the job's.

*Reproduction*: `repro/r2-group-kill-orphans-job.sh <binary>`
(`out/r2-group-kill.out`): the wrapper is started as a group leader, its
group is sent SIGKILL. With this branch: "wrapper alive=no job alive=yes";
with the expiry rewritten into the past to stand for the hour, a second
run is granted the device while the first job is alive. With master:
"wrapper alive=no job alive=no". `pueue kill` does signal the task's whole
group: pueue task 1622 (`repro/pueue-kill-group-task.sh`, a shell with a
background `sleep` in its group, default group) was killed with
`pueue kill 1622`, and both the shell and the `sleep` were gone two
seconds later (`out/pueue-kill-group.out`: shell pgid 65058, sleep pgid
65058). A job that has its own group, as the branch gives it, is outside
that kill.

*Smallest fix*: give the job's group a lifeline. Start the job through a
tiny guard (spyder re-executed in a hidden mode) that is the leader of the
job's new group, holds the read end of a pipe whose write end only the
wrapper holds, forks the job, and on EOF sends SIGKILL to its own group.
The wrapper's death, by any signal, then ends the job. About 40 lines;
the existing `TestRunTimeoutDoesNotOrphanWrappedJob` keeps passing, and a
new test kills the wrapper's group and asserts the worker is gone.

### D3. Activity on a leased simulator keeps its lease, as the tool text promises — high

*Severity*: high. With two or more busy simulator leases, the one whose
calls come second in each 30 s window never has its renewal written; at
the file's expiry the reaper shuts its simulator down (or deletes it, for
a lease that created it) while it is in use.

*Mechanism*: `Touch` (`simlease.go:1082-1110`) moves `ExpiresAt` in
memory on every call and writes only when `now - lastPersist >= 30s`,
where `lastPersist` is shared by all leases (`:166`). Every write first
reloads the book (`beginBook` → `adoptDiskLocked`, `:284-317`), which
replaces each lease's in-memory record with the file's, so a renewal that
was not written is lost at the next write by anyone, and at every 30 s
reap (`Reap` calls `beginBook`, `:1126-1135`).

*Reproduction*: `TestR6_ActivityRenewalLostAndReaperShutsDownBusySimulator`
(`out/gorepro.out`): two leases with 5-minute TTLs, both touched every
second for ten minutes, B first; "reaper released … (SIM-A, owner job-A),
undo=shut_down, while it was in use" at t=+300 s.

*Smallest fix*: decide per lease. Keep the time each lease was last
written (or compare the in-memory expiry with the file's after
`beginBook`) and write the touched lease when its written expiry is less
than half a TTL away. Add the reproduction as a test.

### D4. Every name of an uninventoried phone keys one lease, whether or not it is connected — medium

*Severity*: medium. Two jobs can hold one phone when one names it by
display name and the census does not list it at that moment: a replug, a
reboot after a crash, a phone that is slow to reappear, or a census that
misses its 4 s budget under load. Inventoried devices are safe (keyed by
alias); this affects phones not yet in `inventory.json`.

*Mechanism*: `inventory.ReservationKey` (`inventory.go:174-198`) falls back
to the ref verbatim when the census has no match; `spyder run`
(`main.go:441-454`) and the daemon (`tools.go:644-653`, which also returns
the ref when `resolveUninventoried` fails) both do this. The lease then
sits under `"Lab iPhone"` while the other job's is under the UDID, and
`existingKeyLocked` checks only those two spellings of the caller's own
ref.

*Reproduction*: `TestR3_DisplayNameKeyDependsOnPresence`: job A acquires
"Lab iPhone" with the phone listed (key = UDID), job B acquires it with
the phone unlisted (key = "Lab iPhone"); two live leases, two owners, one
phone. Model: `flap`.

*Smallest fix*: fail closed. When a ref needs a census and the census
does not resolve it, refuse to reserve it ("not connected; name it by
UDID or inventory alias") instead of keying the literal, and have the
daemon renew and release under the key it recorded at acquire, as
`spyder run` already does. One branch in `ReservationKey`'s two callers,
a key lookup by owner in `renew`/`release`, plus the tests. Checked in the
model: `fix_flap`, `fix_flap_agent` and `fix_live_new_agent` hold. It does
not help a master `spyder run`, which keys names verbatim (D1).

### D5. A release that reports success has released the lease — medium

*Severity*: medium. An MCP agent that reserved a phone by display name
and releases it while the phone is unplugged is told "released
reservation on Lab iPhone"; the lease stays until it expires, which for an
MCP reserve can be 24 h (`reservations.MaxTTL`). The renewal in the same
state fails with "no active reservation", which the agent may read as
"already free".

*Mechanism*: the daemon keys each call afresh (`handleRelease`,
`tools.go:1188-1243` → `Store.Release` → `canonical`); with the phone
absent the key is the literal name, `Release` finds nothing and returns
nil (`reservations.go:262-266`).

*Reproduction*: `TestR4_ReleaseWhileAbsentReportsSuccessAndStrands`:
reserve with 24 h while connected, unplug, renew fails, release returns
nil, replug, another agent is refused for the next 24 h. Model:
`flap_agent`.

*Smallest fix*: the D4 fix covers it (the release is refused, not
silently empty). Independently, `Release` could report "no reservation
held by <owner> under <key>" instead of nil when nothing was removed, so
callers can tell.

### D6. A `spyder run` whose lease lapsed either gets it back or stops — medium

*Severity*: medium. A holder that misses its renewal window (laptop
asleep for more than about 40 minutes mid-job, the process stopped, two
failed renewals in a row) loses the lease for good while its job keeps
running; the daemon's sweep frees the device within a second of wake and
the next job is granted it. Lab jobs are serialised by the `lab` pueue
group, so the exposure is mainly MCP agents and runs outside pueue.

*Mechanism*: `Renew` refuses an expired record ("no active reservation",
`reservations.go:300-303`) and a record taken by someone else (Conflict);
the renewal loop prints either and continues (`main.go:589-597`). Nothing
re-acquires and nothing stops the job. The ticker runs every 20 minutes
on the monotonic clock while `expires_at` is wall-clock, so after a sleep
the first renewal can come up to 20 awake minutes late. On macOS Go's
monotonic clock is `mach_absolute_time` (`runtime/sys_darwin_arm64.s`,
`nanotime_trampoline`), which does not advance during sleep: a lid closed
for more than 40 minutes between two renewals is enough.

*Reproduction*: `TestR5_LapsedLeaseIsNotRecoveredAndIsRegranted`: A
renews at minute 20, the clock jumps 65 minutes, B is granted, A's renewal
then fails with a Conflict naming B. Model: `sleep`.

*Smallest fix*: check every minute and renew when the wall-clock expiry is
within 40 minutes; on "no active reservation", `Acquire` again (same
owner); on a Conflict, cancel the job's context (which kills its group)
and exit with a distinct code. About 20 lines in the renewal goroutine.

### D7. A simulator lease left pending by a dead process is always reaped — medium

*Severity*: medium. A daemon that dies mid-`sim_acquire` leaves a
`pending` record with its PID. The reaper skips it while `kill(pid, 0)`
succeeds; if that PID now belongs to any live process (a reboot, or PID
wrap on a busy build machine; after a reboot the new daemon can even get
its predecessor's PID), the record is never reaped, its owner cannot
release it ("still being acquired"), and the simulator stays out of every
other acquire for as long as that process lives.

*Mechanism*: `operationActive` (`simlease.go:1164-1170`); `reapable`
(`:1159-1162`); `Release` refuses pending records (`:883-886`).

*Reproduction*: `TestR7_PendingLeaseWithReusedPIDIsNeverReaped`: a
pending record whose `operation_pid` is a live, unrelated process
(`os.Getppid()`), 48 h later: not reaped, owner release refused, another
owner's acquire does not get the simulator.

*Smallest fix*: record the operation's start time with the PID and treat
a record older than any create or boot can take (say 30 minutes) as dead;
or have the operating process hold a per-operation `flock` and test it
with `LOCK_NB`. Also treat `operation_pid == os.Getpid()` without an
`inFlight` entry as dead.

## 5. Design findings

**F1. Leases are advisory and nothing fails safe when one is lost.** A
`spyder run` job uses the device directly, so the only defence against a
lapsed lease is the holder noticing. Today it prints and carries on (D6),
and a dead wrapper's job is not stopped (D2). Change: make loss of the
lease a stop condition for `spyder run` (D6 fix) and tie the job's life to
the wrapper's (D2 fix). Cost: about 60 lines and two tests. This turns
every remaining lapse into a failed job instead of two jobs on one device.

**F2. Two lease books for one device, neither aware of the other.**
Simulator leases (`sim-leases.json`) do not gate device verbs: `authorize`
consults only the reservation book (`tools.go:944-954`), and `sim_acquire`
writes nothing there. Any caller can install, launch or terminate on a
simulator someone else leases, and a `reserve` on that simulator's UDID
by a third party then refuses the lease holder's own gated verbs. The two
books also differ in every design choice: whole-file replace versus
merge-with-tombstones, `pending`/`operation_pid` versus none, activity
renewal versus explicit only, 30 min versus 60 min default TTL, PID
liveness versus wall-clock expiry. Change: have `sim_acquire` place a
reservation under the simulator's UDID for the lease owner, released with
the lease; then one gate covers both. Cost: moderate; the reaper must
release the reservation it placed. (By reading; not reproduced.)

**F3. Keying is a function of the environment, not of the lease.** The
reservation key is recomputed from the inventory and the live census
(K1), so the same ref keys differently over time (D4, D5) and between
versions (D1, `old_name`). Change: store the resolved device id in the
lease record and key by it; resolve a ref once at acquire, fail closed
when it cannot be resolved, and match later calls by id. Cost: a field in
the book (old readers ignore unknown fields, so the format stays
compatible) and the keying change.

**F4. A lease has no total lifetime.** A hung job without `--timeout`
keeps its lease for as long as `spyder run` lives, renewed every 20
minutes; `MaxTTL` caps one renewal, not the lease. An MCP `renew` without
`ttl_seconds` shrinks a 24 h lease to 1 h (`reservations.go:287-289`).
Change: report lease age in `reservations` and the dashboard, and let
`renew` keep the lease's own TTL when none is given. Cost: small.

**F5. A corrupt book is moved aside and every live lease with it.** Any
process that opens a corrupt book (the daemon at start, every `spyder
run` at start) quarantines it and starts empty (`reservations.go:140-157`;
`main.go:475-483`), which frees every device at once. On this branch only
an old writer or outside damage can corrupt it, so this is worth a log
line and a dashboard alert rather than more code.

**F6. The renewal ticker and the expiry are on different clocks.**
`time.NewTicker` is monotonic; `expires_at` is wall-clock and compared
with wall-clock `now` (`reservationLive`). Fine while awake; after sleep
or a clock step the two disagree. The D6 fix (a frequent wall-clock check)
removes the dependency.

**F7. The tests that cover the shared book exercise only new writers.**
`twoproc_test.go` and `reservation_authority_test.go` run two processes of
the same build. The only mixed-version case that matters (D1) is not
tested, and the compatibility tests check that each version can parse the
other's file, which is necessary but not the hazard. A test that runs the
merge-base `spyder run` beside the new one, as R1 does, would have caught
it; it costs a second build in the test.

## 6. Unconfirmed suspicions

1. **Case and dash variants of an uninventoried iOS UDID key separately.**
   A ref that matches the hardware-UDID or standard-UUID pattern skips the
   census and is used verbatim (`inventory.go:149-156`, `:220-229`), so
   `…667A` and `…667a`, or a device's UDID and its CoreDevice UUID, are
   different keys for one uninventoried phone. A dashless UDID goes
   through the census and is folded when the phone is connected. go-ios
   matches a UDID exactly (`ios/utils.go:128` in the vendored fork,
   `SerialNumber == udid`), so spyder's own verbs fail on a lowercase
   spelling; whether a job's own tools (xcodebuild, devicectl) accept it
   decides whether two jobs could both use the phone. Not reproduced.
2. (Confirmed during the run and moved to D1: master writers tear the
   book and write back released leases, R8.)
3. **A new `spyder run --on` against the master daemon.** If the file shows
   the daemon's grant, the new run renews through the file while the
   master daemon keeps an in-memory copy with the original expiry and
   rewrites it at its next write, shortening the lease in the file. The
   integrator recorded "a new CLI with the old daemon behaves as today";
   today's behaviour already includes DH-05. Not modelled (the model has
   no old daemon) and not reproduced.
4. (Confirmed and moved to D2: `pueue kill` kills the task's process
   group, pueue task 1622.)
5. (Confirmed by reading and moved to D6: Go's monotonic clock on darwin
   is `mach_absolute_time`, which does not advance while the Mac sleeps;
   `runtime/sys_darwin_arm64.s` `nanotime_trampoline` in go1.27.4.)

## 7. Coverage

**Read in full**: `internal/reservations/reservations.go`;
`main.go:270-645` (`spyder run`); `cli.go:673-717` (`reserveViaDaemon`,
`updateDaemonRunLease`); master `internal/reservations/reservations.go`
and master `main.go` `runCmd`/`deriveOwner`; `internal/simlease/simlease.go`;
`internal/mcp/notifications.go:1-104`; the reservation handlers in
`internal/mcp/tools.go:944-1395` and `resolveAdapter`/`resolveUninventoried`
(`:1521-1618`); `internal/inventory/inventory.go:60-265`; `internal/paths`;
`run_timeout_test.go`; T215's diff (`ceffc30a`) and ge's factory
`spawn_instance` handler (`sample/tiltbuggy/src/main.cpp:235-275` at ge
`abab952`), which does pass the per-spawn `app_channel` to the instance as
`SPYDER_APP_CHANNEL`, so the precondition named for deploying 🎯T215
holds for the one factory ge has.

**Skimmed**: `internal/daemon/daemon.go:310-420`; `internal/mcp/dev_policy.go`,
`power_policy.go` (reservation use only); `internal/pool/pool.go`,
`internal/poolstore` (holds are not reachable from any verb); the test
lists of `internal/reservations`, `internal/simlease` and the root package;
`internal/simlease/simlease_test.go` fakes.

**Not reached**: `internal/devpolicy` beyond its reservation hooks;
`internal/runs` (run manifests opened and closed beside leases);
`internal/appchannel` instance leasing beyond T215; Android emulator
leases in `simlease/android.go`; `simlease/inventory.go` and
`provenance.go` (claims and prune); the gated-verb list's completeness;
the app-instance lease (`h.instances.Acquire`). None of the existing test
suites was run; the reproductions ran against the branch tip and master
built in a scratch directory with a private `GOCACHE`. Nothing touched the
installed daemon, `~/.spyder`, a device, a simulator or the adb server.

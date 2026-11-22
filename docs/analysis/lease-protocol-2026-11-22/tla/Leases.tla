------------------------------- MODULE Leases -------------------------------
(***************************************************************************)
(* spyder's device reservation book (internal/reservations), the jobs that *)
(* use it, and the daemon, at filler/fix-hunt-findings-3 (dcbf544a).       *)
(*                                                                         *)
(* The book is ~/.spyder/reservations.json: a map from a key (an alias, a  *)
(* UDID, or a literal display name) to {owner, expiry}. Time is relative:  *)
(* an entry's rem is the number of ticks until it expires; it is live      *)
(* while rem > 0 (reservationLive: now.Before(ExpiresAt)).                 *)
(*                                                                         *)
(* Clients:                                                                *)
(*   "new"   spyder run at this branch: flock, reload, change, rename      *)
(*           (Store.withBook); the key is computed once per run and cached *)
(*           (main.go:441-454).                                            *)
(*   "old"   spyder run at master (v0.112.0): reads the book once at       *)
(*           start, decides from that copy, and renames its whole copy     *)
(*           over the book at acquire, every renewal and release, without  *)
(*           the flock (master reservations.go New/saveLocked).            *)
(*   "agent" an MCP or REST client of the daemon (reserve/renew/release);  *)
(*           the daemon keys every call afresh (Handler.ReservationKey).   *)
(* The daemon sweeps expired entries every second (notifications.go:95)    *)
(* and keeps no lease state in memory, so a restart loses nothing; while   *)
(* it is down, agents cannot renew.                                        *)
(*                                                                         *)
(* A two-step new-style write (lock+read, then write+unlock) lets an old   *)
(* client's unlocked rename land in between, as it can in the code.        *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets

CONSTANTS
    Jobs,           \* job identities (also the owner strings)
    Kind,           \* [Jobs -> {"new","old","agent"}]
    Ref,            \* [Jobs -> Keys]: the name the job gives the device
    TTL,            \* lease length in ticks
    RenewAge,       \* an awake holder renews before its age reaches this
    MaySleep,       \* time may pass without a holder's renewal (sleep, SIGSTOP)
    MayFlap,        \* device 1 may drop off USB and come back
    MayKill,        \* a wrapper may be SIGKILLed
    WorkerSurvives, \* the wrapped job survives its wrapper's death (R2)
    MayDaemonDown   \* the daemon may stop and restart

\* Keys: U1 and N1 are the UDID and the display name of device 1; U2 is
\* a second device's UDID.
Keys == {"U1", "N1", "U2"}
Dev(k) == IF k = "U2" THEN 2 ELSE 1
None == "none"
NoEntry == [owner |-> None, rem |-> 0]
Entry == [owner: Jobs \cup {None}, rem: 0..TTL]
EmptyBook == [k \in Keys |-> NoEntry]

VARIABLES
    book,     \* the file
    pc,       \* job program counters
    key,      \* key each job's lease is under
    snap,     \* a job's copy of the book: old clients always; new clients and the daemon while locked
    dsnap,    \* the daemon sweep's copy while it holds the lock
    age,      \* ticks since a running job last renewed
    worker,   \* the wrapped job's process is alive
    lock,     \* flock holder: a job, "daemon" or None
    present,  \* device 1 is listed by usbmux
    daemonUp

vars == <<book, pc, key, snap, dsnap, age, worker, lock, present, daemonUp>>

Live(e) == e.rem > 0

\* inventory.ReservationKey for an uninventoried device.
KeyFor(j) ==
    IF Ref[j] # "N1" THEN Ref[j]
    ELSE IF Kind[j] = "old" THEN "N1"          \* master: inventory alias only
    ELSE IF present THEN "U1" ELSE "N1"        \* census lists it, or not

\* Store.existingKeyLocked: keep an older spelling of a live record.
ExistingKey(s, ref, k) ==
    IF Live(s[k]) THEN k ELSE IF Live(s[ref]) THEN ref ELSE k

Prune(b) == [k \in Keys |-> IF Live(b[k]) THEN b[k] ELSE NoEntry]

PhysDev(j) == Dev(Ref[j])

TypeOK ==
    /\ book \in [Keys -> Entry]
    /\ pc \in [Jobs -> {"idle", "acqLocked", "running", "renLocked", "exiting",
                        "relLocked", "done", "dead"}]
    /\ key \in [Jobs -> Keys \cup {None}]
    /\ snap \in [Jobs -> [Keys -> Entry]]
    /\ dsnap \in [Keys -> Entry]
    /\ age \in [Jobs -> 0..(TTL + 1)]
    /\ worker \in [Jobs -> BOOLEAN]
    /\ lock \in Jobs \cup {"daemon", None}
    /\ present \in BOOLEAN
    /\ daemonUp \in BOOLEAN

Init ==
    /\ book = EmptyBook
    /\ pc = [j \in Jobs |-> "idle"]
    /\ key = [j \in Jobs |-> None]
    /\ snap = [j \in Jobs |-> EmptyBook]
    /\ dsnap = EmptyBook
    /\ age = [j \in Jobs |-> 0]
    /\ worker = [j \in Jobs |-> FALSE]
    /\ lock = None
    /\ present = TRUE
    /\ daemonUp = TRUE

\* Agents reach the book through the daemon.
CanReach(j) == Kind[j] # "agent" \/ daemonUp

(* ---- new-style transactions: Store.withBook ---------------------------- *)

\* Lock and reload. Acquire (main.go:497, tools.go:1156): new clients key
\* once; an agent's key is computed by the daemon on this call.
AcqLock(j) ==
    /\ pc[j] = "idle" /\ Kind[j] # "old" /\ CanReach(j) /\ lock = None
    /\ lock' = j /\ snap' = [snap EXCEPT ![j] = book]
    /\ key' = [key EXCEPT ![j] = KeyFor(j)]
    /\ pc' = [pc EXCEPT ![j] = "acqLocked"]
    /\ UNCHANGED <<book, dsnap, age, worker, present, daemonUp>>

AcqWrite(j) ==
    /\ pc[j] = "acqLocked"
    /\ LET s == snap[j]
           k == ExistingKey(s, Ref[j], key[j])
       IN IF Live(s[k]) /\ s[k].owner # j
          THEN \* Conflict: spyder run exits 3; an agent gets an error.
               /\ pc' = [pc EXCEPT ![j] = "done"]
               /\ UNCHANGED <<book, key, worker, age>>
          ELSE /\ book' = [s EXCEPT ![k] = [owner |-> j, rem |-> TTL]]
               /\ key' = [key EXCEPT ![j] = k]
               /\ pc' = [pc EXCEPT ![j] = "running"]
               /\ worker' = [worker EXCEPT ![j] = Kind[j] # "agent"]
               /\ age' = [age EXCEPT ![j] = 0]
    /\ lock' = None /\ snap' = [snap EXCEPT ![j] = EmptyBook]
    /\ UNCHANGED <<dsnap, present, daemonUp>>

\* Renewal (main.go:581-600, tools.go:1376). The attempt resets the
\* ticker whatever its outcome.
RenLock(j) ==
    /\ pc[j] = "running" /\ Kind[j] # "old" /\ CanReach(j) /\ lock = None
    /\ lock' = j /\ snap' = [snap EXCEPT ![j] = book]
    /\ key' = IF Kind[j] = "agent" THEN [key EXCEPT ![j] = KeyFor(j)] ELSE key
    /\ pc' = [pc EXCEPT ![j] = "renLocked"]
    /\ UNCHANGED <<book, dsnap, age, worker, present, daemonUp>>

RenWrite(j) ==
    /\ pc[j] = "renLocked"
    /\ LET s == snap[j]
           k == ExistingKey(s, Ref[j], key[j])
       IN IF Live(s[k]) /\ s[k].owner = j
          THEN book' = [s EXCEPT ![k].rem = TTL]
          ELSE UNCHANGED book   \* "no active reservation" or Conflict: printed, job carries on
    /\ age' = [age EXCEPT ![j] = 0]
    /\ pc' = [pc EXCEPT ![j] = "running"]
    /\ lock' = None /\ snap' = [snap EXCEPT ![j] = EmptyBook]
    /\ UNCHANGED <<key, dsnap, worker, present, daemonUp>>

\* An agent whose daemon is down cannot renew; its attempt fails.
AgentRenewFails(j) ==
    /\ pc[j] = "running" /\ Kind[j] = "agent" /\ ~daemonUp
    /\ age' = [age EXCEPT ![j] = 0]
    /\ UNCHANGED <<book, pc, key, snap, dsnap, worker, lock, present, daemonUp>>

\* The wrapped command exits; spyder run's Wait returns (main.go:622). For
\* an agent: it is finished with the device and is about to release.
Finish(j) ==
    /\ pc[j] = "running"
    /\ IF Kind[j] = "agent" THEN TRUE ELSE worker[j]
    /\ worker' = [worker EXCEPT ![j] = FALSE]
    /\ pc' = [pc EXCEPT ![j] = "exiting"]
    /\ UNCHANGED <<book, key, snap, dsnap, age, lock, present, daemonUp>>

RelLock(j) ==
    /\ pc[j] = "exiting" /\ Kind[j] # "old" /\ CanReach(j) /\ lock = None
    /\ lock' = j /\ snap' = [snap EXCEPT ![j] = book]
    /\ key' = IF Kind[j] = "agent" THEN [key EXCEPT ![j] = KeyFor(j)] ELSE key
    /\ pc' = [pc EXCEPT ![j] = "relLocked"]
    /\ UNCHANGED <<book, dsnap, age, worker, present, daemonUp>>

\* Store.Release: absent is a no-op success; expired is deleted by anyone;
\* another live owner is a Conflict.
RelWrite(j) ==
    /\ pc[j] = "relLocked"
    /\ LET s == snap[j]
           k == ExistingKey(s, Ref[j], key[j])
       IN IF s[k].owner = None THEN UNCHANGED book
          ELSE IF ~Live(s[k]) THEN book' = [s EXCEPT ![k] = NoEntry]
          ELSE IF s[k].owner # j THEN UNCHANGED book
          ELSE book' = [s EXCEPT ![k] = NoEntry]
    /\ pc' = [pc EXCEPT ![j] = "done"]
    /\ lock' = None /\ snap' = [snap EXCEPT ![j] = EmptyBook]
    /\ UNCHANGED <<key, dsnap, age, worker, present, daemonUp>>

(* ---- old spyder run: master reservations.go --------------------------- *)

\* New() loads the book, dropping expired entries; Acquire decides from
\* that copy and renames the whole copy over the book. Load and acquire
\* are one step here (they are milliseconds apart), which is the old
\* client's best case.
OldAcquire(j) ==
    /\ pc[j] = "idle" /\ Kind[j] = "old"
    /\ LET s == Prune(book)
           k == KeyFor(j)
       IN /\ key' = [key EXCEPT ![j] = k]
          /\ IF Live(s[k]) /\ s[k].owner # j
             THEN /\ pc' = [pc EXCEPT ![j] = "done"]
                  /\ UNCHANGED <<book, snap, worker, age>>
             ELSE LET s2 == [s EXCEPT ![k] = [owner |-> j, rem |-> TTL]]
                  IN /\ book' = s2 /\ snap' = [snap EXCEPT ![j] = s2]
                     /\ pc' = [pc EXCEPT ![j] = "running"]
                     /\ worker' = [worker EXCEPT ![j] = TRUE]
                     /\ age' = [age EXCEPT ![j] = 0]
    /\ UNCHANGED <<dsnap, lock, present, daemonUp>>

OldRenew(j) ==
    /\ pc[j] = "running" /\ Kind[j] = "old"
    /\ LET s == snap[j] k == key[j]
       IN IF Live(s[k]) /\ s[k].owner = j
          THEN LET s2 == [s EXCEPT ![k].rem = TTL]
               IN book' = s2 /\ snap' = [snap EXCEPT ![j] = s2]
          ELSE UNCHANGED <<book, snap>>
    /\ age' = [age EXCEPT ![j] = 0]
    /\ UNCHANGED <<pc, key, dsnap, worker, lock, present, daemonUp>>

OldRelease(j) ==
    /\ pc[j] = "exiting" /\ Kind[j] = "old"
    /\ LET s == snap[j] k == key[j]
       IN IF s[k].owner = None \/ (Live(s[k]) /\ s[k].owner # j)
          THEN UNCHANGED book
          ELSE book' = [s EXCEPT ![k] = NoEntry]
    /\ pc' = [pc EXCEPT ![j] = "done"]
    /\ snap' = [snap EXCEPT ![j] = EmptyBook]
    /\ UNCHANGED <<key, dsnap, age, worker, lock, present, daemonUp>>

(* ---- failures ----------------------------------------------------------- *)

\* SIGKILL of the wrapper or the agent. Its flock goes with it.
Kill(j) ==
    /\ MayKill
    /\ pc[j] \notin {"idle", "done", "dead"}
    /\ pc' = [pc EXCEPT ![j] = "dead"]
    /\ worker' = [worker EXCEPT ![j] = worker[j] /\ WorkerSurvives]
    /\ lock' = IF lock = j THEN None ELSE lock
    /\ snap' = [snap EXCEPT ![j] = EmptyBook]
    /\ UNCHANGED <<book, key, dsnap, age, present, daemonUp>>

\* An orphaned worker ends by itself eventually.
OrphanExits(j) ==
    /\ pc[j] = "dead" /\ worker[j]
    /\ worker' = [worker EXCEPT ![j] = FALSE]
    /\ UNCHANGED <<book, pc, key, snap, dsnap, age, lock, present, daemonUp>>

Flap ==
    /\ MayFlap
    /\ present' = ~present
    /\ UNCHANGED <<book, pc, key, snap, dsnap, age, worker, lock, daemonUp>>

DaemonStop ==
    /\ MayDaemonDown /\ daemonUp
    /\ daemonUp' = FALSE
    \* An agent request in flight dies with the daemon: the agent sees an
    \* error and retries later from where it was.
    /\ lock' = IF lock = "daemon" \/ (lock \in Jobs /\ Kind[lock] = "agent") THEN None ELSE lock
    /\ pc' = [j \in Jobs |-> IF lock = j /\ Kind[j] = "agent"
                             THEN CASE pc[j] = "acqLocked" -> "idle"
                                    [] pc[j] = "renLocked" -> "running"
                                    [] pc[j] = "relLocked" -> "exiting"
                                    [] OTHER -> pc[j]
                             ELSE pc[j]]
    /\ dsnap' = EmptyBook
    /\ UNCHANGED <<book, key, snap, age, worker, present>>

\* A restart opens the book from disk (daemon.go:333). It keeps nothing
\* else, so the book is unchanged.
DaemonStart ==
    /\ ~daemonUp /\ daemonUp' = TRUE
    /\ UNCHANGED <<book, pc, key, snap, dsnap, age, worker, lock, present>>

(* ---- the daemon's one-second expiry sweep: List() ----------------------- *)

SweepLock ==
    /\ daemonUp /\ lock = None
    /\ \E k \in Keys : book[k].owner # None /\ ~Live(book[k])
    /\ lock' = "daemon" /\ dsnap' = book
    /\ UNCHANGED <<book, pc, key, snap, age, worker, present, daemonUp>>

SweepWrite ==
    /\ lock = "daemon"
    /\ book' = Prune(dsnap)
    /\ lock' = None /\ dsnap' = EmptyBook
    /\ UNCHANGED <<pc, key, snap, age, worker, present, daemonUp>>

(* ---- time --------------------------------------------------------------- *)

Holding(j) == pc[j] \in {"running", "renLocked"}
Dec(e) == [e EXCEPT !.rem = IF e.rem > 0 THEN e.rem - 1 ELSE 0]
Advance ==
    /\ book' = [k \in Keys |-> Dec(book[k])]
    /\ snap' = [j \in Jobs |-> [k \in Keys |-> Dec(snap[j][k])]]
    /\ dsnap' = [k \in Keys |-> Dec(dsnap[k])]
    /\ age' = [j \in Jobs |-> IF Holding(j) /\ age[j] <= TTL THEN age[j] + 1 ELSE age[j]]
    /\ UNCHANGED <<pc, key, worker, lock, present, daemonUp>>

\* Awake: no running holder misses its renewal (the ticker fires at TTL/3).
Tick ==
    /\ \A j \in Jobs : Holding(j) => age[j] < RenewAge
    /\ Advance

\* Asleep or stopped: time passes regardless.
Sleep == MaySleep /\ Advance

JobStep(j) ==
    \/ AcqLock(j) \/ AcqWrite(j) \/ RenLock(j) \/ RenWrite(j)
    \/ AgentRenewFails(j) \/ Finish(j) \/ RelLock(j) \/ RelWrite(j)
    \/ OldAcquire(j) \/ OldRenew(j) \/ OldRelease(j)

Next ==
    \/ \E j \in Jobs : JobStep(j) \/ Kill(j) \/ OrphanExits(j)
    \/ Flap \/ DaemonStop \/ DaemonStart \/ SweepLock \/ SweepWrite
    \/ Tick \/ Sleep

\* Jobs and the daemon make progress; time passes; failures are not forced.
Fairness ==
    /\ \A j \in Jobs : SF_vars(JobStep(j)) /\ SF_vars(Finish(j)) /\ WF_vars(OrphanExits(j))
    /\ WF_vars(DaemonStart) /\ WF_vars(SweepLock \/ SweepWrite) /\ WF_vars(Tick)

Spec == Init /\ [][Next]_vars /\ Fairness

(* ---- properties --------------------------------------------------------- *)

\* A job is using the device: its wrapped process runs, or an agent holds.
Using(j) == IF Kind[j] = "agent" THEN Holding(j) ELSE worker[j]

\* I1. No two jobs use one device at the same time.
JobExclusion ==
    \A i, j \in Jobs : (i # j /\ PhysDev(i) = PhysDev(j)) => ~(Using(i) /\ Using(j))

\* I2. The book never holds two live leases with different owners on one device.
BookExclusion ==
    \A k1, k2 \in Keys :
        (k1 # k2 /\ Dev(k1) = Dev(k2) /\ Live(book[k1]) /\ Live(book[k2]))
            => book[k1].owner = book[k2].owner

\* I3. A job using a device has a live lease on it in the book.
UserIsCovered ==
    \A j \in Jobs : Using(j) =>
        \E k \in Keys : Dev(k) = PhysDev(j) /\ book[k].owner = j /\ Live(book[k])

\* I4. A job that has released holds nothing.
ReleasedHoldsNothing ==
    \A k \in Keys : Live(book[k]) => pc[book[k].owner] # "done"

\* L1. Once every job has finished or died and no orphan runs, the book
\* empties (no lease outlives its holder forever).
AllGone == \A j \in Jobs : pc[j] \in {"done", "dead"} /\ ~worker[j]
BookEmpty == \A k \in Keys : ~Live(book[k])
EventuallyFree == AllGone ~> BookEmpty

\* L2. Every job eventually finishes or dies.
JobsEnd == \A j \in Jobs : <>(pc[j] \in {"done", "dead"})

\* L2 needs a daemon that does not die on every request forever.
JobsEndIfDaemonSettles == <>[]daemonUp => JobsEnd
=============================================================================

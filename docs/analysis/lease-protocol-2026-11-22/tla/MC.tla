---------------------------------- MODULE MC ----------------------------------
\* Job configurations for Leases.tla. Each .cfg names one pair.
EXTENDS Leases, TLC
CONSTANTS a, b, c

\* base: two new spyder runs name device 1 by UDID.
KindNN == [j \in {a, b} |-> "new"]
RefUU  == [j \in {a, b} |-> "U1"]

\* a new run and an MCP agent, both by UDID.
KindNA == (a :> "new" @@ b :> "agent")

\* display name vs UDID, both new.
RefNU  == (a :> "N1" @@ b :> "U1")

\* an agent alone on the display name, and a second agent by UDID.
KindAA == [j \in {a, b} |-> "agent"]

\* old run on device 1, two new runs on device 2 (R1).
KindONN == (a :> "old" @@ b :> "new" @@ c :> "new")
RefUVV  == (a :> "U1" @@ b :> "U2" @@ c :> "U2")

\* old run by display name, new run by UDID.
KindON == (a :> "old" @@ b :> "new")

\* old run and new run on one device by UDID; old and old.
KindOO == [j \in {a, b} |-> "old"]
\* three jobs: old by UDID, new and agent by display name.
KindONA == (a :> "old" @@ b :> "new" @@ c :> "agent")
RefUNN  == (a :> "U1" @@ b :> "N1" @@ c :> "N1")

\* three jobs, no old client, all by UDID.
KindNNA == (a :> "new" @@ b :> "new" @@ c :> "agent")
RefUUU  == [j \in {a, b, c} |-> "U1"]

\* liveness pairs under every fault.
RefNN   == [j \in {a, b} |-> "N1"]
KindAN  == (a :> "new" @@ b :> "agent")

JobsAB == {a, b}
JobsABC == {a, b, c}
=============================================================================

# Defect hunt: spyder, 2026-11-20

A read-only hunt for defects the existing tests do not catch, with a failing reproduction for every defect listed as confirmed. No product code or existing test was changed; this file is the only change on the branch.

## Snapshot and method

- **Code under test:** commit `b253ae46` (`🎯T197 Install an Android app as a unit: APK plus its OBB files`), Go 1.27.4, darwin/arm64.
- **Priorities given:** a device left reserved after a crash or restart, or leased to two jobs at once; the daemon crashing mid-job; a remote call that hangs forever.
- **How the work was split.** I read the reservation and lease model, the dispatch path (deadlines, self-restart, the handler lock), `spyder run`, the run store, the idle/power policy, `ensure_session`, scripting (`app_exec`/`run_script`) and the Verify shell runner. Four parallel readers each took one subsystem:
  - simulator leases and the sim/emulator pool;
  - stream relay and app channel;
  - child-process lifecycle and supervision (tunnel, XCTest agent, runwda, log capture, adb watcher, health, wedge monitor);
  - device adapters and the parsing of device-tool output.
- **How each finding was confirmed.** The readers worked on `877a20ed`, three commits earlier. I copied every reproduction they wrote into this snapshot, read it first to check it could not reach a real device, the adb server, a simulator or the live daemon, and re-ran it here. Every defect below was re-run on `b253ae46`, and the output quoted is from that re-run. Where my run's output differed from the reader's, the report quotes mine.
- **Isolation.** Every reproduction uses fakes only:
  - stub adapters, a fake simctl and a fake pool executor;
  - fake `adb`, `xcrun`, `ios`, `emulator` and `terminal-notifier` scripts in temp directories, put first on `PATH` (the device tests assert that `adb` resolves to the fake);
  - loopback listeners on random ports;
  - `SPYDER_DATA_DIR` and `HOME` pointed at temp directories.

  Nothing touched the installed daemon, `~/.spyder`, a device, a simulator, the adb server or the lab pueue group. After each run, `pgrep` showed no process of ours left alive.
- **Race detector.** All reproductions were run with `-race`. The only data races reported are the two listed as defects (pool instance sharing, desktop pid).

## How to run the reproductions

Each defect's test function is shown inline. The package clause, imports and shared helpers for each test file are in the appendix, so a file is that appendix block plus the test functions that name it. Some tests also use helpers that already exist in the package's own tests (`stubAdapter`, `testInventory`, `fakeSimctl`, `fakeExec`, `fakeADB`, `selfHealHandler`, …).

```sh
GOWORK=off go test -race -count=1 -run 'TestDefectHunt' -v ./<package>/
```

`GOWORK=off` is needed only when the checkout sits under `~/work/github.com/marcelocantos/`, whose `go.work` does not list spyder. Every reproduction fails on `b253ae46`; a fixed defect makes its test pass.

## Severity scale

| Level | Meaning |
|---|---|
| critical | The daemon crashes or exits mid-job, or one device or simulator is given to two jobs, through normal use or one request from any client or peer. |
| high | The same outcomes behind a narrower trigger; a lease leaked or lost; enforcement bypassed; a process left driving a device; an unbounded hang; a security hole. |
| medium | Wrong results, lost evidence, or state that leaks into the next job, with no crash and no double lease. |
| low | Latent, rare, cosmetic, or tests only. |

## Summary

| ID | Desired state | Severity | Main file |
|---|---|---|---|
| DH-01 | No frame from an app-channel peer crashes the daemon | critical | `internal/appchannel/protocol.go` |
| DH-02 | One `app_exec` request never crashes the daemon through unbounded `run_script` recursion | critical | `internal/mcp/app_exec.go` |
| DH-03 | No iOS log frame from a device panics the daemon | critical | `internal/oslog/parser.go` |
| DH-04 | A state-capture `select` filter never panics the daemon | critical | `internal/appchannel/state_capture.go` |
| DH-05 | The daemon never grants a device that `spyder run` holds | critical | `main.go` |
| DH-06 | A slow device call on one device never stalls unrelated verbs or restarts the daemon | critical | `internal/mcp/tools.go` |
| DH-07 | A client that hangs up never causes a daemon self-restart | critical | `internal/mcp/server.go` |
| DH-08 | A failed or interrupted `sim_acquire` never deletes a simulator it did not create | critical | `internal/simlease/simlease.go` |
| DH-09 | A reservation gates every name the resolver accepts for the device | high | `internal/daemon/daemon.go` |
| DH-10 | Two `spyder run` jobs never share one lease because they started in the same directory | high | `main.go` |
| DH-11 | `spyder run --on` releases the daemon's lease when the job ends and keeps it renewed while the job runs | high | `main.go` |
| DH-12 | `spyder run` never releases the lease while the wrapped job is still running | high | `main.go` |
| DH-13 | Concurrent writers never corrupt `reservations.json`, and a corrupt file never turns enforcement off | high | `internal/reservations/reservations.go` |
| DH-14 | The self-restart limit (3 per hour) holds across restarts | high | `internal/health/self_restart.go` |
| DH-15 | No two leases ever hold one simulator, and the reaper never deletes a simulator a live lease holds | high | `internal/simlease/simlease.go` |
| DH-16 | Two simulator lease books over one file never lease one simulator twice or lose a lease | high | `internal/simlease/simlease.go` |
| DH-17 | `app_*` handlers end with their dispatch deadline | high | `internal/mcp/app_channel.go` |
| DH-18 | A call's timeout bounds its socket write | high | `internal/appchannel/session.go` |
| DH-19 | A duplicate response from an app never wedges its session | high | `internal/appchannel/session.go` |
| DH-20 | The instance pool never grants one app instance to two holders | high | `internal/appchannel/factory.go` |
| DH-21 | `SpawnInstance` never adopts an unrelated app's session | high | `internal/appchannel/spawn.go` |
| DH-22 | `app_screenshot` writes only inside the screenshots directory | high (security) | `internal/mcp/app_channel.go` |
| DH-23 | jq filters compare msgpack integers by value | high | `internal/appchannel/jq.go` |
| DH-24 | Selector `os>=` / `os<=` predicates compare against the version strings adapters actually produce | high | `internal/selector/selector.go` |
| DH-25 | Arguments passed to `adb shell` reach the device shell as single words | high | `internal/device/android_settings.go` |
| DH-26 | Android `AppPID` says "app not running" only when the app is not running | high | `internal/device/android.go` |
| DH-27 | Android `State` and `List` calls to adb have a deadline | high | `internal/device/android_fps.go` |
| DH-28 | Releasing a device never stops the next holder's app | medium | `internal/mcp/tools.go` |
| DH-29 | The idle policy never stops an app on a device that is reserved | medium | `internal/mcp/power_policy.go` |
| DH-30 | `ensure_session` never closes another owner's app-channel session | medium | `internal/mcp/ensure_session.go` |
| DH-31 | A released device never keeps the network profile its holder applied | medium | `internal/mcp/tools.go` |
| DH-32 | `reserve` never reports success for a hold it could not persist | medium | `internal/reservations/reservations.go` |
| DH-33 | A `sim_renew` that reports success leaves the lease held | medium | `internal/simlease/simlease.go` |
| DH-34 | `sim_acquire` only returns a lease on a booted simulator | medium | `internal/simlease/simlease.go` |
| DH-35 | A simulator lease that `sim_acquire` reports as granted survives a restart | medium | `internal/simlease/simlease.go` |
| DH-36 | A damaged provenance file never disables simulator leasing | medium | `internal/simlease/provenance.go` |
| DH-37 | The tunnel the daemon has just started is never killed and respawned by `Supervise` | medium | `internal/daemon/daemon.go` |
| DH-38 | A tunnel `Stop` that returns success means the tunnel has exited | medium | `internal/iostunnel/supervisor.go` |
| DH-39 | A failed runwda attempt never leaves its `ios runwda` child running | medium | `internal/device/runwda.go` |
| DH-40 | Replacing a runwda attempt never runs two at once for one device | medium | `internal/device/runwda.go` |
| DH-41 | The supervision tick and `agent_ensure` never launch two runners at once for one device | medium | `internal/agent/agent.go` |
| DH-42 | A child that dies right after every successful start reaches NeedsAttention | medium | `internal/health/supervise.go` |
| DH-43 | A hung notifier never blocks stall escalation or the self-restart | medium | `internal/health/notify.go` |
| DH-44 | One old broken-pipe burst never pins the wedge monitor to "wedged" for the daemon's lifetime | medium | `internal/wedge/monitor.go` |
| DH-45 | The adb server watcher never restarts the server in a tight loop | medium | `internal/adbserver/adbserver.go` |
| DH-46 | A forgotten log capture expires after its TTL even while lines keep arriving | medium | `internal/logcapture/session.go` |
| DH-47 | A state capture stops when its session ends | medium | `internal/appchannel/session.go` |
| DH-48 | Starting a state capture on a closed session returns an error instead of panicking | medium | `internal/appchannel/state_capture.go` |
| DH-49 | Memory tracks bytes received, not the length a peer claims | medium | `internal/appchannel/protocol.go` |
| DH-50 | Drained app logs are never lost when encoding the reply fails | medium | `internal/mcp/app_channel.go` |
| DH-51 | A stopped listener never registers a new session | medium | `internal/appchannel/session.go` |
| DH-52 | `GetOrCreateListener` never returns a listener that is being stopped | medium | `internal/appchannel/session.go` |
| DH-53 | Instances booted by `pool_warm` linger and then shut down | medium | `internal/pool/pool.go` |
| DH-54 | Pool status never waits on a simctl call | medium | `internal/pool/pool.go` |
| DH-55 | An Android pool instance can be shut down, because `AVDBoot` returns its adb serial | medium | `internal/pool/executor.go` |
| DH-56 | Android `LogStream` reports a failed `adb logcat` as an error | medium | `internal/device/android.go` |
| DH-57 | Android logcat crash reports name the crashing package | medium | `internal/device/android.go` |
| DH-58 | App crash text that mentions "device not found" is never read as a disconnect | medium | `internal/device/android.go` |
| DH-59 | The iOS `.ips` header decoder yields a process name and a time | medium | `internal/device/ios.go` |
| DH-60 | Android `DisplaySize` handles a `wm size` override | medium | `internal/device/android_input.go` |
| DH-61 | A 40-hex iOS UDID is treated as a physical iOS device | medium | `internal/device/ios.go` |
| DH-62 | A desktop app's long output line never wedges the app | medium | `internal/device/desktop.go` |
| DH-63 | Relaunching a desktop app never orphans the first process | medium | `internal/device/desktop.go` |
| DH-64 | Every crash report a call pulls is kept in the run archive | medium | `internal/mcp/tools.go` |
| DH-65 | A Verify shell step's evidence always includes its last lines | medium | `internal/verify/exec.go` |
| DH-66 | No instant exists at which two owners both hold a device | low | `internal/reservations/reservations.go` |
| DH-67 | A finished Verify shell step leaves no processes behind | low | `internal/verify/exec.go` |
| DH-68 | Tunnel `Stop` kills the whole process group it created | low | `internal/iostunnel/supervisor.go` |
| DH-69 | A second concurrent agent `Shutdown` waits for the resident sessions to end | low | `internal/device/ios_agent.go` |
| DH-70 | The liveness heartbeat never clears an outstanding stall | low | `internal/daemon/health.go` |
| DH-71 | Each adb server restart never leaks a goroutine for the daemon's lifetime | low | `internal/adbserver/adbserver.go` |
| DH-72 | A released instance keeps its full linger after a re-acquire | low | `internal/appchannel/factory.go` |
| DH-73 | A simulator lease is released exactly once | low | `internal/simlease/simlease.go` |
| DH-74 | A huge `ttl_s` clamps to the 12 h maximum | low | `internal/mcp/sim_lease.go` |
| DH-75 | Pool adoption takes each device's template from that device's own ledger row | low | `internal/pool/pool.go` |
| DH-76 | A failed simulator listing never drops pool ledger holds | low | `internal/pool/pool.go` |
| DH-77 | `Pool.Acquire` never shares its internal instance with the linger goroutine | low | `internal/pool/pool.go` |
| DH-78 | In-memory pool stores are independent of each other | low | `internal/poolstore/poolstore.go` |
| DH-79 | iOS `List` returns one row per device | low | `internal/device/ios.go` |
| DH-80 | The desktop adapter writes a process's pid without a data race | low | `internal/device/desktop.go` |
| DH-81 | The Android 9 `FLAG_SECURE` format is detected | low | `internal/device/android_ui.go` |
| DH-82 | Android logcat crash timestamps are read as device-local time | low | `internal/device/android.go` |
| DH-83 | `logcat -t` is sent in device-local time | low | `internal/device/android.go` |
| DH-84 | Android tombstones are attributed to the process, not the crashing thread | low | `internal/device/android.go` |
| DH-85 | The `adb devices -l` parser keeps serials that contain spaces | low | `internal/device/android.go` |
| DH-86 | The install budget covers line-rate transfer on 12 Mb/s links | low | `internal/installdeadline/deadline.go` |
| DH-87 | `ParseUIAutomatorXML` never panics | low | `internal/device/android_ui.go` |
| DH-88 | An empty device reference matches no inventory entry | low | `internal/inventory/inventory.go` |

## Confirmed defects

### DH-01. No frame from an app-channel peer crashes the daemon

- **Severity: critical.** Any process that can reach an app-channel listener (any local process, and any LAN host for the LAN listeners that bind all interfaces) kills spyder with one frame before the hello handshake. A Go stack overflow is fatal and cannot be recovered.
- **Mechanism.** `ReadFrame` decodes the envelope with msgpack/v5. For `RawMessage` and unknown fields the decoder skips values with `Skip`/`skipSlice`, which recurse once per nesting level with no limit. A 16 MiB frame (within `MaxFrameBytes`) of `0x91` bytes (one-element arrays nested inside each other) exhausts the 1 GB goroutine stack.
- **Where.** `internal/appchannel/protocol.go:139`, reached from `session.go:516` (handshake) and `session.go:410` (read loop).
- **Reproduction** (`internal/appchannel/zz_defecthunt_crash_test.go`; it runs in a child copy of the test binary because the defect kills the process; helpers are in the appendix):

```go
// A peer that can reach an app-channel listener (any local process; any LAN
// host for LAN listeners) sends one well-formed, in-limit frame whose params
// are nested arrays. The handshake must reject it, not take the daemon down.
func TestDefectHunt_NestedMsgpackFrameDoesNotCrashDaemon(t *testing.T) {
	const name = "TestDefectHunt_NestedMsgpackFrameDoesNotCrashDaemon"
	if dhIsChild(name) {
		_, l, addr := dhStart(t)
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		// {"params": [[[[ ... nil ... ]]]]} — 0x91 is a one-element fixarray.
		body := []byte{0x81, 0xa6, 'p', 'a', 'r', 'a', 'm', 's'}
		body = append(body, bytes.Repeat([]byte{0x91}, MaxFrameBytes-len(body)-1)...)
		body = append(body, 0xc0)
		var hdr [4]byte
		binary.LittleEndian.PutUint32(hdr[:], uint32(len(body)))
		if _, err := conn.Write(append(hdr[:], body...)); err != nil {
			t.Fatalf("write frame: %v", err)
		}
		if !dhEventually(60*time.Second, func() bool { return l.Connectivity().HandshakeFailures == 1 }) {
			t.Fatal("handshake neither failed nor crashed within 60s")
		}
		return
	}
	out, err := dhRunChild(t, name)
	if err != nil {
		t.Fatalf("listener process died on a nested-array hello frame (%v):\n%s", err, dhFirstLines(out))
	}
}
```

```
--- FAIL: TestDefectHunt_NestedMsgpackFrameDoesNotCrashDaemon (0.66s)
    listener process died on a nested-array hello frame (exit status 2):
        runtime: goroutine stack exceeds 1000000000-byte limit
        fatal error: stack overflow
```

- **Smallest fix.** Before `msgpack.Unmarshal` in `ReadFrame`, run a small iterative scan of the msgpack type codes that rejects container depth above about 256. Every byte from a peer passes through `ReadFrame`.

### DH-02. One `app_exec` request never crashes the daemon through unbounded `run_script` recursion

- **Severity: critical.** Any MCP or REST client can kill the daemon, and every job's in-flight work with it, in about a second; a Go stack overflow is fatal and cannot be recovered.
- **Mechanism.** `handleAppExecContext` removes `app_exec` from the builtin table ("no nested scripting") but keeps `run_script`, which re-enters `handleAppExecContext` with a new Starlark thread, a fresh step budget and the same verb table. A script that calls `run_script` on itself nests without any depth limit until the goroutine stack limit is hit (`runtime: goroutine stack exceeds …`, exit 2). The test reached 2,000 levels in 0.27 s; with the stack limit lowered to 64 MiB the child process died in under 2 s.
- **Where.** `internal/mcp/app_exec.go:93-95`, `internal/mcp/script_helpers.go:234-254`.
- **Reproduction** (`internal/mcp/zz_defecthunt_test.go`, harness below):

```go
// app_exec drops itself from the builtin table ("no nested scripting")
// but keeps run_script, which re-enters handleAppExecContext with a new
// Starlark thread, fresh step budget and the same verb table. Nothing
// bounds the depth.
func TestDefectHunt_RunScriptRecursesWithoutBound(t *testing.T) {
	h, _ := defectHuntHandler(t, &stubAdapter{})
	script := filepath.Join(t.TempDir(), "self.star")
	src := fmt.Sprintf(`n = int(params.get("n", "0"))
if n < 2000:
    run_script(path=%q, params={"n": str(n + 1)})
else:
    fail("reached depth %%d" %% n)
`, script)
	if err := os.WriteFile(script, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := h.Dispatch(context.Background(), "app_exec", map[string]any{
		"script": fmt.Sprintf(`run_script(path=%q)`, script),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := fmt.Sprint(res.Content)
	if strings.Contains(text, "reached depth 2000") {
		t.Errorf("a client script nested run_script 2000 levels deep; each level is a Go+Starlark stack frame chain, so depth is bounded only by the 1 GB goroutine stack (fatal, not recoverable)")
	}
	t.Logf("%.300s", text)
}

// Same recursion without a stopping condition, in a child process. The
// goroutine stack limit is lowered to 64 MiB to keep the run short; the
// production limit (1 GiB) only changes how deep it gets first. A stack
// overflow is a fatal runtime error: no recover() can catch it, and the
// whole daemon exits.
func TestDefectHunt_RunScriptRecursionKillsProcess(t *testing.T) {
	if os.Getenv("DEFECTHUNT_RECURSE") == "1" {
		debug.SetMaxStack(64 << 20)
		h, _ := defectHuntHandler(t, &stubAdapter{})
		script := filepath.Join(t.TempDir(), "self.star")
		os.WriteFile(script, []byte(fmt.Sprintf(`run_script(path=%q)`+"\n", script)), 0o600)
		h.Dispatch(context.Background(), "app_exec", map[string]any{"script": fmt.Sprintf(`run_script(path=%q)`, script)})
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDefectHunt_RunScriptRecursionKillsProcess$")
	cmd.Env = append(os.Environ(), "DEFECTHUNT_RECURSE=1")
	out, err := cmd.CombinedOutput()
	if err != nil && strings.Contains(string(out), "stack exceeds") {
		t.Errorf("one app_exec request crashed the process: %v; %s", err,
			out[strings.Index(string(out), "runtime: goroutine stack exceeds"):][:80])
	}
}
```

```
--- FAIL: TestDefectHunt_RunScriptRecursesWithoutBound (0.27s)
    a client script nested run_script 2000 levels deep; …
--- FAIL: TestDefectHunt_RunScriptRecursionKillsProcess (1.94s)
    one app_exec request crashed the process: exit status 2; runtime: goroutine stack exceeds 67108864-byte limit
```

- **Smallest fix.** Carry a nesting depth in the context (`context.WithValue`) and have `handleRunScriptContext` refuse beyond a small limit (for example 4), or drop `run_script` from the builtin table inside scripts as `app_exec` already is.

### DH-03. No iOS log frame from a device panics the daemon

- **Severity: critical.** One malformed or unexpected frame in an iOS log stream (any `logs`, `log_capture` or `LogRange` on an iOS 17+ device) kills the daemon.
- **Mechanism.** A long-form copy (`0x65FF`) computes `ref := leUint64(top) - 1`. When the top of the stack is 0 or a sentinel, that wraps to `MaxUint64`; the bounds check is done on `int(ref)` (−1) and passes, and `d.stack[ref]` panics. `Decode` runs in `Stream.pump`, a goroutine started with `go s.pump(ctx)` and no `recover`.
- **Where.** `internal/oslog/parser.go:166-171`; `internal/oslog/stream.go:116`.
- **Reproduction** (`internal/oslog/zz_defecthunt_decoder_test.go`):

```go
// A long-form copy (0x65FF) takes its 1-based back-reference from the top
// of the stack. A reference of 0, or a sentinel/tuple in that slot,
// decodes to 0; 0-1 wraps to MaxUint64 and the bounds check (done on
// int(ref) == -1) passes, so d.stack[ref] panics. The frame comes from
// the device over DTX.
func TestDefectHunt_OSLogCopyLongFormZeroRefDoesNotPanic(t *testing.T) {
	const (
		sentinel = uint16(cmdSentinel) << 8   // 0x6800
		copyLong = uint16(cmdCopy)<<8 | 0xFF  // 0x65FF
		zeroImm  = uint16(0xC000)             // one-word immediate, value 0
		tableRst = uint16(cmdTableReset) << 8 // 0x6400
	)
	cases := map[string][]byte{
		"sentinel then long copy":       dhWords(sentinel, copyLong),
		"zero immediate then long copy": dhWords(sentinel, zeroImm, copyLong),
		"after reset":                   dhWords(tableRst, sentinel, copyLong),
	}
	for name, frame := range cases {
		t.Run(name, func(t *testing.T) { dhDecode(t, fmt.Sprint(name), frame) })
	}
}
```

```
--- FAIL: TestDefectHunt_OSLogCopyLongFormZeroRefDoesNotPanic (0.00s)
    sentinel then long copy: Decode panicked on device-supplied frame 00 68 ff 65: runtime error: index out of range [18446744073709551615] with length 0
    (all three sub-cases fail the same way)
```

- **Smallest fix.** Pop `r := leUint64(...)`; `if r == 0 || r > uint64(len(d.stack)) { return out, fmt.Errorf(...) }`; then append `d.stack[r-1]`. Add a `recover` in `pump` that ends the stream with an error. (A 30 s fuzz run with long-form copies excluded found no other panic.)

### DH-04. A state-capture `select` filter never panics the daemon

- **Severity: critical.** An ordinary agent filter over ordinary integer state (`.a.b` where `a` is a number, `.hp + 1`, `.n | type`) kills the daemon.
- **Mechanism.** `normaliseForJQ` converts every integer to `int64`, a type gojq does not support, and gojq panics ("invalid type: int64"). `ApplyJQ` recovers that panic, but `StateCapture.applyJQ` runs on the capture's own goroutine without a `recover`.
- **Where.** `internal/appchannel/state_capture.go:212`, `244-274`; root cause `internal/appchannel/jq.go:143-157`.
- **Reproduction:**

```go
// A state capture's select is applied on the capture's own goroutine. A jq
// type error there (here `.a.b` on {"a": 1}, i.e. a slice whose shape is not
// what the filter assumed) must be counted as a sample error, not panic.
func TestDefectHunt_StateCaptureSelectTypeErrorDoesNotCrashDaemon(t *testing.T) {
	const name = "TestDefectHunt_StateCaptureSelectTypeErrorDoesNotCrashDaemon"
	if dhIsChild(name) {
		m, l, addr := dhStart(t)
		defer dhStopBounded(l)
		conn := dhHello(t, addr, "select-app", MethodStateQuery)
		result, _ := PackParams(map[string]any{"a": 1})
		dhServe(conn, func(env *Envelope) *Envelope { return &Envelope{ID: env.ID, Result: result} })
		s := dhWaitSession(t, m, "select-app")
		c, err := s.StartStateCaptureWithSelect("world", MinStateCaptureInterval, ".a.b")
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		time.Sleep(time.Second)
		r, err := s.StopStateCapture(c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if r.Errors == 0 {
			t.Fatalf("select errors were not counted: %+v", r)
		}
		return
	}
	out, err := dhRunChild(t, name)
	if err != nil {
		t.Fatalf("process died while a state capture applied select=.a.b to {\"a\":1} (%v):\n%s", err, dhFirstLines(out))
	}
}
```

```
--- FAIL: TestDefectHunt_StateCaptureSelectTypeErrorDoesNotCrashDaemon (0.02s)
    process died while a state capture applied select=.a.b to {"a":1} (exit status 2):
        panic: invalid type: int64 (1)
```

- **Smallest fix.** Normalise integers to `int` (and out-of-range `uint64` to `float64` or `*big.Int`) in `normaliseForJQ`, and call the recovering `ApplyJQ` from the capture loop.

### DH-05. The daemon never grants a device that `spyder run` holds

- **Severity: critical.** Double lease in normal use: while `spyder run --device iPad -- xcodebuild test …` holds the iPad, the daemon grants the same iPad to any other agent, and its next write erases the `spyder run` record from disk.
- **Mechanism.** `spyder run` (`runCmd`) opens its own `reservations.Store` over `~/.spyder/reservations.json` and acquires, renews and releases in that process. The daemon's `Store` reads the file only once, in `New` at startup, and from then on trusts its in-memory map. Neither process re-reads before deciding, so each grants against its own stale view, and each `saveLocked` replaces the whole file with its own map (last writer wins).
- **Where.** `main.go:388-411`, `443-453`, `468-479`; `internal/reservations/reservations.go:83-99`, `129-178`, `341-365`; `internal/daemon/daemon.go:333-347`.
- **Reproduction** (`internal/reservations/zz_defecthunt_test.go`):

```go
// The daemon and `spyder run` (main.go runCmd) each open their own Store
// over ~/.spyder/reservations.json. The daemon reads the file once, at
// startup. A device `spyder run` holds is therefore free in the daemon.
func TestDefectHunt_SpyderRunLeaseInvisibleToDaemon(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reservations.json")
	daemon, err := New(path) // daemon.Build at serve time
	if err != nil {
		t.Fatal(err)
	}
	run, err := New(path) // main.go runCmd, later
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.Acquire("iPad", "job-A", DefaultTTL, "spyder run: xcodebuild test"); err != nil {
		t.Fatal(err)
	}
	// Another agent asks the daemon for the same device.
	if _, err := daemon.Acquire("iPad", "job-B", DefaultTTL, ""); err == nil {
		t.Errorf("daemon leased iPad to job-B while `spyder run` holds it for job-A")
	}
	// And the daemon's write erased job-A's record from disk too.
	reread, _ := New(path)
	if r, ok := reread.Get("iPad"); ok && r.Owner == "job-B" {
		t.Errorf("file now says job-B holds iPad; job-A's lease is gone from disk")
	}
}
```

```
--- FAIL: TestDefectHunt_SpyderRunLeaseInvisibleToDaemon (0.01s)
    daemon leased iPad to job-B while `spyder run` holds it for job-A
    file now says job-B holds iPad; job-A's lease is gone from disk
```

- **Smallest fix.** Make the daemon the only writer: have `runCmd` acquire, renew and release through the daemon's REST `reserve`/`renew`/`release` (it already does this for `--on`), falling back to the file only when no daemon is reachable. If two writers must remain, take an exclusive `flock` on a sibling lock file, re-read the file under it before every decision, and write under it.

### DH-06. A slow device call on one device never stalls unrelated verbs or restarts the daemon

- **Severity: critical.** One slow crash-log pull (or an Android `input`, `port_forward`, `rotate`, `record_start`, or a `reserve`/`resolve` with a selector) blocks every verb that touches `h.mu`; the blocked verbs overrun their deadlines and the stuck-dispatch watchdog exits the daemon mid-job for everyone.
- **Mechanism.** Several handlers take the global handler mutex `h.mu` with `defer h.mu.Unlock()` and then do device I/O while holding it. Almost every verb needs `h.mu` briefly to resolve its device, so all of them queue behind the slow call, including pure inventory lookups. When a queued verb passes its deadline (15 s for fast reads) and is still blocked 5 s later, `maybeSelfRestartAfterStuck` force-stalls and calls `os.Exit(1)`. The code states the intended rule ("do not hold it across device I/O — a wedged network op must not stall unrelated tools", 🎯T99.2) but these handlers break it.
- **Where.** `internal/mcp/tools.go:1767-1797` (crashes: `ResolveExecutable` and `Crashes` under `h.mu`), `tools.go:1722-1734` (rotate), `tools.go:1823` (record_start), `tools.go:475-479` and `1122-1124` → `1019` (resolve/reserve with a selector call `adapter.List()` with no timeout under `h.mu`), `internal/mcp/android_control.go:221, 257, 287, 325, 383, 446, 498, 537` (port forward, input tap/swipe/key, notification shade), `tools.go:1189-1193` (release waits up to 10 s for a recorder under `h.mu`).
- **Reproduction:**

```go
// A slow crash-report pull on one device holds h.mu for its whole
// duration. A pure inventory lookup on the same daemon then waits for
// h.mu, overruns its 15 s FastRead deadline, and the stuck-dispatch
// watchdog exits the daemon.
func TestDefectHunt_SlowCrashesPullStallsResolveAndRestartsDaemon(t *testing.T) {
	save := DeadlineFastRead
	DeadlineFastRead = 150 * time.Millisecond // stands in for 15 s
	t.Cleanup(func() { DeadlineFastRead = save })

	release := make(chan struct{})
	ios := &stubAdapter{crashes: func(string, time.Time, string) ([]device.CrashReport, error) {
		<-release // a long .ips copy over USB
		return nil, nil
	}}
	h, exits := defectHuntHandler(t, ios)
	t.Cleanup(func() { close(release) })

	go h.Dispatch(context.Background(), "crashes", map[string]any{"device": "iPad"})
	time.Sleep(50 * time.Millisecond) // crashes now holds h.mu

	started := time.Now()
	res, err := h.Dispatch(context.Background(), "resolve", map[string]any{"name": "iPad"})
	elapsed := time.Since(started)
	if err != nil || res == nil || res.IsError {
		t.Errorf("resolve (an inventory lookup) failed after %v: err=%v res=%+v", elapsed, err, res)
	}
	time.Sleep(400 * time.Millisecond) // past the 100 ms restart grace
	if n := exits.Load(); n > 0 {
		t.Errorf("daemon self-restarted %d time(s) because resolve waited on h.mu behind crashes", n)
	}
}
```

```
--- FAIL: TestDefectHunt_SlowCrashesPullStallsResolveAndRestartsDaemon (0.60s)
    resolve (an inventory lookup) failed after 151ms: … tool timeout: resolve after 151ms
    daemon self-restarted 1 time(s) because resolve waited on h.mu behind crashes
```

- **Smallest fix.** In each listed handler, hold `h.mu` only for `authorize` + `resolveAdapter` (as `launchAppLocked` and `handleNetwork` already do) and do the device call after unlocking; use `listWithTimeout` in `buildCandidates`.

### DH-07. A client that hangs up never causes a daemon self-restart

- **Severity: critical.** Ordinary CLI use restarts the daemon: `spyder crashes` gives up after 10 s while the daemon allows the verb 60 s, so any crash pull longer than 10 s ends with the daemon exiting 5 s later, killing every other job's calls and sessions.
- **Mechanism.** `Dispatch` derives its context from the transport request. A client disconnect cancels it, and the `case <-ctx.Done()` branch treats that exactly like a deadline: it returns "tool timeout" and starts `maybeSelfRestartAfterStuck`. A context-less handler (most verbs in `toolHandlers()`) keeps running after the cancel, so 5 s later the watchdog exits the process although no deadline was ever exceeded. CLI timeouts are shorter than daemon deadlines for several verbs (`crashes`, `list-apps`: 10 s vs 60 s/15 s; `reserve`/`release`/`renew`: 30 s vs 60 s; `install`: 5 min vs a size-scaled budget that can exceed it).
- **Where.** `internal/mcp/server.go:637-659` and `678-696`; CLI defaults in `internal/clitimeout/clitimeout.go`, e.g. `cli.go:1330` (crashes uses `DefaultRead`).
- **Reproduction:**

```go
// A client that hangs up (CLI Ctrl-C, HTTP disconnect) cancels the
// dispatch context. Dispatch treats that exactly like a deadline: if a
// context-less handler is still running after the grace, it exits the
// daemon — no deadline was ever exceeded.
func TestDefectHunt_ClientHangupOnContextlessVerbRestartsDaemon(t *testing.T) {
	ios := &stubAdapter{rotate: func(string, string) error {
		time.Sleep(time.Second) // a 1 s rotation; DeadlineDeviceOp is 60 s
		return nil
	}}
	h, exits := defectHuntHandler(t, ios)

	ctx, hangUp := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); hangUp() }()
	res, _ := h.Dispatch(ctx, "rotate", map[string]any{"device": "iPad", "orientation": "landscape"})
	if res != nil && res.IsError {
		t.Logf("client saw: %s", resultErrorText(t, res))
	}
	time.Sleep(400 * time.Millisecond)
	if n := exits.Load(); n > 0 {
		t.Errorf("daemon self-restarted %d time(s) because a client hung up on a 1 s rotate", n)
	}
}

// The CLI gives `spyder crashes` a 10 s HTTP timeout (clitimeout.DefaultRead)
// while the daemon allows the crashes verb 60 s. A pull that takes longer
// than 10 s ends the HTTP request, which cancels the dispatch context; the
// context-less crashes handler is still running 5 s later, so the daemon
// exits. Scaled: CLI 100 ms, pull 1 s, grace 100 ms.
func TestDefectHunt_CLICrashesTimeoutRestartsDaemon(t *testing.T) {
	ios := &stubAdapter{crashes: func(string, time.Time, string) ([]device.CrashReport, error) {
		time.Sleep(time.Second)
		return nil, nil
	}}
	h, exits := defectHuntHandler(t, ios)
	cliCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	h.Dispatch(cliCtx, "crashes", map[string]any{"device": "iPad"})
	time.Sleep(400 * time.Millisecond)
	if n := exits.Load(); n > 0 {
		t.Errorf("`spyder crashes` timing out on the client restarted the daemon (%d exit(s))", n)
	}
}
```

```
--- FAIL: TestDefectHunt_ClientHangupOnContextlessVerbRestartsDaemon (0.45s)
    client saw: tool timeout: rotate on iPad after 51ms
    daemon self-restarted 1 time(s) because a client hung up on a 1 s rotate
--- FAIL: TestDefectHunt_CLICrashesTimeoutRestartsDaemon (0.50s)
    `spyder crashes` timing out on the client restarted the daemon (1 exit(s))
```

- **Smallest fix.** Only start `maybeSelfRestartAfterStuck` when the dispatch's own deadline fired: check `errors.Is(ctx.Err(), context.DeadlineExceeded)` and that the deadline is the one `withDispatchDeadline` set (keep the derived deadline and compare, or use `context.WithTimeoutCause` with a sentinel cause). On a plain cancel, return "cancelled" and let the handler finish.

### DH-08. A failed or interrupted `sim_acquire` never deletes a simulator it did not create

- **Severity: critical.** An ordinary `simctl create` error destroys another job's leased simulator (or the owner's hand-made one) when the requested name matches. Pinned names collide easily, for example simctl's default "iPhone 16 Pro".
- **Mechanism.** A created lease is recorded as pending with only a name until `Create` returns. If `Create` fails, `abandon` → `release` → `undo` matches any simulator with that name (`l.UDID == "" && s.Name == l.Name`), regardless of device type, runtime, other leases or claims, and shuts it down and deletes it. The same happens when the caller's context dies during `Create` (the reaper undoes it later) and after a crash mid-create.
- **Where.** `internal/simlease/simlease.go:600-609` (match by name), reached from `393-397` and `427-439`.
- **Reproduction** (`internal/simlease/zz_defecthunt_simlease_test.go`; uses the package's existing `fakeSimctl`; helpers in the appendix):

```go
// A sim_acquire whose `simctl create` fails must not touch any simulator
// it did not create. Here the requested name equals the name of another
// owner's leased, booted simulator on a different runtime (simctl's own
// default names, "iPhone 16 Pro", collide like this routinely).
func TestDefectHunt_FailedCreateNeverDeletesAnotherSimulator(t *testing.T) {
	const victim = "AAAAAAAA-0000-4000-8000-0000000000AA"
	f := &fakeSimctl{}
	f.add(victim, "iPhone 16 Pro", iphone16, runtime18, "Booted")
	d := &dhSimctl{fakeSimctl: f}
	m := dhManager(t, d, fixedNow(), "")

	// Owner b leases the existing simulator.
	b, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "b", Name: "iPhone 16 Pro"})
	if err != nil {
		t.Fatal(err)
	}
	if b.UDID != victim {
		t.Fatalf("setup: b leased %s, want %s", b.UDID, victim)
	}

	// Owner a asks for the same name on another runtime; create fails.
	d.createErr = errors.New("simctl create: runtime unavailable")
	if _, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime17, Owner: "a", Name: "iPhone 16 Pro"}); err == nil {
		t.Fatal("acquire succeeded despite create failure")
	}

	if got := f.state(victim); got != "Booted" {
		t.Fatalf("owner b's leased simulator is %q after owner a's failed create (mutations: %v); want it untouched (Booted)",
			got, f.mutations())
	}
}
```

```
--- FAIL: TestDefectHunt_FailedCreateNeverDeletesAnotherSimulator (0.00s)
    owner b's leased simulator is "gone" after owner a's failed create (mutations: [shutdown AAAAAAAA-0000-4000-8000-0000000000AA delete AAAAAAAA-0000-4000-8000-0000000000AA]); want it untouched (Booted)
```

- **Smallest fix.** When `Create` returns an error, drop the record without any undo (nothing was created). For crash recovery, require name, device type and runtime to match, skip UDIDs another lease holds or someone has claimed, and act only on a single match; better, make created names always unique and refuse a pinned name that already exists.

### DH-09. A reservation gates every name the resolver accepts for the device

- **Severity: high.** Two jobs drive one device: a reservation on the alias does not stop another owner (or an anonymous caller) who names the same device by its display name or by its UDID without the dash.
- **Mechanism.** The reservation key comes from the daemon's normaliser: the inventory alias when `inventory.Lookup` matches (alias case-insensitively, UDIDs exactly), else the raw string. `resolveAdapter` is more permissive: a ref that is neither in the inventory nor a well-formed UDID falls to `resolveUninventoried`, which matches a connected iOS device by name or by dash-stripped, case-folded UDID. So `"Lab iPad"` and `"00008103001122334455667A"` reach the reserved iPad under reservation keys nobody holds.
- **Where.** Normaliser `internal/daemon/daemon.go:336-341` (and `main.go:372-377`); `internal/mcp/tools.go:1474-1487`, `1515-1536`; `internal/inventory/inventory.go:117-131`.
- **Reproduction:**

```go
// The reservation key is canonicalised by the daemon's normaliser
// (inventory alias, else the raw string), but resolveAdapter also
// accepts a connected device's name or its UDID without dashes. Those
// forms reach the same physical device under a different reservation
// key, so a reservation on the alias does not gate them.
func TestDefectHunt_ReservationBypassedByDeviceNameOrDashlessUDID(t *testing.T) {
	const udid = "00008103-001122334455667A" // iPad in testInventory
	var launchedOn []string
	ios := &stubAdapter{
		list: func() ([]device.Info, error) {
			return []device.Info{{UUID: udid, Name: "Lab iPad", Platform: "ios"}}, nil
		},
		launchApp: func(id, _ string, _ map[string]string) error {
			launchedOn = append(launchedOn, id)
			return nil
		},
	}
	h, _ := defectHuntHandler(t, ios)
	// daemon.Build's normaliser, verbatim.
	resv, _ := reservations.New("", reservations.WithNormalizer(func(ref string) string {
		if e, ok := h.inventory.Lookup(ref); ok && e.Alias != "" {
			return e.Alias
		}
		return ref
	}))
	h.reservations = resv
	if _, err := resv.Acquire("iPad", "job-A", time.Hour, ""); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"Lab iPad", "00008103001122334455667A"} {
		res, err := h.Dispatch(context.Background(), "launch_app", map[string]any{
			"device": ref, "bundle_id": "com.other.app", "owner": "job-B",
		})
		if err == nil && res != nil && !res.IsError {
			t.Errorf("job-B launched an app on job-A's reserved iPad via %q", ref)
		}
	}
	t.Logf("launches reached device ids: %v", launchedOn)
}
```

```
--- FAIL: TestDefectHunt_ReservationBypassedByDeviceNameOrDashlessUDID (0.00s)
    job-B launched an app on job-A's reserved iPad via "Lab iPad"
    job-B launched an app on job-A's reserved iPad via "00008103001122334455667A"
    launches reached device ids: [00008103-001122334455667A 00008103-001122334455667A]
```

- **Smallest fix.** Key reservations by the resolved device id, not by a string normalisation: in `authorize`, `handleReserve`, `handleRelease` and `handleRenew`, call `resolveAdapter` first and pass `platform+":"+id` (or the inventory alias for that id) to the store. Failing that, make the normaliser use the same resolution as `resolveAdapter`.

### DH-10. Two `spyder run` jobs never share one lease because they started in the same directory

- **Severity: high.** Double lease: two pueue tasks or two terminals in one repository run device tests on the same device at once, and when the first finishes it releases the lease the second still depends on.
- **Mechanism.** Without `--as`, `deriveOwner` uses `filepath.Base(cwd)` as the owner. `Acquire` by the same owner is a renewal, not a conflict. Any two invocations from directories with the same base name (the same checkout, or two clones both named after the repo) are the same owner.
- **Where.** `main.go:348-357`, `370`; `internal/reservations/reservations.go:146-161`.
- **Reproduction** (`zz_defecthunt_test.go` in the repository root, package `main`; it re-executes the test binary to run `runCmd`, which calls `os.Exit`; all state is under temp dirs):

```go
// Re-exec hook: the child process runs runCmd (which calls os.Exit).
func TestDefectHuntRunCmdHelper(t *testing.T) {
	if os.Getenv("DEFECTHUNT_RUNCMD") != "1" {
		t.Skip("helper")
	}
	runCmd(strings.Split(os.Getenv("DEFECTHUNT_ARGS"), "\x1f"))
}

// With no --as, `spyder run` uses filepath.Base(cwd) as the owner, and
// Acquire by the same owner renews instead of conflicting. Two jobs
// started from the same directory (two pueue tasks, two terminal tabs,
// or two clones that share a directory name) both get the device.
func TestDefectHunt_TwoRunsFromSameDirShareTheLease(t *testing.T) {
	data := t.TempDir()
	work := filepath.Join(t.TempDir(), "game")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	start := func(job string) *exec.Cmd {
		args := []string{"--device", "fake-device", "--", "/bin/sh", "-c", job}
		cmd := exec.Command(os.Args[0], "-test.run=^TestDefectHuntRunCmdHelper$")
		cmd.Dir = work
		cmd.Env = append(os.Environ(),
			"DEFECTHUNT_RUNCMD=1",
			"DEFECTHUNT_ARGS="+strings.Join(args, "\x1f"),
			paths.DataDirEnv+"="+data,
			"HOME="+t.TempDir())
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return cmd
	}
	first := start("sleep 2")
	time.Sleep(500 * time.Millisecond) // first holds the device now
	second := start("true")
	err := second.Wait()
	_ = first.Wait()
	if err == nil {
		t.Errorf("a second `spyder run` from the same directory got the device the first was still using (exit 0, want 3)")
	}
}
```

```
--- FAIL: TestDefectHunt_TwoRunsFromSameDirShareTheLease (2.02s)
    a second `spyder run` from the same directory got the device the first was still using (exit 0, want 3)
```

- **Smallest fix.** Default the owner to something unique per invocation, such as `<basename>:<pid>@<host>`, and print it, so a deliberate shared owner needs `--as`.

### DH-11. `spyder run --on` releases the daemon's lease when the job ends and keeps it renewed while the job runs

- **Severity: high.** It leaves the device reserved for up to an hour after every `spyder run --on`, and a job longer than an hour loses its lease mid-run to another job.
- **Mechanism.** With `--on`, `reserveViaDaemon` acquires in the daemon's store. `runCmd` then opens its local store, "re-acquires" (same owner → renew), renews every 20 minutes and finally releases, all locally. The daemon's copy is neither renewed nor released: after the job it stays held by the finished job until `DefaultTTL` (1 h) passes; during a job longer than 1 h it expires and the daemon grants the device to someone else. The daemon-side run opened by `handleReserve` is also never closed, so it is never archived.
- **Where.** `main.go:379-386`, `404`, `443-453`, `468-479`; `cli.go:662-695`.
- **Reproduction:**

```go
// `spyder run --on` reserves through the daemon, then renews and
// releases through its own Store. The daemon's copy is never released.
func TestDefectHunt_SpyderRunOnReleaseLeavesDaemonLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reservations.json")
	daemon, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	// reserveViaDaemon → handleReserve → daemon store.
	if _, err := daemon.Acquire("iPad", "job-A", DefaultTTL, "spyder run --on"); err != nil {
		t.Fatal(err)
	}
	// runCmd: New + Acquire (same owner → renew) … child runs … Release.
	run, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.Acquire("iPad", "job-A", DefaultTTL, "spyder run: make test"); err != nil {
		t.Fatal(err)
	}
	if err := run.Release("iPad", "job-A"); err != nil {
		t.Fatal(err)
	}
	if r, ok := daemon.Get("iPad"); ok {
		t.Errorf("after `spyder run` released, the daemon still holds iPad for %s until %s",
			r.Owner, r.ExpiresAt.Format(time.RFC3339))
	}
	if _, err := daemon.Acquire("iPad", "job-B", DefaultTTL, ""); err != nil {
		t.Errorf("next job cannot reserve the released device: %v", err)
	}
}

// `spyder run --on` renews only its own Store; the daemon's copy of the
// same lease is never renewed and lapses after DefaultTTL while the
// wrapped job is still running. The daemon then grants the device to
// someone else.
func TestDefectHunt_SpyderRunOnLongJobLosesDaemonLease(t *testing.T) {
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	path := filepath.Join(t.TempDir(), "reservations.json")
	daemon, _ := New(path, WithNow(clock))
	if _, err := daemon.Acquire("iPad", "job-A", DefaultTTL, "spyder run --on"); err != nil {
		t.Fatal(err)
	}
	run, _ := New(path, WithNow(clock))
	if _, err := run.Acquire("iPad", "job-A", DefaultTTL, "spyder run: long soak"); err != nil {
		t.Fatal(err)
	}
	// runCmd's ticker renews every DefaultTTL/3 for a 2-hour job.
	for i := 0; i < 6; i++ {
		now = now.Add(DefaultTTL / 3)
		if _, err := run.Renew("iPad", "job-A", DefaultTTL); err != nil {
			t.Fatalf("renew %d: %v", i, err)
		}
	}
	if _, err := daemon.Acquire("iPad", "job-B", DefaultTTL, ""); err == nil {
		t.Errorf("job-A's `spyder run` is still renewing, but the daemon granted iPad to job-B")
	}
}
```

```
--- FAIL: TestDefectHunt_SpyderRunOnReleaseLeavesDaemonLease (0.00s)
    after `spyder run` released, the daemon still holds iPad for job-A until 2026-11-20T15:29:37+11:00
    next job cannot reserve the released device: device iPad is reserved by job-A until … (note: "spyder run --on")
--- FAIL: TestDefectHunt_SpyderRunOnLongJobLosesDaemonLease (0.00s)
    job-A's `spyder run` is still renewing, but the daemon granted iPad to job-B
```

- **Smallest fix.** The same as the previous defect: route renew and release through the daemon. A narrower fix for `--on` alone: call REST `renew` from the ticker and REST `release` in `release()`.

### DH-12. `spyder run` never releases the lease while the wrapped job is still running

- **Severity: high.** When `--timeout` fires (or `spyder run` gets SIGTERM from pueue, CI or a supervisor), the reservation is released and spyder exits while the job's worker processes keep driving the device, which the next lease holder now also uses.
- **Mechanism.** The child is started with `exec.CommandContext` and no process group. On cancellation Go sends SIGKILL to the direct child only; a wrapping shell, `make`, or `xcodebuild` dies, but its children are reparented and keep running. `runCmd` then calls `release()` and exits 30.
- **Where.** `main.go:460-467`, `481-492`.
- **Reproduction:**

```go
// `spyder run --timeout` kills only its direct child (exec.CommandContext
// → SIGKILL to one pid). A wrapped shell/xcodebuild's own children keep
// running — and keep driving the device — after spyder has released the
// reservation and exited.
func TestDefectHunt_RunTimeoutOrphansWrappedJob(t *testing.T) {
	data := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "worker.pid")
	// The "test job": a shell that starts a worker and waits for it.
	job := "sleep 30 & echo $! > " + pidFile + "; wait"
	args := []string{"--device", "fake-device", "--as", "job-A", "--timeout", "1s", "--", "/bin/sh", "-c", job}

	cmd := exec.Command(os.Args[0], "-test.run=^TestDefectHuntRunCmdHelper$")
	cmd.Env = append(os.Environ(),
		"DEFECTHUNT_RUNCMD=1",
		"DEFECTHUNT_ARGS="+strings.Join(args, "\x1f"),
		paths.DataDirEnv+"="+data,
		"HOME="+t.TempDir())
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 30 {
		t.Fatalf("want exit 30 (timeout), got %v", err)
	}

	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	time.Sleep(200 * time.Millisecond)

	store, _ := reservations.New(filepath.Join(data, "reservations.json"))
	_, held := store.Get("fake-device")
	alive := syscall.Kill(pid, 0) == nil
	if alive && !held {
		t.Errorf("reservation released and spyder exited, but the job's worker (pid %d) is still running", pid)
	}
}
```

```
--- FAIL: TestDefectHunt_RunTimeoutOrphansWrappedJob (1.22s)
    reservation released and spyder exited, but the job's worker (pid 43462) is still running
```

- **Smallest fix.** Start the child with `SysProcAttr{Setpgid: true}`, set `cmd.Cancel` to SIGTERM the process group (`syscall.Kill(-pid, SIGTERM)`) and `cmd.WaitDelay` to escalate to SIGKILL of the group, and release only after the group has gone.

### DH-13. Concurrent writers never corrupt `reservations.json`, and a corrupt file never turns enforcement off

- **Severity: high.** A corrupt book makes the next daemon start run with reservations disabled (every mutating verb from every owner is allowed, and `reserve` reports "reservations not configured") and makes every `spyder run` fail.
- **Mechanism.** Both writers stage through the same fixed path, `reservations.json.tmp`, with `os.WriteFile` (open with `O_TRUNC`, write, close) and then rename. When the daemon and `spyder run` save at the same time, one process's truncate and shorter write land inside the other's longer write, and the mixed bytes are renamed into place. `daemon.Build` treats any load error other than "not exist" as "reservations unavailable; strict mode disabled" and sets the store to nil.
- **Where.** `internal/reservations/reservations.go:356-364`; `internal/daemon/daemon.go:343-347`; the same fixed `.tmp` pattern is in `internal/runs/runs.go:586-595` and `internal/mcp/launched.go:172-180`.
- **Reproduction:**

```go
// Both writers use the same "<path>.tmp" staging file. Interleaved
// O_TRUNC opens and writes can leave mixed bytes that are then renamed
// into place; the next New() fails to parse and the daemon disables
// reservations (daemon.Build: resvStore = nil).
func TestDefectHunt_SharedTmpFileCorruptsBook(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reservations.json")
	a, _ := New(path)
	b, _ := New(path)
	// Different sizes so a short write over a long one leaves a tail.
	for i := 0; i < 20; i++ {
		a.Acquire(fmt.Sprintf("dev-a-%02d", i), "owner-with-a-long-name-"+strings.Repeat("x", 200), DefaultTTL, strings.Repeat("n", 300))
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			a.Renew("dev-a-00", "owner-with-a-long-name-"+strings.Repeat("x", 200), DefaultTTL)
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			b.Acquire("short", "b", DefaultTTL, "")
			b.Release("short", "b")
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	var bad error
	for time.Now().Before(deadline) && bad == nil {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var v []Reservation
		if err := json.Unmarshal(data, &v); err != nil {
			bad = fmt.Errorf("%v (len %d)", err, len(data))
		}
	}
	close(done)
	wg.Wait()
	if bad != nil {
		_, err := New(path)
		t.Errorf("reservations.json became unparseable: %v; New() now returns: %v", bad, err)
	}
}
```

```
--- FAIL: TestDefectHunt_SharedTmpFileCorruptsBook (0.01s)
    reservations.json became unparseable: invalid character 'x' after top-level value (len 13954);
    New() now returns: load reservations: parsing reservations: invalid character '{' after top-level value
```

- **Smallest fix.** Stage with `os.CreateTemp(filepath.Dir(path), "reservations-*.tmp")`, `fsync`, then rename. In `daemon.Build`, fail closed on a corrupt book: move it aside (`reservations.json.corrupt-<time>`), start with an empty book, and mark health `needs_attention`, instead of disabling enforcement.

### DH-14. The self-restart limit (3 per hour) holds across restarts

- **Severity: high.** It removes the brake on every self-restart defect in this report: a stuck verb that recurs after each restart (a wedged device the next caller touches again) restarts the daemon without limit instead of stopping at 3 and asking for attention.
- **Mechanism.** `SelfRestartLimiter` keeps its attempt history in process memory. The first allowed `Request` calls `os.Exit(1)`, and the relaunched daemon starts with an empty history, so the "3 per hour" branch is unreachable in production.
- **Where.** `internal/health/self_restart.go:31-44`, `69-109`; `internal/mcp/server.go:339`.
- **Reproduction:**

```go
// The limiter that is meant to cap self-restarts at 3 per hour lives in
// process memory and its first allowed Request calls os.Exit. Each
// relaunch starts a fresh limiter, so the cap is never reached.
func TestDefectHunt_SelfRestartLimitResetsEveryRelaunch(t *testing.T) {
	exits := 0
	for relaunch := 0; relaunch < 5; relaunch++ {
		// EnableSelfHeal: health.NewSelfRestartLimiter(3, time.Hour)
		l := health.NewSelfRestartLimiterForTest(3, time.Hour, func(int) { exits++ })
		l.Request("dispatch stuck after deadline")
	}
	if exits > 3 {
		t.Errorf("%d self-restarts within one hour; the limit is 3", exits)
	}
}

// The child-process reader found the same defect independently
// (internal/health/zz_defecthunt_supervise_test.go):
// SelfRestartLimiter promises at most max self-restarts per window. In
// production (mcp/server.go:337) it is NewSelfRestartLimiter(3, time.Hour)
// and an allowed Request ends the process (os.Exit), so the record of that
// attempt dies with it. Each relaunched daemon is a fresh limiter, so a
// wedge that recurs after every restart restarts the daemon without limit.
// Here each "lifetime" is one limiter, as it is in production.
func TestDefectHunt_SelfRestartBudgetSurvivesTheRestartItCauses(t *testing.T) {
	allowed := 0
	for lifetime := 1; lifetime <= 5; lifetime++ {
		exited := false
		l := NewSelfRestartLimiterForTest(3, time.Hour, func(int) { exited = true })
		if l.Request("dispatch stuck after deadline") {
			allowed++
		}
		if !exited {
			break // rate-limited: this lifetime keeps running
		}
	}
	if allowed > 3 {
		t.Fatalf("self-restart budget is 3 per hour, but %d consecutive daemon lifetimes within the hour each exited", allowed)
	}
}
```

```
--- FAIL: TestDefectHunt_SelfRestartLimitResetsEveryRelaunch (0.00s)
    5 self-restarts within one hour; the limit is 3
--- FAIL: TestDefectHunt_SelfRestartBudgetSurvivesTheRestartItCauses (0.00s)
    self-restart budget is 3 per hour, but 5 consecutive daemon lifetimes within the hour each exited
```

- **Smallest fix.** Persist attempt times (for example append to `~/.spyder/self-restarts.jsonl`, which `exitlog` nearly does already) and load them in `NewSelfRestartLimiter`.

### DH-15. No two leases ever hold one simulator, and the reaper never deletes a simulator a live lease holds

- **Severity: high.** Double lease of a simulator, and deletion of a simulator under a live lease. One path needs a crash mid-create; the other is a narrow race with no crash.
- **Mechanism.** `Acquire`'s "already leased" set is keyed only by UDID. A pending created lease has no UDID yet, so its `spyder-lease-<id>` simulator looks free. After a crash mid-create, a new acquire can take the orphan, and the reaper then deletes it by name under the live lease. With no crash, if the new simulator shows up in `simctl list` before `Create` returns, a second acquire leases it too.
- **Where.** `internal/simlease/simlease.go:329-338`, `604-605`.
- **Reproduction:**

```go
// A daemon that died after `simctl create` but before recording the UDID
// leaves a pending lease with only a name. Until the reaper undoes it, a
// new acquire can take that orphan simulator; the reaper must then not
// delete a simulator that a live lease holds.
func TestDefectHunt_ReaperNeverDeletesSimulatorHeldByLiveLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sim-leases.json")
	f := &fakeSimctl{}
	now := fixedNow()
	t0 := now()

	// Daemon 1 dies mid-acquire: pending record persisted, sim created,
	// UDID never recorded.
	m1 := dhManager(t, f, now, path)
	m1.mu.Lock()
	dead := &Lease{ID: "deadbeefdeadbeef", Name: NamePrefix + "deadbeef", DeviceType: iphone16, Runtime: runtime18,
		Owner: "a", Action: ActionCreated, Pending: true, TTLSeconds: 3600, AcquiredAt: t0, ExpiresAt: t0.Add(time.Hour)}
	m1.leases[dead.ID] = dead
	m1.mustPersistLocked()
	m1.mu.Unlock()
	if _, err := f.Create(ctx, dead.Name, iphone16, runtime18); err != nil {
		t.Fatal(err)
	}

	// Daemon 2 starts; a client's acquire lands before the reaper's pass
	// (or after a pass whose undo failed).
	m2 := dhManager(t, f, now, path)
	b, err := m2.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "b"})
	if err != nil {
		t.Fatal(err)
	}
	m2.Reap(ctx)

	if got := f.state(b.UDID); got != "Booted" {
		t.Fatalf("owner b's live lease %s on %s: simulator is %q after reap (mutations: %v); want Booted",
			b.ID, b.UDID, got, f.mutations())
	}
}

// No two leases ever hold one simulator: while owner a's created
// simulator is not yet recorded on its (pending, UDID-less) lease, owner
// b's acquire must not pick it up.
func TestDefectHunt_InFlightCreateIsNotLeasedTwice(t *testing.T) {
	f := &fakeSimctl{}
	s := &slowCreateSimctl{fakeSimctl: f, visible: make(chan struct{}), finish: make(chan struct{})}
	m := dhManager(t, s, fixedNow(), "")

	type res struct {
		l   Lease
		err error
	}
	aCh := make(chan res, 1)
	go func() {
		l, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "a"})
		aCh <- res{l, err}
	}()
	<-s.visible
	// b uses the plain fake so its own (possible) create does not block.
	b, errB := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "b"})
	close(s.finish)
	a := <-aCh
	if a.err != nil || errB != nil {
		t.Fatalf("acquires: a=%v b=%v", a.err, errB)
	}
	if a.l.UDID == b.UDID {
		t.Fatalf("simulator %s leased to a (%s, %s) and b (%s, %s) at once",
			a.l.UDID, a.l.ID, a.l.Action, b.ID, b.Action)
	}
}
```

```
--- FAIL: TestDefectHunt_ReaperNeverDeletesSimulatorHeldByLiveLease (0.00s)
    owner b's live lease 05f0a17359d9fe70 on 00000000-…-000000000001: simulator is "gone" after reap (mutations: [create spyder-lease-deadbeef boot … shutdown … delete …]); want Booted
--- FAIL: TestDefectHunt_InFlightCreateIsNotLeasedTwice (0.00s)
    simulator 00000000-0000-4000-8000-000000000001 leased to a (13b186493d3dbbde, created) and b (77fc2554c36a87e6, booted) at once
```

- **Smallest fix.** In `Acquire`'s candidate filter, also exclude simulators whose name equals any pending created lease's name; in `undo`, never touch a UDID another lease holds.

### DH-16. Two simulator lease books over one file never lease one simulator twice or lose a lease

- **Severity: high.** When two daemons share a data directory, which is exactly what CLAUDE.md's ":3131" recipe (`bin/spyder serve --addr :3131`, no `SPYDER_DATA_DIR`) does, each grants the other's leased simulator, the last writer erases the other's leases, and each reaper undoes the other's in-flight pending acquires. This is the simulator analogue of the reservation-book defect.
- **Mechanism.** `New` reads `sim-leases.json` once; every persist overwrites the whole file through the shared `<path>.tmp`. `sim-provenance.json` and `pool.db` are shared the same way.
- **Where.** `internal/simlease/simlease.go:183-199`, `207-225` (tmp file at `220`).
- **Reproduction:**

```go
// Two lease books over one file (the LaunchAgent daemon plus a tree
// `spyder serve --addr :3131` without SPYDER_DATA_DIR, as CLAUDE.md
// suggests) must neither lease one simulator twice nor lose a lease.
func TestDefectHunt_TwoManagersOverOneFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sim-leases.json")
	f := &fakeSimctl{}
	f.add("AAAAAAAA-0000-4000-8000-000000000001", "shut", iphone16, runtime18, "Shutdown")
	m1 := dhManager(t, f, fixedNow(), path)
	m2 := dhManager(t, f, fixedNow(), path)

	a, err := m1.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "a"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m2.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "b"})
	if err != nil {
		t.Fatal(err)
	}
	m3 := dhManager(t, f, fixedNow(), path)
	n := len(m3.Status(ctx).Leases)
	if a.UDID == b.UDID || n != 2 {
		t.Fatalf("a=%s (%s) b=%s (%s): same simulator=%v; lease file holds %d of 2 leases",
			a.ID, a.UDID, b.ID, b.UDID, a.UDID == b.UDID, n)
	}
}
```

```
--- FAIL: TestDefectHunt_TwoManagersOverOneFile (0.00s)
    a=7cb6784bfaee03b9 (AAAAAAAA-…-000000000001) b=fb12902592eecf62 (AAAAAAAA-…-000000000001): same simulator=true; lease file holds 1 of 2 leases
```

- **Smallest fix.** Hold an exclusive `flock` on the lease file for the daemon's lifetime (disable the book with a loud error if it is taken), stage writes with `os.CreateTemp`, and make the :3131 recipe set `SPYDER_DATA_DIR`.

### DH-17. `app_*` handlers end with their dispatch deadline

- **Severity: high.** An app that answers slowly (or keeps sending progress beats) past 60 s makes the stuck-dispatch watchdog exit the daemon, killing every device job. The same failure was fixed on 2026-11-11 for `wait_app_session` and `run_script`.
- **Mechanism.** Every `app_*` handler calls `s.Call(context.Background(), …)` and so ignores the dispatch context. `Call` keeps waiting while the app sends progress beats, and `timeout_ms` is uncapped. Past `DeadlineDeviceOp` plus the 5 s grace, `maybeSelfRestartAfterStuck` calls `os.Exit`.
- **Where.** `internal/mcp/app_channel.go:1004` (app_call) and the other `s.Call(context.Background(), …)` sites in that file; `internal/mcp/app_spawn.go:113`; `internal/mcp/app_calls_batch.go:127` (up to 32 calls in sequence).
- **Reproduction** (`internal/mcp/zz_defecthunt_appchannel_test.go`; uses the package's existing `selfHealHandler` and `requireNoSelfRestart`):

```go
// Session.Call promises that a slow app which keeps sending progress beats
// (a long cold load) is waited for. app_call honours that with
// context.Background(), so the handler outlives its dispatch deadline and the
// 🎯T99.3 watchdog exits the daemon — the same failure the 2026-11-11 tests in
// dispatch_ctx_selfrestart_test.go fixed for wait_app_session/run_script.
// Production numbers: DeadlineDeviceOp 60s + 5s grace; any app_call whose app
// beats for longer than ~65s (or any timeout_ms > 65000 on a silent app).
func TestDefectHunt_AppCallPastDeadline_NoSelfRestart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	save := DeadlineDeviceOp
	DeadlineDeviceOp = 80 * time.Millisecond
	t.Cleanup(func() { DeadlineDeviceOp = save })

	h, exits := selfHealHandler(t, &stubAdapter{})
	mgr := appchannel.NewManager()
	t.Cleanup(mgr.Close)
	h.appChannel = mgr
	l, err := mgr.Start(appchannel.StartParams{Owner: "defecthunt"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Stop)
	conn := dhAppConn(t, l.Port, "busy-game", nil, "load_level")
	sid := dhSessionID(t, mgr)

	// The app is alive and loading: it beats every 20ms but has not answered.
	stopBeats := make(chan struct{})
	t.Cleanup(func() { close(stopBeats) })
	go func() {
		beat, _ := appchannel.PackParams(appchannel.ProgressPush{MainQueue: 1})
		for {
			select {
			case <-stopBeats:
				return
			case <-time.After(20 * time.Millisecond):
			}
			if err := appchannel.WriteFrame(conn, &appchannel.Envelope{Method: appchannel.PushProgress, Params: beat}); err != nil {
				return
			}
		}
	}()

	res, err := h.Dispatch(context.Background(), "app_call", map[string]any{
		"session_id": sid, "method": "load_level",
	})
	if err != nil || res == nil || !res.IsError || !strings.Contains(resultErrorText(t, res), "tool timeout") {
		t.Fatalf("want a tool timeout, got %v %+v", err, res)
	}
	requireNoSelfRestart(t, exits)
}
```

```
ERROR self-restart: exiting for supervised relaunch reason="dispatch stuck after deadline: app_call device=" attempt=1 max=3
--- FAIL: TestDefectHunt_AppCallPastDeadline_NoSelfRestart (0.19s)
    self-restart fired: the handler outlived its dispatch deadline
```

- **Smallest fix.** Move the `app_*` verbs into `contextHandlers()` and pass the dispatch `ctx` through to `Call` and `SpawnInstance`.

### DH-18. A call's timeout bounds its socket write

- **Severity: high.** One suspended app (backgrounded, debugger-paused) hangs its handler forever. The blocked writer holds the session's write gate, so every later call to that session also hangs, and via the previous defect the daemon restarts.
- **Mechanism.** `writeEnvelopeContext` sets a write deadline only when the context has one, and all handlers pass `context.Background()`. A write to a peer that has stopped reading blocks once the socket buffer is full. The read loop's error replies and `Notify` write the same way.
- **Where.** `internal/appchannel/session.go:227`, `357-393`.
- **Reproduction:**

```go
// Call's timeout is documented as the idle budget. A peer that stops reading
// (suspended app, wedged channel thread) must not hold a Call — and with it
// the handler goroutine — forever just because the caller's ctx has no
// deadline (every app_* handler passes context.Background()).
func TestDefectHunt_CallTimeoutBoundsBlockedWrite(t *testing.T) {
	m, l, addr := dhStart(t)
	t.Cleanup(func() { dhStopBounded(l) })
	conn := dhHello(t, addr, "stalled-app", MethodRestoreState) // never reads again
	s := dhWaitSession(t, m, "stalled-app")

	done := make(chan error, 1)
	go func() {
		_, err := s.Call(context.Background(), MethodRestoreState,
			map[string][]byte{"state": make([]byte, 15<<20)}, time.Second)
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = conn.Close() // release the stuck writer before failing
		t.Fatal("Call with a 1s timeout still blocked after 5s writing to a peer that stopped reading")
	}
}
```

```
--- FAIL: TestDefectHunt_CallTimeoutBoundsBlockedWrite (5.00s)
    Call with a 1s timeout still blocked after 5s writing to a peer that stopped reading
```

- **Smallest fix.** In `Call`, write under `context.WithTimeout(ctx, timeout)`; give the other write paths a fixed write deadline.

### DH-19. A duplicate response from an app never wedges its session

- **Severity: high.** A buggy app leaves a permanent zombie session: the disconnect is never noticed, and `Close`, `Stop` and `app_channel_stop` hang forever.
- **Mechanism.** `handleFrame` delivers responses with `p.done <- env` into a 1-slot buffer and no `default`. Two responses for one id while the caller is not receiving fill it, and once the caller has given up the read loop blocks forever; `s.done` never closes and `Close()` waits on it.
- **Where.** `internal/appchannel/session.go:445`.
- **Reproduction:**

```go
// A misbehaving app that answers one request twice must not be able to wedge
// the session's read loop. If it does, the session can never end: the app's
// disconnect goes unnoticed and Session.Close blocks forever.
func TestDefectHunt_DuplicateResponseDoesNotWedgeSession(t *testing.T) {
	m, l, addr := dhStart(t)
	t.Cleanup(func() { dhStopBounded(l) })
	conn := dhHello(t, addr, "dup-app", "big") // the app stops reading after hello
	s := dhWaitSession(t, m, "dup-app")

	// Request id 1 is the first Call on the session. A body bigger than the
	// loopback socket buffers keeps the Call in its write while the app answers.
	callDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := s.Call(ctx, "big", map[string][]byte{"blob": make([]byte, 15<<20)}, time.Second)
		callDone <- err
	}()
	time.Sleep(200 * time.Millisecond)
	res, _ := PackParams(map[string]any{"ok": true})
	for i := 0; i < 2; i++ {
		if err := WriteFrame(conn, &Envelope{ID: 1, Result: res}); err != nil {
			t.Fatalf("duplicate response %d: %v", i, err)
		}
	}
	select {
	case <-callDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Call never returned")
	}

	_ = conn.Close() // the app goes away
	closed := make(chan struct{})
	go func() { _ = s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		_, live := m.GetSession(s.ID)
		buf := make([]byte, 1<<20)
		stacks := string(buf[:runtime.Stack(buf, true)])
		t.Fatalf("Session.Close still blocked 3s after the app disconnected (session still registered: %v; "+
			"a goroutine is parked in (*Session).handleFrame: %v)",
			live, strings.Contains(stacks, "appchannel.(*Session).handleFrame"))
	}
}
```

```
--- FAIL: TestDefectHunt_DuplicateResponseDoesNotWedgeSession (6.00s)
    Session.Close still blocked 3s after the app disconnected (session still registered: true; a goroutine is parked in (*Session).handleFrame: true)
```

- **Smallest fix.** `select { case p.done <- env: default: }`.

### DH-20. The instance pool never grants one app instance to two holders

- **Severity: high.** Double lease of a factory-spawned instance; the second spawned instance is orphaned and the capacity cap is exceeded.
- **Mechanism.** `InstancePool.Acquire` checks capacity under its lock but spawns outside it, and `SpawnInstance` takes the first new session it sees, so two concurrent spawns both pick the same instance.
- **Where.** `internal/appchannel/factory.go:72-123`; `internal/appchannel/spawn.go:40-61`.
- **Reproduction** (`internal/appchannel/zz_defecthunt_pool_test.go`):

```go
// Two players arriving within one instance boot window must get two
// instances. A lease granted twice puts two holders on one game; the
// instance spawned for the second holder is then tracked by nobody.
func TestDefectHunt_AcquireNeverGrantsOneInstanceTwice(t *testing.T) {
	m, l, addr := dhStart(t)
	t.Cleanup(func() { dhStopBounded(l) })

	var (
		mu        sync.Mutex
		spawnReqs int
		instances []*dhApp
	)
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, a := range instances {
			_ = a.conn.Close()
		}
	})
	factoryConn := dhHello(t, addr, "factory", MethodSpawnInstance)
	ok, _ := PackParams(map[string]any{"ok": true})
	dhServe(factoryConn, func(env *Envelope) *Envelope {
		if env.Method != MethodSpawnInstance {
			return nil
		}
		mu.Lock()
		spawnReqs++
		n := spawnReqs
		mu.Unlock()
		if n == 2 { // both requests in: the instances finish booting one after the other
			go func() {
				for i := 1; i <= 2; i++ {
					if a, err := dhDialApp(addr, fmt.Sprintf("instance-%d", i), MethodPing, MethodQuit); err == nil {
						mu.Lock()
						instances = append(instances, a)
						mu.Unlock()
					}
					time.Sleep(400 * time.Millisecond)
				}
			}()
		}
		return &Envelope{ID: env.ID, Result: ok}
	})
	factory := dhWaitSession(t, m, "factory")

	pool := NewInstancePool(m)
	type grant struct {
		holder string
		s      *Session
		err    error
	}
	grants := make(chan grant, 2)
	for _, holder := range []string{"alice", "bob"} {
		go func(h string) {
			s, err := pool.Acquire(context.Background(), factory, "tiltbuggy", h, 5*time.Second)
			grants <- grant{h, s, err}
		}(holder)
	}
	var got []grant
	for len(got) < 2 {
		select {
		case g := <-grants:
			if g.err != nil {
				t.Fatalf("Acquire(%s): %v", g.holder, g.err)
			}
			got = append(got, g)
		case <-time.After(10 * time.Second):
			t.Fatal("Acquire did not return")
		}
	}
	time.Sleep(600 * time.Millisecond) // let the second instance finish connecting
	if got[0].s.ID == got[1].s.ID {
		t.Fatalf("holders %s and %s were both granted instance %s (%s); pool now tracks %v while the manager has %d sessions "+
			"(factory + 2 instances) — the second instance is orphaned",
			got[0].holder, got[1].holder, got[0].s.ID, got[0].s.HelloInfo().AppName, pool.Instances(), len(m.Sessions()))
	}
}
```

```
--- FAIL: TestDefectHunt_AcquireNeverGrantsOneInstanceTwice (0.65s)
    holders alice and bob were both granted instance a68a8cf4f0c99953 (instance-1); pool now tracks map[eb212ad445e1ef4e:1] while the manager has 3 sessions (factory + 2 instances) — the second instance is orphaned
```

- **Smallest fix.** Hold a per-factory lock across `SpawnInstance`, and identify the new instance exactly (next defect).

### DH-21. `SpawnInstance` never adopts an unrelated app's session

- **Severity: high.** The pool leases another job's running app as "the instance" and, when that lease is released, sends it `quit`.
- **Mechanism.** Any session that appears on any listener in the daemon during the spawn wait counts as the new instance, and `gcIfIdle` later quits it.
- **Where.** `internal/appchannel/spawn.go:51-52`; `internal/appchannel/factory.go:157`.
- **Reproduction:**

```go
// SpawnInstance must return the instance the factory spawned, not whatever
// session happens to connect anywhere in the daemon meanwhile. Otherwise the
// pool leases (and on release, quits) an unrelated app on another device.
func TestDefectHunt_SpawnInstanceNeverAdoptsForeignSession(t *testing.T) {
	m, lf, faddr := dhStart(t)
	t.Cleanup(func() { dhStopBounded(lf) })
	lp, err := m.GetOrCreateListener(AppKey{DeviceID: "phone-1", BundleID: "com.example.other"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dhStopBounded(lp) })
	paddr := fmt.Sprintf("127.0.0.1:%d", lp.Port)

	var (
		mu      sync.Mutex
		foreign *dhApp
		inst    *dhApp
	)
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, a := range []*dhApp{foreign, inst} {
			if a != nil {
				_ = a.conn.Close()
			}
		}
	})
	factoryConn := dhHello(t, faddr, "factory", MethodSpawnInstance)
	ok, _ := PackParams(map[string]any{"ok": true})
	dhServe(factoryConn, func(env *Envelope) *Envelope {
		if env.Method != MethodSpawnInstance {
			return nil
		}
		go func() {
			// Another job's app (re)connects on its own device's listener...
			f, _ := dhDialApp(paddr, "phone-app", MethodPing, MethodQuit)
			// ...while the factory's instance is still booting.
			time.Sleep(500 * time.Millisecond)
			i, _ := dhDialApp(faddr, "instance", MethodPing, MethodQuit)
			mu.Lock()
			foreign, inst = f, i
			mu.Unlock()
		}()
		return &Envelope{ID: env.ID, Result: ok}
	})
	factory := dhWaitSession(t, m, "factory")

	pool := NewInstancePool(m, WithLinger(20*time.Millisecond))
	got, err := pool.Acquire(context.Background(), factory, "tiltbuggy", "alice", 5*time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	gotName := got.HelloInfo().AppName
	pool.Release(got.ID)
	time.Sleep(700 * time.Millisecond)

	mu.Lock()
	f := foreign
	mu.Unlock()
	quitForeign := false
	if f != nil {
	drain:
		for {
			select {
			case meth := <-f.got:
				if meth == MethodQuit {
					quitForeign = true
				}
			default:
				break drain
			}
		}
	}
	if gotName != "instance" || got.Listener() != lf || quitForeign {
		t.Fatalf("Acquire granted session %q from listener %s (factory listener %s, phone listener %s); "+
			"after release the pool sent quit to the unrelated phone app: %v",
			gotName, got.Listener().ID, lf.ID, lp.ID, quitForeign)
	}
}
```

```
--- FAIL: TestDefectHunt_SpawnInstanceNeverAdoptsForeignSession (0.75s)
    Acquire granted session "phone-app" from listener 24a6da0c5af5c92d (factory listener ae54f95a008451f1, …); after release the pool sent quit to the unrelated phone app: true
```

- **Smallest fix.** Accept only sessions whose `Listener()` is the factory's listener, ideally also matching a nonce the instance echoes in its hello.

### DH-22. `app_screenshot` writes only inside the screenshots directory

- **Severity: high (security).** Any peer that completes a hello can make spyder write arbitrary bytes to any path the daemon's user can write, for example `~/Library/LaunchAgents/…`.
- **Mechanism.** The app-reported `format` becomes the file extension in `defaultScreenshotPath`; `filepath.Join` resolves `../` inside it, and `writeOutputFile` creates missing parent directories. The session id is sanitised, but the format is not.
- **Where.** `internal/mcp/app_channel.go:836-848`; `internal/mcp/tools.go:590-594`.
- **Reproduction:**

```go
// app_screenshot's default output path is built from the app-reported
// format. Whatever the app says, the file must land inside the screenshots
// directory: the app (any process that reaches the listener — LAN listeners
// bind 0.0.0.0) must not choose where spyder writes the bytes it sends.
func TestDefectHunt_AppScreenshotStaysInScreenshotsDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dataDir := t.TempDir()
	t.Setenv(paths.DataDirEnv, dataDir)
	h := NewHandlerWithAdapters(&stubAdapter{}, nil)
	mgr := appchannel.NewManager()
	t.Cleanup(mgr.Close)
	h.appChannel = mgr
	l, err := mgr.Start(appchannel.StartParams{Owner: "defecthunt"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Stop)
	_ = dhAppConn(t, l.Port, "evil-game", func(string) any {
		return map[string]any{
			"format": "png/../../escaped.txt",
			"width":  1,
			"height": 1,
			"data":   []byte("bytes chosen by the app"),
		}
	}, appchannel.MethodScreenshotApp)
	sid := dhSessionID(t, mgr)

	res, err := h.handleAppScreenshot(map[string]any{"session_id": sid})
	if err != nil || res == nil || res.IsError {
		t.Fatalf("app_screenshot: %v %+v", err, res)
	}
	escaped := filepath.Join(dataDir, "escaped.txt")
	_, statErr := os.Stat(escaped)
	base := paths.ScreenshotsBase() + string(filepath.Separator)
	text := resultErrorText(t, res)
	if statErr == nil || !strings.Contains(text, base) {
		t.Fatalf("app-reported format made spyder write outside %s (wrote %s: %v):\n%s",
			base, escaped, statErr == nil, text)
	}
}
```

```
--- FAIL: TestDefectHunt_AppScreenshotStaysInScreenshotsDir (0.00s)
    app-reported format made spyder write outside …/002/screenshots/ (wrote …/002/escaped.txt: true)
```

- **Smallest fix.** Allow-list the format (`png`, `jpeg`, `webp`, defaulting to `png`) and check that `filepath.Dir(path) == paths.ScreenshotsBase()`.

### DH-23. jq filters compare msgpack integers by value

- **Severity: high.** `wait_state` predicates on integer fields silently never become true (the wait times out instead of firing), and every `select` or `sort` over integers gives wrong answers.
- **Mechanism.** The same `int64` normalisation makes gojq treat the values as an unknown type and order them by type rank instead of value.
- **Where.** `internal/appchannel/jq.go:143-157`.
- **Reproduction** (`internal/appchannel/zz_defecthunt_session_test.go`):

```go
// msgpack integers must compare, sort and do arithmetic like jq numbers.
// wait_state, app_state/state_query select, capture select and log/perf
// select all go through ApplyJQ.
func TestDefectHunt_JQTreatsIntegersAsNumbers(t *testing.T) {
	cases := []struct {
		expr  string
		input any
		want  any
	}{
		{".hp == 5", map[string]any{"hp": 5}, true},
		{"[.xs[] | select(. > 1)]", map[string]any{"xs": []any{1, 2, 3}}, []any{2, 3}},
		{".xs | sort", map[string]any{"xs": []any{3, 1, 2}}, []any{1, 2, 3}},
		{".hp + 1", map[string]any{"hp": 5}, 6},
	}
	for _, c := range cases {
		raw, _ := PackParams(c.input)
		got, err := ApplyJQ(c.expr, raw)
		if err != nil {
			t.Errorf("ApplyJQ(%q, %v): error %v; want %v", c.expr, c.input, err, c.want)
			continue
		}
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("ApplyJQ(%q, %v) = %v (%s); want %v", c.expr, c.input, got, reflect.TypeOf(got), c.want)
		}
	}
}
```

```
--- FAIL: TestDefectHunt_JQTreatsIntegersAsNumbers (0.00s)
    ApplyJQ(".hp == 5", map[hp:5]) = false (bool); want true
    ApplyJQ("[.xs[] | select(. > 1)]", map[xs:[1 2 3]]) = [] ([]interface {}); want [2 3]
    ApplyJQ(".xs | sort", map[xs:[3 1 2]]) = [3 1 2] ([]interface {}); want [1 2 3]
    ApplyJQ(".hp + 1", map[hp:5]): error jq eval error in ".hp + 1": invalid type: int64 (5); want 6
```

- **Smallest fix.** The same as the previous defect.

### DH-24. Selector `os>=` / `os<=` predicates compare against the version strings adapters actually produce

- **Severity: high.** `reserve --on platform=ios,os>=18` (and `spyder run --on`) picks a device on the wrong OS; `os<=N` never matches a real device.
- **Mechanism.** Adapters set `Info.OS` to `"iOS 17.4"`, `"Android 9"` or `"iOS 18.6 (simulator)"`. `compareVersion` compares segment counts and lengths first, and `"iOS 17"` sorts above `"18"`, so every real device passes `os>=N` and none passes `os<=N`. `selector_test.go` uses `OS: "17.4"`, a format no adapter emits, which is why the tests pass.
- **Where.** `internal/selector/selector.go:230-238`, `310`; producers `internal/device/ios.go:694`, `868`, `internal/device/android.go:93`, `internal/device/sim.go:79`; consumer `internal/mcp/tools.go:1012-1067` (buildCandidates).
- **Reproduction** (`internal/selector/zz_defecthunt_osversion_test.go`):

```go
func TestDefectHunt_OSMinRejectsOlderIOSFromRealAdapterFormat(t *testing.T) {
	candidates := []Candidate{{
		Info: device.Info{UUID: "00008110-001A2C3E0A12801E", Platform: "ios", Model: "iPhone14,5", OS: "iOS 17.4"},
	}}
	info, err := Resolve(Selector{Platform: "ios", OSMin: "18"}, candidates, nil)
	var nm *NoMatchError
	if !errors.As(err, &nm) {
		t.Fatalf("os>=18 resolved to %s running %q (err=%v); want NoMatchError", info.UUID, candidates[0].Info.OS, err)
	}
}

func TestDefectHunt_OSMaxAcceptsOlderIOSFromRealAdapterFormat(t *testing.T) {
	candidates := []Candidate{{
		Info: device.Info{UUID: "00008110-001A2C3E0A12801E", Platform: "ios", Model: "iPhone14,5", OS: "iOS 17.4"},
	}}
	info, err := Resolve(Selector{Platform: "ios", OSMax: "18"}, candidates, nil)
	if err != nil || info.UUID != candidates[0].Info.UUID {
		t.Fatalf("os<=18 against %q: got %q, err=%v; want the iOS 17.4 device", candidates[0].Info.OS, info.UUID, err)
	}
}

func TestDefectHunt_OSMinRejectsOlderAndroidFromRealAdapterFormat(t *testing.T) {
	candidates := []Candidate{{
		Info: device.Info{UUID: "R5CT1234ABC", Platform: "android", Model: "SM-S911B", OS: "Android 9"},
	}}
	info, err := Resolve(Selector{Platform: "android", OSMin: "14"}, candidates, nil)
	var nm *NoMatchError
	if !errors.As(err, &nm) {
		t.Fatalf("os>=14 resolved to %s running %q (err=%v); want NoMatchError", info.UUID, candidates[0].Info.OS, err)
	}
}
```

```
--- FAIL: TestDefectHunt_OSMinRejectsOlderIOSFromRealAdapterFormat (0.00s)
    os>=18 resolved to 00008110-001A2C3E0A12801E running "iOS 17.4" (err=<nil>); want NoMatchError
--- FAIL: TestDefectHunt_OSMaxAcceptsOlderIOSFromRealAdapterFormat (0.00s)
    os<=18 against "iOS 17.4": got "", err=no device matches selector {platform=ios, os_max=18}; …
--- FAIL: TestDefectHunt_OSMinRejectsOlderAndroidFromRealAdapterFormat (0.00s)
    os>=14 resolved to R5CT1234ABC running "Android 9" (err=<nil>); want NoMatchError
```

- **Smallest fix.** Compare only the numeric part, `regexp.MustCompile(`\d+(?:\.\d+)*`).FindString(c.Info.OS)`, and treat a missing version as unknown.

### DH-25. Arguments passed to `adb shell` reach the device shell as single words

- **Severity: high.** A `device_setting` value runs arbitrary commands on the device (`60;reboot` reboots it), contradicting `settings.go`'s "not arbitrary adb shell"; `launch_app` env values with spaces are silently split.
- **Mechanism.** adb joins the words after `shell` with spaces without escaping ("We don't escape here, just like ssh(1)"), and the device's `sh` re-parses the result. The test models that join plus `sh`, with stub functions standing in for device commands.
- **Where.** `internal/device/android_settings.go:79`; `internal/device/android.go:346`; the same class applies to unquoted bundle ids in `pidof`, `force-stop` and `monkey`.
- **Reproduction** (`internal/device/zz_defecthunt_adbshell_test.go`; every device test goes through `dhFakeTools`, which puts fake `adb` and `xcrun` first on `PATH` and fails if `adb` resolves anywhere else):

```go
// device_setting's value goes to `adb shell settings put ...` unquoted.
// settings.go promises the verb is "not arbitrary adb shell", yet a value
// with a shell metacharacter runs a second command on the device.
func TestDefectHunt_DeviceSettingValueIsOneArgvWordOnDevice(t *testing.T) {
	dhFakeTools(t, "")
	var mu sync.Mutex
	var deviceCalls [][]string
	dhHook(t, func(ctx context.Context, args ...string) ([]byte, []byte, error) {
		calls := dhDeviceShell(t, args, "settings", "reboot")
		mu.Lock()
		deviceCalls = append(deviceCalls, calls...)
		mu.Unlock()
		return []byte("60\n"), nil, nil
	})
	const value = "60;reboot"
	if _, err := NewAndroidAdapter().SetSystemSetting("R5CT1234ABC", SettingRefreshRate, value); err != nil {
		t.Fatalf("SetSystemSetting: %v", err)
	}
	for _, c := range deviceCalls {
		if c[0] == "reboot" {
			t.Errorf("device ran %q: the setting value was executed as a command", c)
		}
		if len(c) >= 2 && c[1] == "put" {
			want := []string{"settings", "put", "system", c[3], value}
			if !reflect.DeepEqual(c, want) {
				t.Errorf("device saw settings argv %q, want %q", c, want)
			}
		}
	}
}

// launch_app env values reach `am start --es KEY VALUE` unquoted, so a
// value with a space is split by the device shell and the app gets a
// different extra.
func TestDefectHunt_LaunchAppEnvValueSurvivesDeviceShell(t *testing.T) {
	dhFakeTools(t, "*resolve-activity*) printf 'priority=0 preferredOrder=0 match=0x108000 specificIndex=-1 isDefault=false\\ncom.example.app/.MainActivity\\n'; exit 0;;\n")
	var amCalls [][]string
	dhHook(t, func(ctx context.Context, args ...string) ([]byte, []byte, error) {
		amCalls = append(amCalls, dhDeviceShell(t, args, "am", "monkey")...)
		return []byte("Starting: Intent { cmp=com.example.app/.MainActivity (has extras) }\n"), nil, nil
	})
	const value = "hello world"
	if err := NewAndroidAdapter().LaunchApp("R5CT1234ABC", "com.example.app", map[string]string{"GREETING": value}); err != nil {
		t.Fatalf("LaunchApp: %v", err)
	}
	if len(amCalls) != 1 {
		t.Fatalf("am calls = %q, want one", amCalls)
	}
	argv := amCalls[0]
	for i := 0; i+2 < len(argv); i++ {
		if argv[i] == "--es" && argv[i+1] == "GREETING" {
			if argv[i+2] != value || i+3 != len(argv) {
				t.Fatalf("device am argv = %q; extra GREETING = %q followed by stray %q, want %q", argv, argv[i+2], argv[i+3:], value)
			}
			return
		}
	}
	t.Fatalf("device am argv = %q has no --es GREETING", argv)
}
```

```
--- FAIL: TestDefectHunt_DeviceSettingValueIsOneArgvWordOnDevice (0.05s)
    device saw settings argv ["settings" "put" "system" "peak_refresh_rate" "60"], want […"60;reboot"]
    device ran ["reboot"]: the setting value was executed as a command
--- FAIL: TestDefectHunt_LaunchAppEnvValueSurvivesDeviceShell (0.26s)
    device am argv = [… "--es" "GREETING" "hello" "world"]; extra GREETING = "hello" followed by stray ["world"], want "hello world"
```

- **Smallest fix.** Pass `shellQuote(...)` (already in `android_ui.go:180`) around keys, values and bundle ids; validate refresh-rate values as numeric.

### DH-26. Android `AppPID` says "app not running" only when the app is not running

- **Severity: high.** `terminate_app` reports success while the app is still running, and `is_running` answers wrong, whenever adb hiccups or the call hits its deadline.
- **Mechanism.** Any adb error that is not "device not connected" becomes `app not running: <pkg>`, including an adb server fault and the deadline kill (`signal: killed`). `handleTerminateApp` treats that prefix as proof the app has exited.
- **Where.** `internal/device/android.go:857-859`; `internal/mcp/tools.go:909`.
- **Reproduction** (`internal/device/zz_defecthunt_adberrors_test.go`):

```go
// AppPID maps every adb failure that is not "device not connected" to
// "app not running". handleTerminateApp treats that prefix as proof the
// kill worked, and is_running reports not_running. A transport failure or
// a deadline says nothing about the app.
func TestDefectHunt_AndroidAppPIDTransportFailureIsNotAppNotRunning(t *testing.T) {
	dhFakeTools(t, "")
	cases := map[string]androidAdbHook{
		"adb server protocol fault": func(ctx context.Context, args ...string) ([]byte, []byte, error) {
			return nil, []byte("error: protocol fault (couldn't read status): Connection reset by peer\n"), errors.New("exit status 1")
		},
		"deadline while adb blocks": func(ctx context.Context, args ...string) ([]byte, []byte, error) {
			<-ctx.Done()
			return nil, nil, errors.New("signal: killed")
		},
	}
	for name, hook := range cases {
		t.Run(name, func(t *testing.T) {
			dhHook(t, hook)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_, err := NewAndroidAdapter().AppPIDContext(ctx, "R5CT1234ABC", "com.example.app")
			if err == nil {
				t.Fatal("AppPIDContext succeeded on a failed adb call")
			}
			if strings.HasPrefix(err.Error(), "app not running") {
				t.Fatalf("AppPIDContext reported %q for a %s; callers take that as proof the app is gone", err, name)
			}
		})
	}
}
```

```
--- FAIL: TestDefectHunt_AndroidAppPIDTransportFailureIsNotAppNotRunning (0.11s)
    AppPIDContext reported "app not running: com.example.app" for a deadline while adb blocks; callers take that as proof the app is gone
    AppPIDContext reported "app not running: com.example.app" for a adb server protocol fault; …
```

- **Smallest fix.** Return `ctx.Err()` when set; report not-running only for an `*exec.ExitError` with code 1 and empty output; otherwise return the real error.

### DH-27. Android `State` and `List` calls to adb have a deadline

- **Severity: high.** One wedged device or a hung adb server stops the battery sampler for the whole fleet, and with it the lab power policy (drain trend, recharge holds, idle stops); `reserve` with a selector hangs too, holding `h.mu` (see the handler-lock finding).
- **Mechanism.** `androidAdbBG` passes `context.Background()`, so `State`'s three adb calls have no deadline; `List` runs `exec.Command("adb", "devices", "-l")` with no context. The sampler calls `List`, then `State` for each device, on one goroutine.
- **Where.** `internal/device/android_fps.go:55-57`; `internal/device/android.go:54`, `102`; `internal/mcp/battery.go:166-216`.
- **Reproduction:**

```go
// Every adb call State makes runs under context.Background(): a wedged
// `adb shell dumpsys battery` blocks State forever, and the battery
// sampler (internal/mcp/battery.go → battery.Collect) calls State for
// every device in turn on one goroutine, so one wedged device stops
// battery history and the lab power policy for the whole fleet.
func TestDefectHunt_AndroidStateAdbCallsHaveDeadline(t *testing.T) {
	dhFakeTools(t, "")
	var mu sync.Mutex
	var unbounded []string
	dhHook(t, func(ctx context.Context, args ...string) ([]byte, []byte, error) {
		if _, ok := ctx.Deadline(); !ok {
			mu.Lock()
			unbounded = append(unbounded, strings.Join(args, " "))
			mu.Unlock()
		}
		return []byte("  level: 50\n  AC powered: true\n"), nil, nil
	})
	if _, err := NewAndroidAdapter().State("R5CT1234ABC"); err != nil {
		t.Fatalf("State: %v", err)
	}
	if len(unbounded) > 0 {
		t.Fatalf("State ran %d adb calls with no deadline, e.g. %q", len(unbounded), truncate(unbounded[0], 80))
	}
}

// AndroidAdapter.List runs `adb devices -l` with exec.Command (no
// context). A hung adb server hangs List, and the battery sampler and
// selector reservations with it.
func TestDefectHunt_AndroidListReturnsWhenAdbHangs(t *testing.T) {
	dhFakeTools(t, "devices*) exec sleep 20;;\n")
	done := make(chan struct{})
	go func() {
		_, _ = NewAndroidAdapter().List()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("List still blocked 5s after `adb devices -l` hung (fake adb sleeps 20s); there is no deadline")
	}
}
```

```
--- FAIL: TestDefectHunt_AndroidStateAdbCallsHaveDeadline (0.00s)
    State ran 3 adb calls with no deadline, e.g. "-s R5CT1234ABC shell dumpsys battery"
--- FAIL: TestDefectHunt_AndroidListReturnsWhenAdbHangs (5.02s)
    List still blocked 5s after `adb devices -l` hung (fake adb sleeps 20s); there is no deadline
```

- **Smallest fix.** Give `androidAdbBG`, `List` and `getprop` a bounded `context.WithTimeout` (for example 30 s).

### DH-28. Releasing a device never stops the next holder's app

- **Severity: medium.** It interferes across jobs: job A's release can kill the game that job B launched on the same device immediately afterwards (a queue of jobs running the same app makes this the normal sequence), and it also deletes B's launch record so the idle policy stops tracking B's app.
- **Mechanism.** `handleRelease` deletes the reservation first and then starts `go h.stopApps(id, bundles, …)` for the apps spyder launched there. `stopApps` terminates by bundle id with no check of who holds the device now or when the app was launched, and calls `h.launched.Forget`.
- **Where.** `internal/mcp/tools.go:1195`, `1236-1249`; `internal/mcp/power_policy.go:266-311`.
- **Reproduction** (the stub's 100 ms terminate stands for the kill request in flight to the device):

```go
// release frees the reservation and then stops the apps spyder launched
// on the device in a background goroutine. Nothing ties that stop to
// the released lease: the next holder's launch of the same app can be
// killed by it.
func TestDefectHunt_ReleaseStopKillsNextHoldersApp(t *testing.T) {
	var mu sync.Mutex
	running := map[string]bool{}
	ios := &stubAdapter{
		launchApp: func(_, bundle string, _ map[string]string) error {
			mu.Lock()
			running[bundle] = true
			mu.Unlock()
			return nil
		},
		terminateApp: func(_, bundle string) error {
			time.Sleep(100 * time.Millisecond) // kill request in flight to the device
			mu.Lock()
			running[bundle] = false
			mu.Unlock()
			return nil
		},
		appPID: func(_, bundle string) (int, error) {
			mu.Lock()
			defer mu.Unlock()
			if running[bundle] {
				return 4242, nil
			}
			return 0, errors.New("app not running")
		},
	}
	h, _ := defectHuntHandler(t, ios)
	resv, err := reservations.New("")
	if err != nil {
		t.Fatal(err)
	}
	h.reservations = resv
	call := func(name string, args map[string]any) {
		t.Helper()
		res, err := h.Dispatch(context.Background(), name, args)
		if err != nil || res == nil || res.IsError {
			t.Fatalf("%s: %v %+v", name, err, res)
		}
	}
	call("reserve", map[string]any{"device": "iPad", "owner": "job-A"})
	call("launch_app", map[string]any{"device": "iPad", "owner": "job-A", "bundle_id": "com.example.game"})
	call("release", map[string]any{"device": "iPad", "owner": "job-A"})
	// job-B was waiting for the device and starts at once.
	call("reserve", map[string]any{"device": "iPad", "owner": "job-B"})
	call("launch_app", map[string]any{"device": "iPad", "owner": "job-B", "bundle_id": "com.example.game"})

	time.Sleep(500 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if !running["com.example.game"] {
		t.Errorf("job-B holds iPad and launched com.example.game, but job-A's release stopped it")
	}
}
```

```
INFO power: stopped apps device=00008103-001122334455667A reason="lease released by job-A" stopped=[com.example.game]
--- FAIL: TestDefectHunt_ReleaseStopKillsNextHoldersApp (0.50s)
    job-B holds iPad and launched com.example.game, but job-A's release stopped it
```

- **Smallest fix.** Stop the apps synchronously in `handleRelease` before calling `reservations.Release` (bounded by `stopAppsTimeout`), so the device is not free until the stop has finished. Also skip any bundle whose launch record is newer than the release.

### DH-29. The idle policy never stops an app on a device that is reserved

- **Severity: medium.** For an iOS device whose inventory entry has only `ios_coredevice`, the idle sweep stops the holder's app 10 minutes after it was launched, in the middle of the holder's run.
- **Mechanism.** Launched apps are recorded under `resolveAdapter`'s id, which for such an entry is the CoreDevice id. Activity (`sweepIdle`'s touch for every live reservation, and `beforeVerb` for every verb) goes through `deviceIDForRef`, which only knows `android_serial` and `ios_uuid` and otherwise returns the alias. The touch lands on a different key, so the device never looks active.
- **Where.** `internal/mcp/power_policy.go:67-82` (deviceIDForRef), `92-93`, `247-262` (sweepIdle); `internal/mcp/tools.go:1489-1494` (resolveAdapter's CoreDevice fallback); `power_policy.go:57-63` (recordLaunch).
- **Reproduction** (the `ios_uuid` sub-test is the control and passes):

```go
// The idle policy keys launched apps by resolveAdapter's id, but marks
// devices active through deviceIDForRef, which knows only android_serial
// and ios_uuid. For an iOS entry that has only ios_coredevice the two
// differ, so a device that is reserved (and in use) never looks active
// and the idle sweep stops the holder's app 10 minutes after launch.
func TestDefectHunt_IdleSweepStopsAppOnReservedCoreDeviceOnlyIPhone(t *testing.T) {
	for _, tc := range []struct {
		name, entry string
	}{
		{"ios_uuid entry (control)", `{"alias":"iPhone","platform":"ios","ios_uuid":"00008110-000A1B2C3D4E5F60"}`},
		{"ios_coredevice-only entry", `{"alias":"iPhone","platform":"ios","ios_coredevice":"11111111-2222-3333-4444-555555555555"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(paths.DataDirEnv, t.TempDir())
			t.Setenv("HOME", t.TempDir())
			if err := os.WriteFile(paths.InventoryPath(), []byte("["+tc.entry+"]"), 0o600); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			terminated := 0
			ios := &stubAdapter{
				terminateApp: func(string, string) error { mu.Lock(); terminated++; mu.Unlock(); return nil },
				appPID:       func(string, string) (int, error) { return 0, errors.New("app not running") },
			}
			h := NewHandlerWithAdapters(ios, &stubAdapter{})
			h.recordings = recording.NewRegistry()
			h.networkByDevice = map[string]appliedNetwork{}
			resv, _ := reservations.New("", reservations.WithNormalizer(func(ref string) string {
				if e, ok := h.inventory.Lookup(ref); ok && e.Alias != "" {
					return e.Alias
				}
				return ref
			}))
			h.reservations = resv
			for _, c := range []struct {
				name string
				args map[string]any
			}{
				{"reserve", map[string]any{"device": "iPhone", "owner": "job-A"}},
				{"launch_app", map[string]any{"device": "iPhone", "owner": "job-A", "bundle_id": "com.example.game"}},
			} {
				if res, err := h.Dispatch(context.Background(), c.name, c.args); err != nil || res.IsError {
					t.Fatalf("%s: %v %+v", c.name, err, res)
				}
			}
			// The battery tick 11 minutes later; job-A still holds the device.
			h.sweepIdle(time.Now().Add(idleStopAfter + time.Minute))
			time.Sleep(300 * time.Millisecond)
			mu.Lock()
			defer mu.Unlock()
			if terminated > 0 {
				t.Errorf("idle sweep stopped job-A's app although job-A holds iPhone")
			}
		})
	}
}
```

```
--- FAIL: TestDefectHunt_IdleSweepStopsAppOnReservedCoreDeviceOnlyIPhone (0.61s)
    --- PASS: …/ios_uuid_entry_(control) (0.31s)
    --- FAIL: …/ios_coredevice-only_entry (0.30s)
        idle sweep stopped job-A's app although job-A holds iPhone
```

- **Smallest fix.** Make `deviceIDForRef` return exactly what `resolveAdapter` returns (fall back to `IOSCoreDevice` when `IOSUUID` is empty), or have both call one helper.

### DH-30. `ensure_session` never closes another owner's app-channel session

- **Severity: medium.** A caller who does not hold the device can kill the holder's app channel, for example while a Unity app is busy loading and misses one 2 s ping. The caller is then refused, but the holder's session is already gone.
- **Mechanism.** The "fast path" pings an existing session and, if the ping fails, calls `s.Close()`. That happens before the `authorize` check in the deploy/launch path, although the file comment says the fast path "mutates nothing, so it is not reservation-gated".
- **Where.** `internal/mcp/ensure_session.go:114-133` (close at `131`), authorize at `138`.
- **Reproduction:**

```go
// ensure_session's "fast path" pings an existing session and, if the
// ping fails, closes that session — before authorize(). A caller who
// does not hold the device can therefore kill the holder's app channel
// (e.g. while a Unity app is busy loading and misses one ping).
func TestDefectHunt_EnsureSessionClosesHoldersSessionBeforeAuthorize(t *testing.T) {
	h, _ := defectHuntHandler(t, &stubAdapter{})
	h.appChannel = appchannel.NewManager()
	t.Cleanup(h.appChannel.Close)
	resv, _ := reservations.New("")
	h.reservations = resv
	if _, err := resv.Acquire("iPad", "job-A", time.Hour, ""); err != nil {
		t.Fatal(err)
	}

	key := appchannel.AppKey{DeviceID: "00008103-001122334455667A", BundleID: "com.example.app"}
	l, err := h.appChannel.GetOrCreateListener(key)
	if err != nil {
		t.Fatal(err)
	}
	// job-A's app: completes hello, then is busy and does not answer pings.
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", l.Port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	hello, _ := appchannel.PackParams(appchannel.Hello{AppName: "game", AppVersion: "1",
		Methods: appchannel.MethodDescriptors(appchannel.MethodPing)})
	if err := appchannel.WriteFrame(conn, &appchannel.Envelope{ID: 1, Method: appchannel.MethodHello, Params: hello}); err != nil {
		t.Fatal(err)
	}
	if _, err := appchannel.ReadFrame(conn); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(l.Sessions()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(l.Sessions()) == 0 {
		t.Fatal("no session")
	}

	res, _ := h.Dispatch(context.Background(), "ensure_session", map[string]any{
		"device": "iPad", "bundle_id": "com.example.app", "owner": "job-B",
	})
	if res == nil || !res.IsError {
		t.Fatalf("job-B should be refused: %+v", res)
	}
	t.Logf("job-B saw: %s", resultErrorText(t, res))
	time.Sleep(100 * time.Millisecond)
	if len(l.Sessions()) == 0 {
		t.Errorf("job-B was refused, but job-A's app-channel session was closed anyway")
	}
}
```

```
--- FAIL: TestDefectHunt_EnsureSessionClosesHoldersSessionBeforeAuthorize (2.11s)
    job-B saw: device iPad is reserved by job-A until 2026-11-20T15:36:40+11:00
    job-B was refused, but job-A's app-channel session was closed anyway
```

- **Smallest fix.** Move the `authorize` call above the fast path's `s.Close()` (return the conflict if it fails), or only close when the caller is authorised.

### DH-31. A released device never keeps the network profile its holder applied

- **Severity: medium.** The next holder inherits an `offline` or throttled device and sees failures unrelated to their own code.
- **Mechanism.** `handleNetwork` records the profile under the raw device string the caller passed; `handleRelease` looks it up under the raw string passed to release. The reservation is keyed by the canonical alias, so applying with `Raspberry` and releasing with `R5CR112X76K` (or the reverse) releases the lease and skips the clear. The map is also in memory only (lost on restart), and `spyder run` releases without ever reaching this code.
- **Where.** `internal/mcp/tools.go:1217-1219`, `1302`, `1318`.
- **Reproduction:**

```go
// release clears a network profile the owner applied — but it looks the
// profile up by the raw device string the caller passed, while the
// reservation itself is keyed by the canonical alias. Apply with the
// alias, release with the serial (or vice versa), and the next holder
// inherits a throttled/offline device.
func TestDefectHunt_ReleaseBySerialLeavesNetworkProfileApplied(t *testing.T) {
	cleared := 0
	android := &stubAdapter{clearNetwork: func(string) error { cleared++; return nil }}
	h, _ := defectHuntHandler(t, &stubAdapter{})
	h.android = android
	resv, _ := reservations.New("", reservations.WithNormalizer(func(ref string) string {
		if e, ok := h.inventory.Lookup(ref); ok && e.Alias != "" {
			return e.Alias
		}
		return ref
	}))
	h.reservations = resv
	call := func(name string, args map[string]any) string {
		t.Helper()
		res, err := h.Dispatch(context.Background(), name, args)
		if err != nil || res == nil || res.IsError {
			t.Fatalf("%s: %v %+v", name, err, res)
		}
		return fmt.Sprint(res.Content)
	}
	call("reserve", map[string]any{"device": "Raspberry", "owner": "job-A"})
	call("network", map[string]any{"device": "Raspberry", "owner": "job-A", "profile": "offline"})
	out := call("release", map[string]any{"device": "R5CR112X76K", "owner": "job-A"})
	if _, held := resv.Get("Raspberry"); held {
		t.Fatalf("release by serial did not release")
	}
	if cleared == 0 {
		t.Errorf("reservation released (%s) but the 'offline' profile was never cleared", out)
	}
}
```

```
--- FAIL: TestDefectHunt_ReleaseBySerialLeavesNetworkProfileApplied (0.00s)
    reservation released ([… released reservation on R5CR112X76K]) but the 'offline' profile was never cleared
```

- **Smallest fix.** Key `networkByDevice` by `h.canonicalDevice(dev)` in both places. For restarts, persist it next to the reservation book or clear any profile when a reservation is acquired.

### DH-32. `reserve` never reports success for a hold it could not persist

- **Severity: medium.** After a failed write (full disk, I/O error, permissions), the next restart forgets a lease the holder was told it had, and the device can be granted to someone else.
- **Mechanism.** Every mutation calls `_ = s.saveLocked()` and discards the error, so `Acquire`, `Renew` and `Release` report success while the file is unchanged.
- **Where.** `internal/reservations/reservations.go:156, 173, 195, 205, 235, 258, 287, 318`.
- **Reproduction:**

```go
// saveLocked's error is discarded (`_ = s.saveLocked()`). When the write
// fails, Acquire still reports success, the hold exists only in memory,
// and the next daemon start has no record of it.
func TestDefectHunt_UnpersistedAcquireReportsSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reservations.json")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil { // stands in for ENOSPC / EIO
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	_, acqErr := s.Acquire("iPad", "job-A", DefaultTTL, "")

	restarted, err := New(path) // daemon restart
	if err != nil {
		t.Fatal(err)
	}
	if _, held := restarted.Get("iPad"); !held && acqErr == nil {
		t.Errorf("Acquire returned success, but after a restart iPad is free (the hold was never written)")
	}
}
```

```
--- FAIL: TestDefectHunt_UnpersistedAcquireReportsSuccess (0.00s)
    Acquire returned success, but after a restart iPad is free (the hold was never written)
```

- **Smallest fix.** In `Acquire` and `Renew`, save first and on error restore the previous map entry and return the error; at minimum log it and mark health.

### DH-33. A `sim_renew` that reports success leaves the lease held

- **Severity: medium.** A job is told "renewed" and then loses its simulator; last-moment renewals are exactly when this fires.
- **Mechanism.** `Reap` picks due leases and `release` runs the slow simctl undo with no "releasing" marker and no re-check of expiry. `Renew`, `Touch` and same-owner re-acquire all succeed during the undo, and the lease is deleted anyway.
- **Where.** `internal/simlease/simlease.go:684-697`, `557-588`, `628-645`.
- **Reproduction:**

```go
// A Renew that reports success must leave the lease held. Here the
// reaper has already picked the (just-expired) lease and is undoing it
// when the owner renews.
func TestDefectHunt_RenewDuringReapIsNotSilentlyLost(t *testing.T) {
	f := &fakeSimctl{}
	f.add("AAAAAAAA-0000-4000-8000-000000000001", "shut", iphone16, runtime18, "Shutdown")
	d := &dhSimctl{fakeSimctl: f}
	var nowNS atomic.Int64
	nowNS.Store(time.Unix(1_800_000_000, 0).UnixNano())
	now := func() time.Time { return time.Unix(0, nowNS.Load()) }
	m := dhManager(t, d, now, "")

	l, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "a", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	nowNS.Add(int64(61 * time.Second)) // just past expiry

	inUndo := make(chan struct{})
	proceed := make(chan struct{})
	var once sync.Once
	d.hookMu.Lock()
	d.beforeList = func() {
		once.Do(func() {
			close(inUndo)
			<-proceed
		})
	}
	d.hookMu.Unlock()

	done := make(chan []Released)
	go func() { done <- m.Reap(ctx) }()
	<-inUndo

	renewed, renewErr := m.Renew(l.ID, "a", 10*time.Minute)
	close(proceed)
	<-done

	if renewErr != nil {
		return // refusing the renewal would be correct
	}
	held := false
	for _, x := range m.Status(ctx).Leases {
		if x.ID == l.ID {
			held = true
		}
	}
	if !held || f.state(l.UDID) != "Booted" {
		t.Fatalf("Renew returned success (expires %s) but the lease is held=%v and the simulator is %q",
			renewed.ExpiresAt, held, f.state(l.UDID))
	}
}
```

```
--- FAIL: TestDefectHunt_RenewDuringReapIsNotSilentlyLost (0.00s)
    Renew returned success (expires 2027-01-15 19:11:01 +1100 AEDT) but the lease is held=false and the simulator is "Shutdown"
```

- **Smallest fix.** Mark the lease `Releasing` under the lock in `release`; `Renew`, `Touch` and re-acquire refuse or skip such a lease, and a reap re-checks expiry under the lock before undoing.

### DH-34. `sim_acquire` only returns a lease on a booted simulator

- **Severity: medium.** The returned UDID can be shut down, or deleted if the previous lease had created it.
- **Mechanism.** `Acquire` runs `simctl list` outside the lock and picks candidates from that snapshot later. If another lease's release finishes in between, the simulator is granted as `already_running` and never booted.
- **Where.** `internal/simlease/simlease.go:323`, `333-350`.
- **Reproduction:**

```go
// sim_acquire must only return a lease on a simulator that is booted.
// Owner b's acquire lists simulators while owner a still holds (and is
// releasing) the only match; a's release finishes before b takes the
// lock, so b's view of the simulator's state is stale.
func TestDefectHunt_AcquireNeverGrantsAShutDownSimulator(t *testing.T) {
	f := &fakeSimctl{}
	f.add("AAAAAAAA-0000-4000-8000-000000000001", "shut", iphone16, runtime18, "Shutdown")
	d := &dhSimctl{fakeSimctl: f}
	m := dhManager(t, d, fixedNow(), "")

	a, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Action != ActionBooted {
		t.Fatalf("setup: action %s", a.Action)
	}

	d.hookMu.Lock()
	d.afterList = func() {
		if _, err := m.Release(context.Background(), a.ID, "a"); err != nil {
			t.Errorf("release: %v", err)
		}
	}
	d.hookMu.Unlock()

	b, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "b"})
	if err != nil {
		return // failing the acquire would be acceptable
	}
	if got := f.state(b.UDID); got != "Booted" {
		t.Fatalf("acquire returned lease %s (action %s) on %s, but that simulator is %q",
			b.ID, b.Action, b.UDID, got)
	}
}
```

```
--- FAIL: TestDefectHunt_AcquireNeverGrantsAShutDownSimulator (0.00s)
    acquire returned lease abecd18cb35659dd (action already_running) on AAAAAAAA-…-000000000001, but that simulator is "Shutdown"
```

- **Smallest fix.** Keep a release generation counter and re-list when it changed between the list and the lock, or confirm the state with a fresh list before recording `already_running`.

### DH-35. A simulator lease that `sim_acquire` reports as granted survives a restart

- **Severity: medium.** On a full disk or a permission error, a simulator that spyder created and booted is never shut down or deleted after the next restart.
- **Mechanism.** `mustPersistLocked` logs and swallows write errors, including the "record before side effect" write; `Acquire` then creates and boots the simulator and returns success.
- **Where.** `internal/simlease/simlease.go:236-240`, called at `381`, `401`, `416`, `579`.
- **Reproduction:**

```go
// A lease Acquire reports as granted must survive a daemon restart, or
// the simulator it booted is never shut down. Here the lease file cannot
// be written (read-only directory; a full disk behaves the same).
func TestDefectHunt_GrantedLeaseSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sim-leases.json")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	f := &fakeSimctl{}
	m1 := dhManager(t, f, fixedNow(), path)
	l, err := m1.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "a"})
	if err != nil {
		return // refusing the lease would be correct
	}

	// Daemon restarts.
	m2 := dhManager(t, f, fixedNow(), path)
	if n := len(m2.Status(ctx).Leases); n != 1 {
		t.Fatalf("Acquire returned lease %s (%s, created+booted) but a restarted daemon knows %d leases; simulator state %q will never be undone",
			l.ID, l.UDID, n, f.state(l.UDID))
	}
}
```

```
--- FAIL: TestDefectHunt_GrantedLeaseSurvivesRestart (0.01s)
    Acquire returned lease 1e026849d2096dce (00000000-…-000000000001, created+booted) but a restarted daemon knows 0 leases; simulator state "Booted" will never be undone
```

- **Smallest fix.** Make the persist at `381` fatal to `Acquire` (drop the record and return the error before any simctl call), and return persist errors from `Release`.

### DH-36. A damaged provenance file never disables simulator leasing

- **Severity: medium.** A torn write of a purely informational file turns off simulator leasing and the reaper; the daemon keeps serving with only a log line, and earlier leases are never undone.
- **Mechanism.** A parse error in the provenance file (or the lease file) makes `simlease.New` fail; `daemon.Build` logs "simulator leases unavailable" and runs without a lease book. Neither writer fsyncs before rename, so a zero-length file after power loss is plausible.
- **Where.** `internal/simlease/provenance.go:58-61` → `internal/simlease/simlease.go:200-202` (lease file `191-193`) → `internal/daemon/daemon.go:441-442`; writers `simlease.go:221-224`, `provenance.go:86-87`.
- **Reproduction:**

```go
// Provenance is informational; a damaged provenance file must not take
// the lease book (and its reaper) down with it.
func TestDefectHunt_DamagedProvenanceDoesNotDisableLeases(t *testing.T) {
	dir := t.TempDir()
	leasePath := filepath.Join(dir, "sim-leases.json")
	provPath := filepath.Join(dir, "sim-provenance.json")
	// A lease left by the previous daemon that must be reaped.
	m0 := dhManager(t, &fakeSimctl{}, fixedNow(), leasePath)
	m0.mu.Lock()
	m0.leases["x"] = &Lease{ID: "x", UDID: "AAAAAAAA-0000-4000-8000-000000000001", Owner: "a", Action: ActionBooted, ExpiresAt: fixedNow()()}
	m0.mustPersistLocked()
	m0.mu.Unlock()
	if err := os.WriteFile(provPath, []byte(""), 0o644); err != nil { // torn write after power loss
		t.Fatal(err)
	}
	if _, err := New(&Args{Simctl: &fakeSimctl{}, Path: leasePath, ProvenancePath: provPath, Now: fixedNow()}); err != nil {
		t.Fatalf("empty provenance file disables simulator leases entirely (daemon logs and serves without a lease book or reaper): %v", err)
	}
}
```

```
--- FAIL: TestDefectHunt_DamagedProvenanceDoesNotDisableLeases (0.00s)
    empty provenance file disables simulator leases entirely (daemon logs and serves without a lease book or reaper): simlease: parse …/sim-provenance.json: unexpected end of JSON input
```

- **Smallest fix.** On a provenance parse error, rename the file to `.corrupt-<ts>` and continue; quarantine a corrupt lease file loudly instead of disabling the subsystem; fsync the temp file and directory around the rename.

### DH-37. The tunnel the daemon has just started is never killed and respawned by `Supervise`

- **Severity: medium.** Every daemon start, and every tunnel takeover, tears down the iOS tunnel moments after starting it, so device sessions opened in that window break and the tunnel runs a second startup.
- **Mechanism.** `daemon.Run` calls `tunnelSup.Start` and then passes the same supervisor to `health.Supervise`. `Supervise` begins with `startWithRecovery` → `proc.Start`, and `iostunnel.Start` on a running child calls `Stop` (SIGTERM, up to 3 s) and spawns a new tunnel.
- **Where.** `internal/daemon/daemon.go:138` and `198` (takeover: `128` and `133`); `internal/health/supervise.go:94`; `internal/iostunnel/supervisor.go:113-119`.
- **Reproduction** (`internal/iostunnel/zz_defecthunt_lifecycle_test.go`; the fake `ios` is a shell script in a temp dir and the registry is a stub HTTP server on a random port):

```go
// defectHuntEnv isolates paths.Base() and gives the supervisor a stub
// registry so nothing reaches the real tunnel on :60105.
func defectHuntEnv(t *testing.T) (dir string, port int) {
	t.Helper()
	dir = t.TempDir()
	t.Setenv("SPYDER_DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("HOME", filepath.Join(dir, "home"))
	mux := http.NewServeMux()
	mux.HandleFunc("/tunnels", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("[]")) })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	t.Cleanup(func() { _ = exec.Command("pkill", "-KILL", "-f", dir).Run() })
	return dir, ln.Addr().(*net.TCPAddr).Port
}

// The daemon starts the tunnel (daemon.go:138, and :128 on takeover) and
// then hands the same Supervisor to health.Supervise (daemon.go:198 /
// :133). Supervise's first action is proc.Start, and Start on a running
// tunnel SIGTERMs it and spawns another. A tunnel the daemon just started
// must survive being put under supervision.
func TestDefectHunt_SuperviseKeepsAlreadyStartedTunnel(t *testing.T) {
	dir, port := defectHuntEnv(t)
	pidsPath := filepath.Join(dir, "pids")
	bin := filepath.Join(dir, "fake-ios")
	script := "#!/bin/sh\necho $$ >> " + pidsPath + "\ntrap 'exit 0' TERM\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	sup := New(bin, "127.0.0.1", port)
	t.Cleanup(func() { cancel(); _ = sup.Stop(context.Background()) })

	// daemon.go:138
	if err := sup.Start(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(readPIDs(t, pidsPath)) < 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	first := readPIDs(t, pidsPath)
	if len(first) != 1 {
		t.Fatalf("fake tunnel did not start: %v", first)
	}

	// daemon.go:198 — production policy and interval.
	go health.NewSupervisor(health.New()).Supervise(ctx, sup, health.Policy{MaxAttempts: 5, BaseBackoff: 2 * time.Second}, 10*time.Second)

	time.Sleep(5 * time.Second) // well inside the first 10 s probe interval
	pids := readPIDs(t, pidsPath)
	if len(pids) != 1 || !alive(first[0]) {
		t.Fatalf("putting a running tunnel under Supervise restarted it: spawned pids %v, first pid %d alive=%v", pids, first[0], alive(first[0]))
	}
}
```

```
--- FAIL: TestDefectHunt_SuperviseKeepsAlreadyStartedTunnel (5.29s)
    putting a running tunnel under Supervise restarted it: spawned pids [78458 78588], first pid 78458 alive=false
```

- **Smallest fix.** Do not pre-start the tunnel in `daemon.Run`; let `Supervise` perform the first start. Alternatively, have `Supervise` skip its initial `Start` when `proc.Alive()` is already true.

### DH-38. A tunnel `Stop` that returns success means the tunnel has exited

- **Severity: medium.** A tunnel slow to honour SIGTERM (the wedged 🎯T84 state) outlives the daemon in its own process group, still holding `:60105`, and the next daemon cannot start its own tunnel.
- **Mechanism.** `Stop` sets `s.cmd = nil` before it waits. At shutdown two `Stop`s run at once (from `Supervise`'s `ctx.Done` branch and from `daemon.Run`); the second sees `nil` and returns success immediately. `Run` then returns and the process exits before the first `Stop`'s 3 s SIGTERM-then-SIGKILL fallback fires.
- **Where.** `internal/iostunnel/supervisor.go:191-230`; `internal/health/supervise.go:101-104`; `internal/daemon/daemon.go:249-257`.
- **Reproduction:**

```go
// On shutdown two Stops run at once: health.Supervise's ctx.Done branch
// (supervise.go:103) and the daemon's own tunnelSup.Stop (daemon.go:254).
// Stop swaps s.cmd to nil before waiting, so whichever comes second
// returns nil at once while the first is still inside its 3 s
// SIGTERM-then-SIGKILL wait. The daemon then finishes Run and exits,
// cutting that wait short: a tunnel slow to honour SIGTERM (the wedged
// 🎯T84 state) is never SIGKILLed and, being in its own process group,
// outlives the daemon still holding :60105. A Stop that returns success
// must mean the child is gone.
func TestDefectHunt_ConcurrentStopWaitsForExit(t *testing.T) {
	dir, port := defectHuntEnv(t)
	pidsPath := filepath.Join(dir, "pids")
	bin := filepath.Join(dir, "fake-ios")
	script := "#!/bin/sh\necho $$ >> " + pidsPath + "\ntrap '' TERM\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	sup := New(bin, "127.0.0.1", port)
	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(readPIDs(t, pidsPath)) < 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	pids := readPIDs(t, pidsPath)
	if len(pids) != 1 {
		t.Fatal("fake tunnel did not start")
	}

	firstDone := make(chan struct{})
	go func() { _ = sup.Stop(context.Background()); close(firstDone) }() // Supervise's Stop
	time.Sleep(200 * time.Millisecond)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := sup.Stop(shutdownCtx) // daemon.go:254
	stillAlive := alive(pids[0])
	<-firstDone
	if err == nil && stillAlive {
		t.Fatalf("second Stop returned success while tunnel pid %d was still running", pids[0])
	}
}
```

```
--- FAIL: TestDefectHunt_ConcurrentStopWaitsForExit (3.28s)
    second Stop returned success while tunnel pid 78762 was still running
```

- **Smallest fix.** Give each child a `done` channel closed by the reaper goroutine in `startLocked`, and have every `Stop` wait on it (with the SIGKILL fallback) rather than returning when `cmd` is nil.

### DH-39. A failed runwda attempt never leaves its `ios runwda` child running

- **Severity: medium.** After the last allowed attempt fails, an `ios runwda` stays up holding the device's single XCTest session, and nothing stops it, shutdown included; UI automation on that device fails until someone kills it by hand.
- **Mechanism.** `procRunner.Start` returns an error as soon as the child prints a startup-failure marker, or when its 25 s timeout passes, without stopping the child.
- **Where.** `internal/device/runwda.go:749-762`.
- **Reproduction** (`internal/device/zz_defecthunt_runwda_test.go`; the fake `ios` prints a failure marker and takes 2 s to exit on SIGTERM):

```go
// fakeRunwdaBin is an `ios` stand-in whose runwda fails the way a real
// one does on a consent/stream error: it prints a startup-failure marker
// and stays up. On SIGTERM it takes two seconds to tear down, as go-ios
// runwda does while it closes the XCTest session.
func fakeRunwdaBin(t *testing.T) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	t.Setenv("SPYDER_DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("HOME", filepath.Join(dir, "home"))
	bin = filepath.Join(dir, "ios")
	script := "#!/bin/sh\ntrap '/bin/sleep 2; exit 0' TERM\necho 'runwda: ERROR stream_closed'\nwhile :; do /bin/sleep 1; done\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("/usr/bin/pkill", "-KILL", "-f", bin).Run() })
	return bin, dir
}

func liveFakes(bin string) int {
	out, _ := exec.Command("/usr/bin/pgrep", "-f", bin).Output()
	return len(strings.Fields(string(out)))
}

// procRunner.Start returns an error as soon as the child prints a
// startup-failure marker, but leaves that child running. The reappear
// loop stops after runwdaMaxAttempts, so the last failed `ios runwda`
// stays up indefinitely, holding the device's XCTest session, and
// nothing (shutdown included) ever stops it.
func TestDefectHunt_RunwdaFailedAttemptLeavesNoChild(t *testing.T) {
	bin, _ := fakeRunwdaBin(t)
	p := newProcRunner(bin)
	_, err := p.Start("DEFECTHUNT-UDID", "com.example.runner", nil)
	if err == nil {
		t.Fatal("fake runwda should fail")
	}
	time.Sleep(3 * time.Second) // longer than the fake's teardown
	if n := liveFakes(bin); n != 0 {
		t.Fatalf("Start reported failure (%v) but %d runwda child(ren) are still running", err, n)
	}
}
```

```
--- FAIL: TestDefectHunt_RunwdaFailedAttemptLeavesNoChild (3.47s)
    Start reported failure (runwda: ERROR stream_closed) but 1 runwda child(ren) are still running
```

- **Smallest fix.** On every error return from `Start`, kill the process group and wait for the reaper (SIGTERM, grace, SIGKILL).

### DH-40. Replacing a runwda attempt never runs two at once for one device

- **Severity: medium.** Two `ios runwda` processes compete for one device's single UI-automation session.
- **Mechanism.** `stopOwned` sends SIGTERM to the previous attempt's group and returns without waiting, and `Start` spawns the next attempt at once.
- **Where.** `internal/device/runwda.go:792-804`.
- **Reproduction:**

```go
// Start replaces the previous attempt with SIGTERM to its group and
// spawns the next one immediately, without waiting for the first to
// exit. Two `ios runwda` processes then race for the device's single
// UI-automation session.
func TestDefectHunt_RunwdaReplaceWaitsForPreviousAttempt(t *testing.T) {
	bin, _ := fakeRunwdaBin(t)
	p := newProcRunner(bin)
	if _, err := p.Start("DEFECTHUNT-UDID", "com.example.runner", nil); err == nil {
		t.Fatal("fake runwda should fail")
	}
	if _, err := p.Start("DEFECTHUNT-UDID", "com.example.runner", nil); err == nil {
		t.Fatal("fake runwda should fail")
	}
	if n := liveFakes(bin); n > 1 {
		t.Fatalf("after the second attempt started, %d runwda processes for the same device are alive", n)
	}
}
```

```
--- FAIL: TestDefectHunt_RunwdaReplaceWaitsForPreviousAttempt (0.47s)
    after the second attempt started, 2 runwda processes for the same device are alive
```

- **Smallest fix.** Have `stopOwned` wait on the attempt's `exited` channel (SIGKILL after a grace) before returning.

### DH-41. The supervision tick and `agent_ensure` never launch two runners at once for one device

- **Severity: medium.** In production the second `AutomationHost.Launch` calls `stopHold`, which cancels the first launch while its caller is still waiting on it; `agent_ensure` then fails or reports a runner that has just been stopped.
- **Mechanism.** `relaunch` checks the attempt cap under `s.mu`, then unlocks around `Effects.Launch`. `Tick` (supervision loop) and `Ensure` (an `agent_ensure` call) can both pass the check and both launch.
- **Where.** `internal/agent/agent.go:632`, `663-665`; `internal/device/ios_agent.go:802`.
- **Reproduction** (`internal/agent/zz_defecthunt_concurrent_launch_test.go`):

```go
// relaunch drops s.mu around Effects.Launch. The daemon's supervision
// loop (Tick, daemon.go:290) and an agent_ensure call (Ensure) run on
// different goroutines; both observe a dead runner, both pass the
// attempt-cap check, and both launch. In production the second
// AutomationHost.Launch calls stopHold on the first launch's hold
// (ios_agent.go:802), cancelling the session the first caller is still
// waiting on. One device must never have two launches in flight.
func TestDefectHunt_TickAndEnsureNeverLaunchTwiceAtOnce(t *testing.T) {
	const dev = "DEFECTHUNT-UDID"
	var inFlight, maxInFlight, launches atomic.Int32
	release := make(chan struct{})
	sup := NewSupervisor(&SupervisorArgs{
		Effects: Effects{
			Launch: func(string) (int, error) {
				n := inFlight.Add(1)
				for {
					m := maxInFlight.Load()
					if n <= m || maxInFlight.CompareAndSwap(m, n) {
						break
					}
				}
				launches.Add(1)
				<-release // a launch takes seconds on a real device
				inFlight.Add(-1)
				return 4242, nil
			},
		},
		Probe: func(string) (Observation, error) {
			return Observation{Tethered: true, LockKnown: true, Installed: true, RunnerAlive: false}, nil
		},
		OwnerOf: func(string) (string, bool) { return "owner", true },
	})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = sup.Tick(dev) }()
	go func() { defer wg.Done(); _, _ = sup.Ensure(dev, "owner") }()
	time.Sleep(300 * time.Millisecond)
	close(release)
	wg.Wait()

	if maxInFlight.Load() > 1 || launches.Load() > 1 {
		t.Fatalf("one dead runner produced %d launches, %d in flight at once on the same device", launches.Load(), maxInFlight.Load())
	}
}
```

```
--- FAIL: TestDefectHunt_TickAndEnsureNeverLaunchTwiceAtOnce (0.30s)
    one dead runner produced 2 launches, 2 in flight at once on the same device
```

- **Smallest fix.** Add a per-device `launching` flag, set under `s.mu` before unlocking; a caller that finds it set returns the current snapshot instead of launching.

### DH-42. A child that dies right after every successful start reaches NeedsAttention

- **Severity: medium.** A crash-looping supervised child (bad flags, port taken, immediate panic) is restarted on every probe forever, with no backoff and no notification; `MaxAttempts` never applies.
- **Mechanism.** `startWithRecovery` calls `RecoverySucceeded` whenever `Start` returns nil, which resets the attempt counter, although nothing has shown the process is still alive.
- **Where.** `internal/health/supervise.go:131-135`.
- **Reproduction** (`internal/health/zz_defecthunt_supervise_test.go`):

```go
// A child that exits right after every successful Start is a failed
// recovery. Supervise's policy says MaxAttempts failures reach
// NeedsAttention (the only state that notifies), with backoff between
// attempts. Because Start returning nil counts as RecoverySucceeded and
// zeroes the attempt counter, the crash loop restarts on every tick
// forever and is never surfaced.
func TestDefectHunt_SuperviseCrashLoopAfterStartReachesNeedsAttention(t *testing.T) {
	m := New()
	var sawAttention atomic.Bool
	m.OnTransition(func(tr Transition) {
		if tr.To == NeedsAttention {
			sawAttention.Store(true)
		}
	})
	proc := &diesAfterStart{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		NewSupervisor(m).Supervise(ctx, proc, Policy{MaxAttempts: 3, BaseBackoff: 10 * time.Millisecond}, 20*time.Millisecond)
		close(done)
	}()
	time.Sleep(1500 * time.Millisecond)
	cancel()
	<-done

	starts := proc.starts.Load()
	snap, _ := m.Get(ID{Kind: KindSubprocess, Name: proc.Name()})
	if !sawAttention.Load() {
		t.Fatalf("crash loop never reached NeedsAttention: %d starts in 1.5s with MaxAttempts=3; final state %s attempts=%d", starts, snap.State, snap.Attempts)
	}
}
```

```
--- FAIL: TestDefectHunt_SuperviseCrashLoopAfterStartReachesNeedsAttention (1.50s)
    crash loop never reached NeedsAttention: 76 starts in 1.5s with MaxAttempts=3; final state healthy attempts=0
```

- **Smallest fix.** Count recovery as succeeded only when a later probe finds the process alive; if `Alive()` is false while still `Recovering`, call `RecoveryFailed` and back off.

### DH-43. A hung notifier never blocks stall escalation or the self-restart

- **Severity: medium.** The rescue path for a wedged daemon can itself hang: a `terminal-notifier` waiting on Notification Center blocks `ForceStall`, so the self-restart that should follow never happens.
- **Mechanism.** `AttentionNotifier.handleNeedsAttention` runs `terminal-notifier`/`osascript` synchronously with `cmd.Run()` and no timeout while holding its mutex, inside the model transition. `completeStuckDispatch` calls `ForceStall` before `selfRestart.Request`.
- **Where.** `internal/health/notify.go:117-138`, `internal/health/notify_exec.go:41-62`; `internal/mcp/server.go:702-709`.
- **Reproduction:**

```go
// The attention notifier execs terminal-notifier/osascript synchronously,
// with no timeout, inside the model transition and under its own mutex.
// ForceStall (the stuck-dispatch path, mcp/server.go:668) drives a
// NeedsAttention transition before selfRestart.Request; a notifier that
// hangs therefore blocks the self-restart that is meant to rescue a
// wedged daemon. ForceStall must return promptly regardless of delivery.
func TestDefectHunt_HungNotifierDoesNotBlockStallEscalation(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "terminal-notifier")
	// Hangs like a terminal-notifier waiting on Notification Center.
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nwhile :; do /bin/sleep 1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Cleanup(func() { _ = exec.Command("/usr/bin/pkill", "-KILL", "-f", dir).Run() })

	m := New()
	NewAttentionNotifier(NewMacOSNotifier()).Attach(m)
	w := NewProgressWatchdog(m, "spyder", time.Minute)
	w.Begin()

	returned := make(chan struct{})
	var once sync.Once
	go func() {
		w.ForceStall("dispatch stuck after deadline")
		once.Do(func() { close(returned) })
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("ForceStall did not return within 5s: the notifier child blocks stall escalation (and the self-restart that follows it)")
	}
}
```

```
--- FAIL: TestDefectHunt_HungNotifierDoesNotBlockStallEscalation (5.04s)
    ForceStall did not return within 5s: the notifier child blocks stall escalation (and the self-restart that follows it)
```

- **Smallest fix.** Run the notifier with `exec.CommandContext` and a few-second timeout, outside the lock (or in its own goroutine).

### DH-44. One old broken-pipe burst never pins the wedge monitor to "wedged" for the daemon's lifetime

- **Severity: medium.** After a single cable pull, the monitor reports a wedge forever: the doctor finding stays wedged, a snapshot (with a `devicectl` exec) is attempted every poll, and the parity check that would detect a real wedge and auto-recover never runs again.
- **Mechanism.** `reconcile` classifies from `RecentLogLines()`, a ring that is only appended to and never consumed or aged, and returns early when it matches, before the parity check.
- **Where.** `internal/wedge/monitor.go:153-184`.
- **Reproduction** (`internal/wedge/zz_defecthunt_stale_ring_test.go`; parity, capture and recovery hooks are replaced, so no `ioreg`, `devicectl` or `sudo` runs):

```go
// A burst of usbmuxd broken-pipe lines (a cable pull, one bad minute)
// lands in recentLogLines. reconcile classifies from that ring but never
// consumes or ages it, and the log tail only appends matching lines, so
// the same old burst is re-detected on every 30 s poll for as long as the
// daemon lives: a wedge snapshot every reconcile, the doctor finding
// pinned to wedged, the episode never closed, and the parity check
// (isWedgedFn) never consulted again. Once the burst has been acted on
// and no new lines arrive, a healthy host must clear the episode.
func TestDefectHunt_OldBrokenPipeBurstDoesNotPinWedgeForever(t *testing.T) {
	clearRecentLogs()
	t.Cleanup(clearRecentLogs)
	saveW, saveC, saveR := isWedgedFn, captureFn, recoverFn
	t.Cleanup(func() { isWedgedFn, captureFn, recoverFn = saveW, saveC, saveR })

	var parityChecks, snapshots, recoveries int
	isWedgedFn = func() (bool, int, int, error) { parityChecks++; return false, 1, 1, nil }
	captureFn = func(string, string) { snapshots++ }
	recoverFn = func(context.Context) error { recoveries++; return nil } // never the real sudo helper

	for range 3 {
		NoteLogLine(`write unix ->/var/run/usbmuxd: broken pipe`)
	}
	var st wedgeState
	ctx := context.Background()
	reconcile(ctx, "log-tail", &st) // the burst: one recovery attempt, as designed
	if recoveries != 1 {
		t.Fatalf("burst recoveries=%d want 1", recoveries)
	}

	// The host is healthy and no new matching lines arrive. Ten polls
	// (five minutes in production).
	for range 10 {
		reconcile(ctx, "poll", &st)
	}
	if parityChecks == 0 || st.inEpisode || LastDoctorFinding().Wedged || snapshots > 1 {
		t.Fatalf("stale burst re-detected on every poll: parityChecks=%d inEpisode=%v doctorWedged=%v snapshots=%d (want parity consulted, episode cleared, 1 snapshot)",
			parityChecks, st.inEpisode, LastDoctorFinding().Wedged, snapshots)
	}
}
```

```
--- FAIL: TestDefectHunt_OldBrokenPipeBurstDoesNotPinWedgeForever (0.00s)
    stale burst re-detected on every poll: parityChecks=0 inEpisode=true doctorWedged=true snapshots=11 (want parity consulted, episode cleared, 1 snapshot)
```

- **Smallest fix.** Clear the ring (or keep a sequence watermark) once a burst has been acted on, and keep running the parity check on polls.

### DH-45. The adb server watcher never restarts the server in a tight loop

- **Severity: medium.** A server that keeps dropping `track-devices` (a version fight with Unity's bundled adb) gets hundreds of `adb start-server` execs per second, each firing `OnRestart`, which starts a 30 s reverse-restore goroutine in the MCP handler.
- **Mechanism.** `Run` resets the backoff whenever `track` succeeds, and after `drain` returns it restarts with no delay; backoff applies only when both `track` and `start` fail.
- **Where.** `internal/adbserver/adbserver.go:84-99`; `internal/mcp/adb_restart.go:53`.
- **Reproduction** (`internal/adbserver/zz_defecthunt_restart_test.go`; uses the package's existing `fakeADB` script and a fake server on a random port):

```go
// flakyServer answers host:track-devices with OKAY and then drops the
// connection after hold, like an adb server that keeps exiting (a
// version fight with Unity's bundled adb, or a server crash loop).
// After maxDrops connections it keeps the next one open.
func flakyServer(t *testing.T, hold time.Duration, maxDrops int32) (addr string, accepted *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted = &atomic.Int32{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n := accepted.Add(1)
			go func(c net.Conn, n int32) {
				req := make([]byte, 4+len("host:track-devices"))
				if _, err := io.ReadFull(c, req); err != nil {
					_ = c.Close()
					return
				}
				_, _ = c.Write([]byte("OKAY0000"))
				if maxDrops > 0 && n > maxDrops {
					return // keep this one open
				}
				time.Sleep(hold)
				_ = c.Close()
			}(c, n)
		}
	}()
	return ln.Addr().String(), accepted
}

// The supervisor backs off (retryMin 1s .. retryMax 30s) only when it
// cannot reach the server and start-server fails. A server that accepts
// track-devices and then drops it is restarted again immediately, with
// no delay, so spyder spins exec'ing `adb start-server` and firing
// OnRestart (each spawning a 30s reverse-restore goroutine in mcp).
func TestDefectHunt_RestartLoopBacksOff(t *testing.T) {
	addr, _ := flakyServer(t, 0, 0)
	bin, marker := fakeADB(t)
	var events atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		New(Args{Adb: bin, Addr: addr, Evidence: func() string { return "" }, OnRestart: func(Event) { events.Add(1) }}).Run(ctx)
		close(done)
	}()
	time.Sleep(3 * time.Second)
	cancel()
	<-done
	data, _ := os.ReadFile(marker)
	starts := strings.Count(string(data), "start-server")
	// With a 1s minimum retry, three seconds allow at most ~4 restarts.
	if starts > 4 || events.Load() > 4 {
		t.Fatalf("server dropping track-devices: %d start-server execs and %d OnRestart events in 3s (no backoff)", starts, events.Load())
	}
}
```

```
--- FAIL: TestDefectHunt_RestartLoopBacksOff (3.00s)
    server dropping track-devices: 440 start-server execs and 441 OnRestart events in 3s (no backoff)
```

- **Smallest fix.** Treat a track connection that lasted less than `retryMin` as a failure: sleep the backoff and double it.

### DH-46. A forgotten log capture expires after its TTL even while lines keep arriving

- **Severity: medium.** A capture nobody reads keeps its `adb logcat` child or DTX connection, a 50 MB buffer and its `raw.log` growing for as long as the device keeps logging, contrary to "auto-expire after ttl_sec of no Get/Stop activity".
- **Mechanism.** `Session.append` sets `lastUsed` on every incoming line, so `idle()` is never true on a live stream and the sweeper never stops the session.
- **Where.** `internal/logcapture/session.go:135`, `172`, sweeper at `407`; contract text at `internal/mcp/log_capture.go:152`.
- **Reproduction** (`internal/logcapture/zz_defecthunt_ttl_test.go`):

```go
// log_capture_start documents "Sessions auto-expire after ttl_sec of no
// Get/Stop activity", and the package doc says a forgotten capture must
// not pin device IO indefinitely. Session.append refreshes lastUsed on
// every incoming line, so a capture on a live stream that nobody reads
// is never idle and the sweeper (session.go:407) never stops it.
func TestDefectHunt_ForgottenCaptureExpiresWhileLinesArrive(t *testing.T) {
	m := NewManager()
	defer m.Close()
	s, err := m.Start(context.Background(), chattyAdapter{}, StartParams{DeviceID: "dev", TTL: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	// No Get, no Stop, for four TTLs.
	time.Sleep(400 * time.Millisecond)
	if !s.idle(time.Now()) {
		var exp time.Time
		for _, info := range m.List() {
			if info.SessionID == s.ID {
				exp = info.ExpiresAt
			}
		}
		t.Fatalf("session with TTL 100ms and no Get/Stop for 400ms is not idle; List expires_at=%s (still in the future: %v)", exp.Format(time.RFC3339Nano), exp.After(time.Now()))
	}
}
```

```
--- FAIL: TestDefectHunt_ForgottenCaptureExpiresWhileLinesArrive (0.40s)
    session with TTL 100ms and no Get/Stop for 400ms is not idle; List expires_at=… (still in the future: true)
```

- **Smallest fix.** Remove the `s.lastUsed = time.Now()` line from `append`.

### DH-47. A state capture stops when its session ends

- **Severity: medium.** Every app crash or relaunch during a capture leaks a goroutine that polls the dead session at up to 100 Hz for the daemon's lifetime and cannot be stopped.
- **Mechanism.** Only `Session.Close` tears captures down. When the peer disconnects, `handleConn` removes the session but never stops its captures, and `StopStateCapture` can no longer find the session.
- **Where.** `internal/appchannel/session.go:975-993`.
- **Reproduction:**

```go
// A state capture belongs to its session. When the app goes away (crash,
// kill, relaunch) the session is reaped from the manager, so nothing can
// address the capture any more — its poller must stop with the session.
func TestDefectHunt_StateCaptureStopsWhenSessionEnds(t *testing.T) {
	m, l, addr := dhStart(t)
	t.Cleanup(func() { dhStopBounded(l) })
	conn := dhHello(t, addr, "capture-app", MethodStateQuery)
	result, _ := PackParams(map[string]any{"v": 1})
	dhServe(conn, func(env *Envelope) *Envelope { return &Envelope{ID: env.ID, Result: result} })
	s := dhWaitSession(t, m, "capture-app")

	c, err := s.StartStateCapture("world", MinStateCaptureInterval)
	if err != nil {
		t.Fatalf("StartStateCapture: %v", err)
	}
	t.Cleanup(c.cancel) // keep a failing run from polling into later tests
	if !dhEventually(2*time.Second, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.samples) > 0
	}) {
		t.Fatal("capture never sampled")
	}

	_ = conn.Close() // the app dies
	if !dhEventually(3*time.Second, func() bool { _, ok := m.GetSession(s.ID); return !ok }) {
		t.Fatal("ended session was not reaped from the manager")
	}

	select {
	case <-c.done:
	case <-time.After(3 * time.Second):
		c.mu.Lock()
		failed := c.errors
		c.mu.Unlock()
		t.Fatalf("state capture %s still polling 3s after its session ended (%d failed state_query calls so far); "+
			"GetSession(%s) no longer finds the session, so StopStateCapture can never reach it", c.ID, failed, s.ID)
	}
}
```

```
--- FAIL: TestDefectHunt_StateCaptureStopsWhenSessionEnds (3.03s)
    state capture … still polling 3s after its session ended (302 failed state_query calls so far); GetSession(…) no longer finds the session, so StopStateCapture can never reach it
```

- **Smallest fix.** When the read loop ends, call `s.closeStateCaptures()` and `s.cancel()`.

### DH-48. Starting a state capture on a closed session returns an error instead of panicking

- **Severity: medium.** It needs a race with `app_channel_stop`, but the panic happens in a handler goroutine with no `recover` (`internal/mcp/server.go:626-633`), so the result is a daemon crash. This is the concrete case behind the missing-`recover` note in the unconfirmed list.
- **Mechanism.** `closeStateCaptures` sets the capture map to nil; a later `StartStateCapture` writes into it.
- **Where.** `internal/appchannel/state_capture.go:176`, `377`.
- **Reproduction:**

```go
// Starting a capture on a session that has been closed must fail cleanly,
// not panic: handlers run in a bare goroutine, so a panic kills the daemon.
func TestDefectHunt_StartStateCaptureOnClosedSessionDoesNotPanic(t *testing.T) {
	m, l, addr := dhStart(t)
	t.Cleanup(func() { dhStopBounded(l) })
	_ = dhHello(t, addr, "closed-app", MethodStateQuery)
	s := dhWaitSession(t, m, "closed-app")
	_ = s.Close()

	var (
		perr any
		err  error
	)
	func() {
		defer func() { perr = recover() }()
		var c *StateCapture
		c, err = s.StartStateCapture("world", 0)
		if c != nil {
			c.cancel()
		}
	}()
	if perr != nil {
		t.Fatalf("StartStateCapture on a closed session panicked: %v", perr)
	}
	if err == nil {
		t.Fatal("StartStateCapture on a closed session returned no error")
	}
}
```

```
--- FAIL: TestDefectHunt_StartStateCaptureOnClosedSessionDoesNotPanic (0.00s)
    StartStateCapture on a closed session panicked: assignment to entry in nil map
```

- **Smallest fix.** Under `s.mu`, return an error when `s.stateCaptures == nil`.

### DH-49. Memory tracks bytes received, not the length a peer claims

- **Severity: medium.** Memory exhaustion from loopback or LAN peers: each connection that sends only a 4-byte header pins 16 MiB.
- **Mechanism.** `ReadFrame` allocates `make([]byte, length)` straight from the header; the buffer is held for 5 s before hello and indefinitely after it (the read loop has no read deadline).
- **Where.** `internal/appchannel/protocol.go:134`.
- **Reproduction:**

```go
// A frame's 4-byte length prefix is a claim, not data. Peers that have sent
// nothing but a header (here: 8 connections, 4 bytes each) must not make the
// listener hold MaxFrameBytes per connection while it waits for bodies that
// never come (5s before hello; forever after it — readLoop has no deadline).
func TestDefectHunt_FrameLengthPrefixDoesNotPreallocate(t *testing.T) {
	_, l, addr := dhStart(t)
	t.Cleanup(func() { dhStopBounded(l) })
	const conns = 8
	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	var hdr [4]byte
	hdr[0], hdr[1], hdr[2], hdr[3] = 0xff, 0xff, 0xff, 0x00 // 16 MiB - 1
	for i := 0; i < conns; i++ {
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		if _, err := c.Write(hdr[:]); err != nil {
			t.Fatal(err)
		}
	}
	if !dhEventually(2*time.Second, func() bool { return l.Connectivity().Accepted == conns }) {
		t.Fatal("connections not accepted")
	}
	time.Sleep(300 * time.Millisecond) // let each handshake read its header
	var after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&after)

	grown := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	const budget = conns * 256 << 10 // generous: 256 KiB per header-only peer
	if grown > budget {
		t.Fatalf("%d peers that sent only a 4-byte header grew the live heap by %d MiB (budget %d KiB)",
			conns, grown>>20, budget>>10)
	}
}
```

```
--- FAIL: TestDefectHunt_FrameLengthPrefixDoesNotPreallocate (0.32s)
    8 peers that sent only a 4-byte header grew the live heap by 127 MiB (budget 2048 KiB)
```

- **Smallest fix.** Read the body with `io.ReadAll(io.LimitReader(r, int64(length)))` (it grows as bytes arrive) and set a read deadline per frame body.

### DH-50. Drained app logs are never lost when encoding the reply fails

- **Severity: medium.** One `NaN` in a log line or perf sample destroys the whole drained batch, and logging `NaN` is common while debugging physics.
- **Mechanism.** The handler drains the session's buffer, then `json.MarshalIndent` fails on `NaN`, and the drained lines are gone. `app_perf_get` and `app_state_capture_get` behave the same way.
- **Where.** `internal/mcp/app_channel.go:866`; `internal/mcp/tools.go:226`.
- **Reproduction:**

```go
// app_log_get drains the session's log buffer before encoding it. A single
// pushed arg that JSON cannot encode (NaN — a typical value to be logging
// while debugging physics) fails the encode, and every drained line is lost.
func TestDefectHunt_AppLogGetNeverDropsLinesItCannotReturn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(paths.DataDirEnv, t.TempDir())
	h := NewHandlerWithAdapters(&stubAdapter{}, nil)
	mgr := appchannel.NewManager()
	t.Cleanup(mgr.Close)
	h.appChannel = mgr
	l, err := mgr.Start(appchannel.StartParams{Owner: "defecthunt"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Stop)
	conn := dhAppConn(t, l.Port, "physics-game", nil, appchannel.MethodPing)
	sid := dhSessionID(t, mgr)

	for _, lp := range []appchannel.LogPush{
		{Timestamp: 1, Level: "info", Format: "boot ok"},
		{Timestamp: 2, Level: "warn", Format: "velocity %f", Args: []any{math.NaN()}},
	} {
		raw, _ := appchannel.PackParams(lp)
		if err := appchannel.WriteFrame(conn, &appchannel.Envelope{Method: appchannel.PushLog, Params: raw}); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(300 * time.Millisecond) // let the read loop buffer both pushes

	var outcomes []string
	delivered := false
	for i := 0; i < 2; i++ {
		res, err := h.handleAppLogGet(map[string]any{"session_id": sid})
		switch {
		case err != nil:
			outcomes = append(outcomes, "error: "+err.Error())
		case res.IsError:
			outcomes = append(outcomes, "tool error: "+resultErrorText(t, res))
		default:
			text := resultErrorText(t, res) // first text block, error or not
			outcomes = append(outcomes, text)
			if strings.Contains(text, "boot ok") {
				delivered = true
			}
		}
	}
	if !delivered {
		t.Fatalf("the pushed line \"boot ok\" was never returned by app_log_get; two calls gave:\n%s",
			strings.Join(outcomes, "\n---\n"))
	}
}
```

```
--- FAIL: TestDefectHunt_AppLogGetNeverDropsLinesItCannotReturn (0.30s)
    the pushed line "boot ok" was never returned by app_log_get; two calls gave: …
```

- **Smallest fix.** Replace non-finite floats with `null` or a string before encoding, or commit the drain only after encoding succeeds.

### DH-51. A stopped listener never registers a new session

- **Severity: medium.** A connection mid-handshake during `stop()` registers afterwards as a zombie session with no keyed owner, after its transport cleanup has already run.
- **Mechanism.** `stop()` closes a snapshot of its sessions; a handshake that completes afterwards registers itself.
- **Where.** `internal/appchannel/session.go:875-905` against `956-973`.
- **Reproduction:**

```go
// After Listener.Stop returns, no session may appear on that listener: its
// transport mapping is gone and it is no longer in the keyed registry, so a
// session accepted late is an unowned zombie.
func TestDefectHunt_StoppedListenerAcceptsNoNewSession(t *testing.T) {
	m, l, addr := dhStart(t)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if !dhEventually(2*time.Second, func() bool { return l.Connectivity().Accepted == 1 }) {
		t.Fatal("connection not accepted")
	}

	l.Stop() // the app is still before its hello

	hp, _ := PackParams(Hello{AppName: "late", AppVersion: "defecthunt", Methods: MethodDescriptors(MethodPing)})
	_ = WriteFrame(conn, &Envelope{ID: 1, Method: MethodHello, Params: hp})
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	ack, ackErr := ReadFrame(conn)
	time.Sleep(100 * time.Millisecond)
	live := m.Sessions()
	for _, s := range live {
		_ = s.Close()
	}
	if len(live) > 0 || (ackErr == nil && ack.Error == nil) {
		t.Fatalf("stopped listener %s completed a handshake (ack=%+v err=%v) and registered %d live session(s)",
			l.ID, ack, ackErr, len(live))
	}
}
```

```
--- FAIL: TestDefectHunt_StoppedListenerAcceptsNoNewSession (0.11s)
    stopped listener 303cad00df4de4ed completed a handshake (…) and registered 1 live session(s)
```

- **Smallest fix.** Mark the listener stopped at the start of `stop()`; after the handshake, check the flag under `l.mu` and close if set; have `stop()` wait for in-flight `handleConn` calls.

### DH-52. `GetOrCreateListener` never returns a listener that is being stopped

- **Severity: medium.** A launch can be given a channel port that nobody listens on, so the app never connects.
- **Mechanism.** The key leaves the registry only at the end of `stop()`, after the socket has closed and cleanup (for example `adb reverse --remove`) has run.
- **Where.** `internal/appchannel/session.go:697-703` against `899-903`.
- **Reproduction:**

```go
// While a keyed listener is being stopped (its socket is already closed and
// its transport cleanup is running), GetOrCreateListener must not hand it out:
// the launch would inject a port nobody listens on.
func TestDefectHunt_GetOrCreateListenerNeverReturnsStoppingListener(t *testing.T) {
	m := NewManager()
	t.Cleanup(m.Close)
	key := AppKey{DeviceID: "dev-defecthunt", BundleID: "com.example.defecthunt"}
	l, err := m.GetOrCreateListener(key)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	l.SetCleanup(func() { <-release }) // e.g. `adb reverse --remove` taking its time
	go l.Stop()

	addr := fmt.Sprintf("127.0.0.1:%d", l.Port)
	if !dhEventually(2*time.Second, func() bool {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return true
		}
		_ = c.Close()
		return false
	}) {
		t.Fatal("listener socket never closed")
	}

	got, err := m.GetOrCreateListener(key)
	if err != nil {
		t.Fatalf("GetOrCreateListener: %v", err)
	}
	c, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", got.Port), time.Second)
	if c != nil {
		_ = c.Close()
	}
	unblock()
	if dialErr != nil {
		t.Fatalf("GetOrCreateListener returned listener %s (same as stopping one: %v) whose port %d refuses connections: %v",
			got.ID, got == l, got.Port, dialErr)
	}
	if got != l {
		got.Stop()
	}
}
```

```
--- FAIL: TestDefectHunt_GetOrCreateListenerNeverReturnsStoppingListener (0.00s)
    GetOrCreateListener returned listener 134e6027529de131 (same as stopping one: true) whose port 59386 refuses connections
```

- **Smallest fix.** Remove the key from the registry first in `stop()`, or skip stopped listeners in `GetOrCreateListener`.

### DH-53. Instances booted by `pool_warm` linger and then shut down

- **Severity: medium.** Warmed simulators stay booted forever, and the client-supplied `count` is unbounded and runs under the pool lock.
- **Mechanism.** `Warm` boots instances into the running tier but never arms the linger timer, unlike `Adopt`.
- **Where.** `internal/pool/pool.go:640-672` (contrast `366-372`); `internal/mcp/tools.go:2448-2451`.
- **Reproduction** (`internal/pool/zz_defecthunt_pool_test.go`; uses the package's existing `fakeExec` and fake clock):

```go
// pool_warm boots instances into the running (booted, idle) tier; like
// every other idle running instance they must linger and then shut down,
// not stay booted forever.
func TestDefectHunt_WarmedInstancesDoNotStayBootedForever(t *testing.T) {
	fe := newFakeExec()
	fc := newFakeClock(time.Unix(1_800_000_000, 0))
	p := newWithClock(&Config{Templates: []TemplateConfig{iOSTemplate("iphone16")}}, fe, fc)

	if err := p.Warm("iphone16", 2); err != nil {
		t.Fatal(err)
	}
	fc.Advance(24 * time.Hour) // linger is 60s

	booted := 0
	for _, state := range fe.sims {
		if state == "Booted" {
			booted++
		}
	}
	if st := p.Status()[0]; st.Running != 0 || booted != 0 {
		t.Fatalf("24h after pool_warm with a 60s linger: %d instances still running, %d simulators booted", st.Running, booted)
	}
}
```

```
--- FAIL: TestDefectHunt_WarmedInstancesDoNotStayBootedForever (0.00s)
    24h after pool_warm with a 60s linger: 2 instances still running, 2 simulators booted
```

- **Smallest fix.** After `bootInstance` in `Warm`, set `LastReleaseAt` and arm the linger timer as `Adopt` does; cap `n`.

### DH-54. Pool status never waits on a simctl call

- **Severity: medium.** `pool_list`, `pool_gc` and `pool_drain` block behind any boot, and forever if simctl wedges, because the real executor has no timeout.
- **Mechanism.** `p.mu` is held across every executor call, and `simemu.runCapture` uses `exec.Command` with no context or timeout.
- **Where.** `internal/pool/pool.go:415-461`, `640-672`, `712-768`, `771-773`; `internal/simemu/simemu.go:319-322`.
- **Reproduction:**

```go
// pool_list (Status) is a read-only snapshot and must not wait for a
// simulator boot in progress. The real executor runs simctl with no
// timeout, so a wedged simctl would block it forever.
func TestDefectHunt_StatusDoesNotBlockBehindBoot(t *testing.T) {
	d := &dhExec{fakeExec: newFakeExec(), bootEntered: make(chan struct{}), bootGate: make(chan struct{})}
	entered := d.bootEntered
	p := newWithClock(&Config{Templates: []TemplateConfig{iOSTemplate("iphone16")}}, d, newFakeClock(time.Unix(1_800_000_000, 0)))

	warmDone := make(chan error, 1)
	go func() { warmDone <- p.Warm("iphone16", 1) }()
	<-entered

	statusDone := make(chan struct{})
	go func() { p.Status(); close(statusDone) }()
	select {
	case <-statusDone:
	case <-time.After(2 * time.Second):
		close(d.bootGate)
		<-warmDone
		t.Fatal("pool Status blocked for 2s behind an in-progress SimBoot (p.mu is held across exec)")
	}
	close(d.bootGate)
	<-warmDone
}
```

```
--- FAIL: TestDefectHunt_StatusDoesNotBlockBehindBoot (2.00s)
    pool Status blocked for 2s behind an in-progress SimBoot (p.mu is held across exec)
```

- **Smallest fix.** Use `exec.CommandContext` with a deadline, and run the exec outside `p.mu` (mark the instance booting, unlock, exec, re-lock to commit).

### DH-55. An Android pool instance can be shut down, because `AVDBoot` returns its adb serial

- **Severity: medium.** Emulators started by `pool_warm` are never stopped, and `pool_drain` deletes the AVD under a running emulator.
- **Mechanism.** `simemu.AVDBoot` returns `"<name> (started, serial available via `adb devices` once booted)"`; the pool stores that as `Serial` and later runs `adb -s <that> emu kill`, which always fails; `destroyInstance` ignores the failure and deletes the AVD.
- **Where.** `internal/pool/executor.go:109-111` → `internal/simemu/simemu.go:280-293`; `internal/pool/pool.go:944`, `964-972`, `985-996`.
- **Reproduction** (a fake `emulator` that exits 0 is the only thing on `PATH`):

```go
// The pool shuts an Android instance down with AVDShutdown(inst.Serial),
// i.e. `adb -s <serial> emu kill`. The real executor's AVDBoot must
// therefore return an adb serial. Uses a fake `emulator` script on a
// PATH that contains nothing else; no real emulator or adb is run.
func TestDefectHunt_RealAVDBootReturnsAdbSerial(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "emulator"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SPYDER_DATA_DIR", t.TempDir())

	serial, err := RealExecutor{}.AVDBoot("spyder-pool-deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^emulator-\d+$`).MatchString(serial) {
		t.Fatalf("AVDBoot serial = %q; the pool later runs `adb -s %q emu kill`, which cannot match any device", serial, serial)
	}
}
```

```
--- FAIL: TestDefectHunt_RealAVDBootReturnsAdbSerial (0.00s)
    AVDBoot serial = "spyder-pool-deadbeef (started, serial available via `adb devices` once booted)"; the pool later runs `adb -s … emu kill`, which cannot match any device
```

- **Smallest fix.** Wait (bounded) for the `emulator-N` serial whose `adb -s … emu avd name` equals the AVD and return it, or shut down by AVD name.

### DH-56. Android `LogStream` reports a failed `adb logcat` as an error

- **Severity: medium.** A log capture against a disconnected or unauthorised phone is recorded as a clean, complete, empty capture.
- **Mechanism.** When logcat exits at once, the scanner hits EOF, `reapLogcat` ignores the `Wait` error, and `LogStream` returns nil.
- **Where.** `internal/device/android.go:1167-1175`.
- **Reproduction:**

```go
// `adb logcat` that exits at once (device gone, unauthorized, adb server
// down) makes LogStream return nil, which logcapture records as a clean,
// complete capture with no transport error.
func TestDefectHunt_AndroidLogStreamReportsLogcatFailure(t *testing.T) {
	dhFakeTools(t, "")
	dir := t.TempDir()
	bin := filepath.Join(dir, "logcat-fails")
	script := "#!/bin/sh\necho \"adb: device 'R5CT1234ABC' not found\" >&2\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	a := NewAndroidAdapter()
	a.logcatBin = bin
	out := make(chan LogLine, 16)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := a.LogStream(ctx, "R5CT1234ABC", LogFilter{}, out)
	if ctx.Err() != nil {
		t.Fatalf("LogStream did not return before the test deadline")
	}
	if err == nil {
		t.Fatal("LogStream returned nil after adb logcat failed with \"device not found\"; the capture is recorded as complete")
	}
}
```

```
--- FAIL: TestDefectHunt_AndroidLogStreamReportsLogcatFailure (0.25s)
    LogStream returned nil after adb logcat failed with "device not found"; the capture is recorded as complete
```

- **Smallest fix.** Capture stderr into a bounded buffer; if `ctx.Err() == nil` after the loop, return `adb logcat exited: <wait error>: <stderr>`.

### DH-57. Android logcat crash reports name the crashing package

- **Severity: medium.** `crashes(process=<package>)` drops every Java crash. The existing tests assert the logcat tag as the process, which is why they pass.
- **Mechanism.** `Process` is taken from the logcat tag, which is always `AndroidRuntime`; the package is on the `Process: com.x, PID:` line.
- **Where.** `internal/device/android.go:675-681`.
- **Reproduction** (`internal/device/zz_defecthunt_crashes_test.go`):

```go
// Every Java crash in `logcat -b crash` is logged under the tag
// AndroidRuntime; the crashing package is on the "Process:" line. Crashes
// reports Process="AndroidRuntime", so crashes(process=<package>) drops
// every Java crash. (crashes_test.go asserts the tag as the process.)
func TestDefectHunt_AndroidLogcatCrashProcessIsThePackage(t *testing.T) {
	const buf = `11-19 14:30:22.000  1234  1234 E AndroidRuntime: FATAL EXCEPTION: main
11-19 14:30:22.001  1234  1234 E AndroidRuntime: Process: com.example.app, PID: 1234
11-19 14:30:22.002  1234  1234 E AndroidRuntime: java.lang.NullPointerException: boom
11-19 14:30:22.003  1234  1234 E AndroidRuntime: 	at com.example.app.Main.onCreate(Main.java:42)
`
	all := parseLogcatCrashBuffer(buf, time.Time{}, "")
	if len(all) != 1 || all[0].Process != "com.example.app" {
		got := ""
		if len(all) > 0 {
			got = all[0].Process
		}
		t.Errorf("crash Process = %q, want com.example.app (from the Process: line)", got)
	}
	if got := parseLogcatCrashBuffer(buf, time.Time{}, "com.example.app"); len(got) != 1 {
		t.Errorf("process filter com.example.app returned %d reports, want 1", len(got))
	}
}
```

```
--- FAIL: TestDefectHunt_AndroidLogcatCrashProcessIsThePackage (0.00s)
    crash Process = "AndroidRuntime", want com.example.app (from the Process: line)
    process filter com.example.app returned 0 reports, want 1
```

- **Smallest fix.** Inside an open block, match `Process: ([^,]+), PID:` and set the process from it.

### DH-58. App crash text that mentions "device not found" is never read as a disconnect

- **Severity: medium.** `crashes()` fails with "device not connected" on a healthy phone whose app threw, say, "Bluetooth device not found".
- **Mechanism.** `isAndroidDeviceNotConnected` ("contains device and contains not found") is applied to the crash buffer on stdout.
- **Where.** `internal/device/android.go:621-624` (the same pattern is in `ListApps`, `LaunchApp`, `InstallApp`).
- **Reproduction:**

```go
// androidLogcatCrashes runs isAndroidDeviceNotConnected over the crash
// buffer itself (stdout), and that heuristic is "contains device and
// contains not found". An app that crashes with "... device not found"
// (Bluetooth, camera, USB, audio) makes crashes() report the phone as
// disconnected and return no reports.
func TestDefectHunt_AndroidCrashTextIsNotADisconnect(t *testing.T) {
	dhFakeTools(t, "")
	const buf = `11-19 14:30:22.000  1234  1234 E AndroidRuntime: FATAL EXCEPTION: main
11-19 14:30:22.001  1234  1234 E AndroidRuntime: Process: com.example.btapp, PID: 1234
11-19 14:30:22.002  1234  1234 E AndroidRuntime: java.lang.IllegalStateException: Bluetooth device not found
`
	dhHook(t, func(ctx context.Context, args ...string) ([]byte, []byte, error) {
		for _, a := range args {
			if a == "pull" {
				return nil, []byte("adb: error: failed to stat remote object '/data/tombstones/.': Permission denied\n"), errors.New("exit status 1")
			}
		}
		return []byte(buf), nil, nil
	})
	reports, err := NewAndroidAdapter().Crashes("R5CT1234ABC", time.Time{}, "")
	if err != nil {
		t.Fatalf("Crashes: %v (the phone answered; the text is the app's exception)", err)
	}
	if len(reports) != 1 {
		t.Fatalf("got %d reports, want 1", len(reports))
	}
}
```

```
--- FAIL: TestDefectHunt_AndroidCrashTextIsNotADisconnect (0.00s)
    Crashes: device not connected: R5CT1234ABC (the phone answered; the text is the app's exception)
```

- **Smallest fix.** Apply the heuristic to stderr only, and only when the command failed.

### DH-59. The iOS `.ips` header decoder yields a process name and a time

- **Severity: medium.** On iOS, `crashes` with `since` or `process` set returns nothing.
- **Mechanism.** The first line of an iOS 15+ `.ips` file has the keys `app_name`, `name`, `timestamp` and `bug_type`; `ipsHeader` looks for `procName`, `process_name` and `captured_time`, so `Process` is empty and `Timestamp` zero, and `Crashes` filters every report out. The proof decodes a real header line with the struct; `Crashes` itself needs a go-ios session, which was not run.
- **Where.** `internal/device/ios.go:1874-1880`, `1826-1843`, filter at `1857-1862`.
- **Reproduction:**

```go
// The first line of an iOS 15+ .ips crash report is a small JSON header
// keyed app_name / name / timestamp / bug_type. procName, captureTime and
// exception live in the multi-line body. ipsHeader looks for procName,
// process_name and captured_time on the first line, so every report gets
// an empty Process and zero Timestamp, and IOSAdapter.Crashes then drops
// all of them whenever since or process is set (ios.go:1857-1862).
func TestDefectHunt_IOSIpsHeaderYieldsProcessAndTime(t *testing.T) {
	const first = `{"app_name":"MultiMaze","timestamp":"2026-11-19 14:22:31.00 +1100","app_version":"1.0","slice_uuid":"0b5f0d4e-1d9a-3a0e-9e7f-6d2b7c0f9a11","build_version":"7","platform":2,"bundleID":"com.squz.multimaze","share_with_app_devs":0,"is_first_party":0,"bug_type":"309","os_version":"iPhone OS 18.1 (22B83)","roots_installed":0,"name":"MultiMaze","incident_id":"6B1D2E8A-2F4C-4C1B-9B6E-3E2F8D7C1A90"}`
	var hdr ipsHeader
	if err := json.Unmarshal([]byte(first), &hdr); err != nil {
		t.Fatal(err)
	}
	if hdr.ProcName == "" && hdr.ProcessName == "" {
		t.Errorf("no process name decoded from the .ips header line (app_name/name = MultiMaze)")
	}
	if hdr.CapturedTime == "" {
		t.Errorf("no capture time decoded from the .ips header line (timestamp = 2026-11-19 14:22:31.00 +1100)")
	}
}
```

```
--- FAIL: TestDefectHunt_IOSIpsHeaderYieldsProcessAndTime (0.00s)
    no process name decoded from the .ips header line (app_name/name = MultiMaze)
    no capture time decoded from the .ips header line (timestamp = 2026-11-19 14:22:31.00 +1100)
```

- **Smallest fix.** Decode `name`/`app_name` and `timestamp` (layout `"2006-01-02 15:04:05.00 -0700"`), optionally falling back to the body's `procName` and `captureTime`.

### DH-60. Android `DisplaySize` handles a `wm size` override

- **Severity: medium.** `notification_tap_first` fails on Samsung phones at their default resolution and on any device with `wm size` set.
- **Mechanism.** A second `Override size:` line makes the parse fail; taps use the override (logical) size.
- **Where.** `internal/device/android_input.go:146-160`.
- **Reproduction** (`internal/device/zz_defecthunt_parsers_test.go`):

```go
// `wm size` prints a second "Override size:" line whenever the logical
// resolution differs from the panel (Samsung's default FHD+ on a WQHD+
// panel, or any `wm size WxH`). `input tap` works in the override
// (logical) space. DisplaySize fails outright on that output, which breaks
// notification_tap_first on those devices.
func TestDefectHunt_AndroidDisplaySizeHandlesOverride(t *testing.T) {
	dhFakeTools(t, "")
	dhHook(t, func(ctx context.Context, args ...string) ([]byte, []byte, error) {
		return []byte("Physical size: 1440x3200\nOverride size: 1080x2400\n"), nil, nil
	})
	w, h, err := NewAndroidAdapter().DisplaySize("R5CT1234ABC")
	if err != nil {
		t.Fatalf("DisplaySize: %v", err)
	}
	if w != 1080 || h != 2400 {
		t.Fatalf("DisplaySize = %dx%d, want the logical 1080x2400 that input tap uses", w, h)
	}
}
```

```
--- FAIL: TestDefectHunt_AndroidDisplaySizeHandlesOverride (0.00s)
    DisplaySize: unexpected wm size output: "Physical size: 1440x3200\nOverride size: 1080x2400\n"
```

- **Smallest fix.** Parse line by line and prefer `Override size:`.

### DH-61. A 40-hex iOS UDID is treated as a physical iOS device

- **Severity: medium.** On iPhone X/8 and older iPads the app channel never connects (the phone is told to dial 127.0.0.1), `usb_speed` is missing, and an uninventoried UDID is classified as an Android serial.
- **Mechanism.** `IsSimulatorID` treats anything that is not `8-16` hex as a simulator; `usbspeed.skipUSBMeta` relies on it; `inventory.ClassifyRaw` only knows `8-16` and `8-4-4-4-12`.
- **Where.** `internal/device/ios.go:1900-1907`; `internal/usbspeed/enrich.go:70`; `internal/inventory/inventory.go:137-146`; consumer `internal/mcp/app_channel_lan.go:31`.
- **Reproduction** (three packages):

```go
// iPhone X / 8 and earlier (A11 and older, up to iOS 16) have a 40-hex
// UDID with no dash. isSimulatorID calls that a simulator, so the app
// channel tells a physical phone to dial 127.0.0.1 (pickAppChannelHost)
// and usbspeed skips the device (skipUSBMeta). inventory.ClassifyRaw has
// the same gap and files the UDID as an Android serial.
func TestDefectHunt_LegacyIOSUDIDIsNotASimulator(t *testing.T) {
	const legacy = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678" // 40 hex, iPhone X era
	if IsSimulatorID(legacy) {
		t.Fatalf("IsSimulatorID(%q) = true; a 40-hex UDID is a physical device", legacy)
	}
}

// A USB-attached iPhone 8/X (40-hex UDID, also its USB serial) is a
// physical phone. skipUSBMeta asks device.IsSimulatorID, which calls any
// UDID that is not 8-16 a simulator, so the device never gets usb_speed.
func TestDefectHunt_LegacyIOSUDIDGetsUSBSpeed(t *testing.T) {
	const legacy = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	raw := census(ioregNode("iPhone@01100000", "iPhone", legacy, 0, USBDeviceSpeedHigh))
	if _, ok := SpeedForID(legacy, ParseCensus(raw)); !ok {
		t.Fatalf("census fixture does not carry %s", legacy)
	}
	devices := []device.Info{{UUID: legacy, Platform: "ios", Model: "iPhone10,6"}}
	Enrich(devices, raw, nil, nil)
	if devices[0].USBSpeed != "480 Mb/s" {
		t.Fatalf("USBSpeed = %q for a cabled physical iPhone X, want \"480 Mb/s\"", devices[0].USBSpeed)
	}
}

// iOS devices before the A12 (iPhone X/8 and older iPads, iOS <= 16) have
// a 40-hex UDID with no dash, as printed by `ios list` and xctrace.
// ClassifyRaw only knows 8-16 and 8-4-4-4-12, so it files the UDID as an
// Android serial.
func TestDefectHunt_ClassifyRawLegacyIOSUDID(t *testing.T) {
	const legacy = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	e := ClassifyRaw(legacy)
	if e.Platform != "ios" || e.IOSUUID != legacy {
		t.Fatalf("ClassifyRaw(%q) = platform %q, ios_uuid %q, android_serial %q; want ios / ios_uuid", legacy, e.Platform, e.IOSUUID, e.AndroidSerial)
	}
}
```

```
--- FAIL: TestDefectHunt_LegacyIOSUDIDIsNotASimulator (0.00s)
    IsSimulatorID("a1b2c3d4e5f60718293a4b5c6d7e8f9012345678") = true; a 40-hex UDID is a physical device
--- FAIL: TestDefectHunt_LegacyIOSUDIDGetsUSBSpeed (0.00s)
    USBSpeed = "" for a cabled physical iPhone X, want "480 Mb/s"
--- FAIL: TestDefectHunt_ClassifyRawLegacyIOSUDID (0.00s)
    ClassifyRaw("a1b2…5678") = platform "android", …; want ios / ios_uuid
```

- **Smallest fix.** Treat `^[0-9A-Fa-f]{40}$` as a hardware UDID in all three places.

### DH-62. A desktop app's long output line never wedges the app

- **Severity: medium.** A desktop game that prints one line over 1 MiB without a newline hangs forever in `write`.
- **Mechanism.** The scanner stops at `bufio.ErrTooLong` and nothing reads the pipe afterwards. A 512 KiB control exits normally.
- **Where.** `internal/device/desktop.go:352-368`.
- **Reproduction** (`internal/device/zz_defecthunt_desktop_test.go`):

```go
// desktopProc.scan stops at the first line over 1 MiB (bufio.ErrTooLong)
// and nothing drains the pipe after that, so the launched game blocks
// forever in write(2) once the 64 KiB pipe buffer fills. It never exits.
func TestDefectHunt_DesktopLongLineDoesNotWedgeApp(t *testing.T) {
	dhFakeTools(t, "")
	exe := dhScript(t, "head -c 2097152 /dev/zero | tr '\\000' x\necho\necho done\n")
	a := NewDesktopAdapter(nil)
	if err := a.LaunchApp(exe, "com.example.game", nil); err != nil {
		t.Fatal(err)
	}
	pid, _ := a.AppPID(exe, "com.example.game")
	t.Cleanup(func() {
		if pid > 0 {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	})
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := a.AppPID(exe, "com.example.game"); err != nil {
			return // exited
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("app that printed a 2 MiB line is still running after 15s (pid %d): its stdout is no longer read", pid)
}

// Control for the test above: the same script with a 512 KiB line (under
// the scanner's 1 MiB cap) exits promptly. It passes without -race; under
// -race it trips the p.pid race pinned by TestDefectHunt_DesktopPIDRace.
func TestDefectHunt_DesktopLongLineControl(t *testing.T) {
	dhFakeTools(t, "")
	exe := dhScript(t, "head -c 524288 /dev/zero | tr '\\000' x\necho\necho done\n")
	a := NewDesktopAdapter(nil)
	if err := a.LaunchApp(exe, "com.example.game", nil); err != nil {
		t.Fatal(err)
	}
	pid, _ := a.AppPID(exe, "com.example.game")
	t.Cleanup(func() {
		if pid > 0 {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	})
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := a.AppPID(exe, "com.example.game"); err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("control app with a 512 KiB line did not exit within 15s")
}
```

```
--- FAIL: TestDefectHunt_DesktopLongLineDoesNotWedgeApp (15.01s)
    app that printed a 2 MiB line is still running after 15s (pid 60807): its stdout is no longer read
```

The control passes without `-race`; under `-race` it trips the pid race below.

- **Smallest fix.** After a scan error, `io.Copy(io.Discard, r)`, or use a reader that splits over-long lines.

### DH-63. Relaunching a desktop app never orphans the first process

- **Severity: medium.** The first instance keeps running unmanaged after `terminate_app`.
- **Mechanism.** A second `LaunchApp` overwrites the map entry without stopping the first process.
- **Where.** `internal/device/desktop.go:167-170`.
- **Reproduction:**

```go
// A second LaunchApp of the same (executable, bundle) replaces the map
// entry without stopping the first process. TerminateApp then only
// reaches the second one; the first keeps running, unmanaged.
func TestDefectHunt_DesktopRelaunchDoesNotOrphanFirstProcess(t *testing.T) {
	dhFakeTools(t, "")
	exe := dhScript(t, "exec sleep 30\n")
	a := NewDesktopAdapter(nil)
	if err := a.LaunchApp(exe, "com.example.game", nil); err != nil {
		t.Fatal(err)
	}
	first, err := a.AppPID(exe, "com.example.game")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-first, syscall.SIGKILL) })
	if err := a.LaunchApp(exe, "com.example.game", nil); err != nil {
		t.Fatal(err)
	}
	second, _ := a.AppPID(exe, "com.example.game")
	t.Cleanup(func() {
		if second > 0 {
			_ = syscall.Kill(-second, syscall.SIGKILL)
		}
	})
	if err := a.TerminateApp(exe, "com.example.game"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if dhAlive(first) {
		t.Fatalf("first launch (pid %d) still running after TerminateApp; spyder no longer tracks it (second pid %d)", first, second)
	}
}
```

```
--- FAIL: TestDefectHunt_DesktopRelaunchDoesNotOrphanFirstProcess (0.36s)
    first launch (pid 66524) still running after TerminateApp; spyder no longer tracks it (second pid 66525)
```

- **Smallest fix.** Terminate a live prior process before replacing it, or refuse with "already running".

### DH-64. Every crash report a call pulls is kept in the run archive

- **Severity: medium.** Evidence is lost: when one `crashes` call returns several reports, the run keeps only the last one on disk, while its manifest lists all of them with the wrong sizes.
- **Mechanism.** `archiveArtefact` names each artefact `<source>-<UTC second><ext>`. The crashes loop archives every report within the same second, so each `AddArtefact` overwrites the previous file and appends another manifest entry pointing at the same name. Two screenshots archived within one second collide the same way.
- **Where.** `internal/mcp/tools.go:611-631` (name at `625-626`), loop at `1799-1806`.
- **Reproduction:**

```go
// crashes archives every pulled report into the active run under the
// name crashes-<UTC second>.ips. Several reports pulled by one call share
// that name, so each overwrites the last while the manifest lists all.
func TestDefectHunt_CrashReportsInOneCallOverwriteEachOther(t *testing.T) {
	ios := &stubAdapter{crashes: func(string, time.Time, string) ([]device.CrashReport, error) {
		return []device.CrashReport{
			{Process: "Game", Raw: "first crash"},
			{Process: "Game", Raw: "second crash"},
			{Process: "Game", Raw: "third crash"},
		}, nil
	}}
	h, _ := defectHuntHandler(t, ios)
	store, err := runs.New(filepath.Join(t.TempDir(), "runs"))
	if err != nil {
		t.Fatal(err)
	}
	h.runs = store
	run, err := store.Open("iPad", "job-A", "")
	if err != nil {
		t.Fatal(err)
	}
	if res, err := h.Dispatch(context.Background(), "crashes", map[string]any{"device": "iPad", "owner": "job-A"}); err != nil || res.IsError {
		t.Fatalf("crashes: %v %+v", err, res)
	}
	got, err := store.Get(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, a := range got.Artefacts {
		data, _ := os.ReadFile(filepath.Join(store.BaseDir(), run.ID, a.Name))
		seen[string(data)] = true
	}
	if len(got.Artefacts) != 3 || len(seen) != 3 {
		t.Errorf("manifest lists %d crash artefacts but only %d distinct reports survive on disk: %v",
			len(got.Artefacts), len(seen), seen)
	}
}
```

```
--- FAIL: TestDefectHunt_CrashReportsInOneCallOverwriteEachOther (0.00s)
    manifest lists 3 crash artefacts but only 1 distinct reports survive on disk: map[third crash:true]
```

- **Smallest fix.** Make names unique in `AddArtefact` (append a per-run sequence number, or the report's own file name for crashes) and refuse to overwrite an existing artefact.

### DH-65. A Verify shell step's evidence always includes its last lines

- **Severity: medium.** Verification evidence is silently truncated: the end of a test run's output (usually the summary and the failure) is missing from the step's recorded output and report, although the step's exit code is right.
- **Mechanism.** `DefaultShell` reads the stdout pipe in one goroutine and calls `cmd.Wait()` in another. `Wait` closes the read end as soon as the shell exits (os/exec: "it is incorrect to call Wait before all reads from the pipe have completed"). Whatever is still in the pipe buffer at that moment is lost. The real per-line `emit` (open, append and close `events.log`) is slow enough for a fast-printing step to finish first.
- **Where.** `internal/verify/exec.go:59-102` (Wait at `88`).
- **Reproduction** (`internal/verify/zz_defecthunt_test.go`):

```go
// DefaultShell reads the step's stdout pipe in one goroutine while
// another calls cmd.Wait(), which closes that pipe as soon as the shell
// exits ("it is incorrect to call Wait before all reads from the pipe
// have completed" — os/exec). Output still buffered in the pipe when the
// step exits is lost from the step's evidence.
func TestDefectHunt_ShellStepLosesTailOfOutput(t *testing.T) {
	// ~48 KiB: fits the pipe buffer, so the shell exits without waiting
	// for the reader.
	cmd := `i=0; while [ $i -lt 4000 ]; do echo "line-$i-padding-padding"; i=$((i+1)); done; echo LAST`
	lost := 0
	const runs = 10
	for n := 0; n < runs; n++ {
		// Emit does what Run.emit does per line: append to events.log
		// with open/write/close. That per-line cost lets the shell finish
		// before the reader drains.
		events := filepath.Join(t.TempDir(), "events.log")
		emit := func(line string) {
			f, err := os.OpenFile(events, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err == nil {
				fmt.Fprintln(f, line)
				f.Close()
			}
		}
		res := DefaultShell(context.Background(), StepRequest{Step: Step{Command: cmd}, Cwd: t.TempDir(), Emit: emit})
		if res.Code != 0 {
			t.Fatalf("exit %d: %s", res.Code, res.Output)
		}
		if !strings.Contains(res.Output, "LAST") {
			lost++
		}
	}
	if lost > 0 {
		t.Errorf("%d of %d successful runs lost the tail of their output (no LAST line)", lost, runs)
	}
}
```

```
--- FAIL: TestDefectHunt_ShellStepLosesTailOfOutput (0.43s)
    10 of 10 successful runs lost the tail of their output (no LAST line)
```

- **Smallest fix.** Wait for `readDone` before calling `cmd.Wait()` (read to EOF first), and on timeout kill the group, then drain, then `Wait`. If grandchildren may hold the pipe open, set `cmd.WaitDelay`.

### DH-66. No instant exists at which two owners both hold a device

- **Severity: low.** It needs the old holder to act at the exact expiry instant, but it is a real hole in the lease invariant.
- **Mechanism.** `Acquire` treats a hold as live only when `now.Before(ExpiresAt)`, while `Authorize`, `Release`, `Renew` and `Get` treat it as expired only when `now.After(ExpiresAt)`. At `now == ExpiresAt` a new owner is granted the device and the old owner is still authorised.
- **Where.** `internal/reservations/reservations.go:146` against `193, 227, 256, 285, 309`.
- **Reproduction:**

```go
// Acquire treats now == ExpiresAt as expired (now.Before(exp) is false),
// while Authorize treats it as live (now.After(exp) is false). At that
// instant the old holder is still authorised to mutate and a new owner
// is granted the device.
func TestDefectHunt_ExpiryInstantAuthorisesTwoOwners(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	s, _ := New("", WithNow(func() time.Time { return now }))
	if _, err := s.Acquire("iPad", "job-A", time.Hour, ""); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour) // exactly ExpiresAt
	aOK := s.Authorize("iPad", "job-A") == nil
	_, bErr := s.Acquire("iPad", "job-B", time.Hour, "")
	if aOK && bErr == nil {
		t.Errorf("at the expiry instant job-A is authorised and job-B was granted iPad")
	}
}
```

```
--- FAIL: TestDefectHunt_ExpiryInstantAuthorisesTwoOwners (0.00s)
    at the expiry instant job-A is authorised and job-B was granted iPad
```

- **Smallest fix.** Use one predicate everywhere, for example `live := now.Before(r.ExpiresAt)`, and treat `!live` as expired in all five places.

### DH-67. A finished Verify shell step leaves no processes behind

- **Severity: low.** A step that starts a helper in the background (a log tail, a local server, `adb logcat`) leaves it running after the step, the run and even the daemon, holding devices or ports.
- **Mechanism.** `DefaultShell` puts the step in its own process group but kills the group only on timeout; on a normal exit nothing signals the group.
- **Where.** `internal/verify/exec.go:91-98`, `113-125`.
- **Reproduction:**

```go
// A shell step that starts a background process and exits is reported
// finished, but its process group is only killed on timeout. The
// background process keeps running after the step (and the daemon).
func TestDefectHunt_FinishedShellStepLeavesProcessesRunning(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "bg.pid")
	cmd := "sleep 30 & echo $! > " + pidFile + "; echo started"
	res := DefaultShell(context.Background(), StepRequest{Step: Step{Command: cmd}, Cwd: t.TempDir()})
	if res.Code != 0 {
		t.Fatalf("exit %d: %s", res.Code, res.Output)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	time.Sleep(200 * time.Millisecond)
	if syscall.Kill(pid, 0) == nil {
		t.Errorf("step finished (exit 0) but its background process %d is still running", pid)
	}
}
```

```
--- FAIL: TestDefectHunt_FinishedShellStepLeavesProcessesRunning (0.21s)
    step finished (exit 0) but its background process 3862 is still running
```

- **Smallest fix.** After `Wait` returns on the normal path, call `killProcessGroup(cmd)` as well (SIGTERM, short grace, SIGKILL to `-pgid`).

### DH-68. Tunnel `Stop` kills the whole process group it created

- **Severity: low.** The real `ios tunnel` does not appear to fork today, so nothing is known to leak yet.
- **Mechanism.** The tunnel is started with `Setpgid`, but `Stop` signals only the leader's pid.
- **Where.** `internal/iostunnel/supervisor.go:155`, `199`, `223`, `228`.
- **Reproduction:**

```go
// Stop runs the tunnel in its own process group (Setpgid) but signals
// only the leader, so anything the tunnel spawned survives Stop.
func TestDefectHunt_StopKillsTunnelProcessGroup(t *testing.T) {
	dir, port := defectHuntEnv(t)
	childPath := filepath.Join(dir, "child")
	bin := filepath.Join(dir, "fake-ios")
	// The grandchild ignores nothing; it simply outlives a leader-only kill.
	script := "#!/bin/sh\n(while :; do sleep 1; done) &\necho $! > " + childPath + "\ntrap 'exit 0' TERM\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	sup := New(bin, "127.0.0.1", port)
	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(readPIDs(t, childPath)) < 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	child := readPIDs(t, childPath)
	if len(child) != 1 {
		t.Fatal("grandchild did not start")
	}
	if err := sup.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if alive(child[0]) {
		t.Fatalf("tunnel Stop returned but grandchild pid %d is still running", child[0])
	}
}
```

```
--- FAIL: TestDefectHunt_StopKillsTunnelProcessGroup (2.16s)
    tunnel Stop returned but grandchild pid 79464 is still running
```

- **Smallest fix.** Signal `-pid` (`syscall.Kill(-pid, SIGTERM)`, then `SIGKILL`).

### DH-69. A second concurrent agent `Shutdown` waits for the resident sessions to end

- **Severity: low.** On shutdown the daemon can exit before the DeviceKit `ios runwda` child is SIGKILLed; the next daemon's orphan reaper partly cleans up.
- **Mechanism.** `StopAgents` runs twice at shutdown. `stopHold` deletes the hold from the map before waiting on it, so the second `Shutdown` finds nothing and returns while the first is still waiting; `Run` then exits before the session's 8 s `WaitDelay` can SIGKILL a slow runwda.
- **Where.** `internal/device/ios_agent.go:1029-1045`; `internal/daemon/daemon.go:249`, `287`.
- **Reproduction** (`internal/device/zz_defecthunt_agent_shutdown_test.go`):

```go
// On SIGTERM the daemon calls StopAgents twice at once: superviseIOSAgents'
// ctx.Done branch (daemon.go:287) and Run's shutdown (daemon.go:249). Both
// reach AutomationHost.Shutdown, and stopHold deletes the hold before
// waiting on it, so the second Shutdown finds nothing and returns while
// the first is still waiting for the resident session (the DeviceKit
// `ios runwda` child, Setpgid) to end. Run then stops the tunnel and
// exits, so the 8 s WaitDelay that would SIGKILL a slow runwda never
// runs. A Shutdown that returns must mean the sessions are gone.
func TestDefectHunt_ConcurrentAgentShutdownWaitsForSessions(t *testing.T) {
	h := NewAutomationHost(nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	h.holds["DEFECTHUNT-UDID"] = &runnerHold{cancel: cancel, done: done}
	// The session takes a second to wind down after cancel, as runwda
	// does while it closes the XCTest session.
	go func() { <-ctx.Done(); time.Sleep(time.Second); close(done) }()

	firstDone := make(chan struct{})
	go func() { h.Shutdown(); close(firstDone) }() // daemon.go:287
	time.Sleep(100 * time.Millisecond)
	h.Shutdown() // daemon.go:249
	select {
	case <-done:
	default:
		<-firstDone
		t.Fatal("second Shutdown returned while the resident session was still running")
	}
	<-firstDone
}
```

```
--- FAIL: TestDefectHunt_ConcurrentAgentShutdownWaitsForSessions (1.00s)
    second Shutdown returned while the resident session was still running
```

- **Smallest fix.** Keep the hold in the map marked "stopping" until its `done` closes, so every caller waits on the same `done`.

### DH-70. The liveness heartbeat never clears an outstanding stall

- **Severity: low.** A wedged-but-alive stall vanishes from `spyder status` and the health surface within 5 s although the stuck operation is still outstanding, and `Check` fires only once per stall, so it never returns.
- **Mechanism.** The 5 s heartbeat calls `Observe(daemon/spyder, true)` on the same entity the `ProgressWatchdog` uses, and `Observe(true)` moves any state to Healthy.
- **Where.** `internal/daemon/health.go:39-50`; `internal/health/watchdog.go:116`.
- **Reproduction** (`internal/daemon/zz_defecthunt_selfheal_test.go`; `PATH` holds only a no-op `terminal-notifier`):

```go
// startHealthWiring's 5 s heartbeat calls Observe(daemon/spyder, true)
// unconditionally, and EnableSelfHeal's ProgressWatchdog reports stalls
// on that same entity. Observe(true) moves any state to Healthy, so a
// wedged-but-alive stall is cleared (and its notification dismissed)
// within five seconds while the stuck operation is still outstanding.
// ProgressWatchdog.Check fires once per stall, so it never comes back.
func TestDefectHunt_HeartbeatDoesNotClearOutstandingStall(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SPYDER_DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("HOME", filepath.Join(dir, "home"))
	// No real notifications: a no-op terminal-notifier is the only one on PATH.
	if err := os.WriteFile(filepath.Join(dir, "terminal-notifier"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup := health.NewSupervisor(health.New())
	startHealthWiring(ctx, sup, false)                                  // daemon.go:173
	w := health.NewProgressWatchdog(sup.Model(), "spyder", time.Second) // mcp/server.go:336

	w.Begin()
	if !w.Check(time.Now().Add(2 * time.Second)) {
		t.Fatal("watchdog did not flag the stall")
	}
	snap, _ := sup.Model().Get(daemonSelfHealthID)
	if snap.State != health.NeedsAttention {
		t.Fatalf("after stall: %s", snap.State)
	}

	time.Sleep(6 * time.Second) // one heartbeat; the operation is still outstanding
	snap, _ = sup.Model().Get(daemonSelfHealthID)
	if snap.State != health.NeedsAttention {
		t.Fatalf("stall still outstanding but daemon-self is %s: the liveness heartbeat cleared it", snap.State)
	}
}
```

```
--- FAIL: TestDefectHunt_HeartbeatDoesNotClearOutstandingStall (6.02s)
    stall still outstanding but daemon-self is healthy: the liveness heartbeat cleared it
```

- **Smallest fix.** Use a separate health entity for the heartbeat, or skip it while the watchdog has a stall outstanding.

### DH-71. Each adb server restart never leaks a goroutine for the daemon's lifetime

- **Severity: low.** It leaks one goroutine and one closed connection per Unity `kill-server`.
- **Mechanism.** Every successful `track` starts `go func(){ <-ctx.Done(); conn.Close() }()` on the daemon's context, and that goroutine outlives the connection.
- **Where.** `internal/adbserver/adbserver.go:171-175`.
- **Reproduction:**

```go
// Every successful track() starts a goroutine that waits for the
// supervisor's ctx (the daemon's lifetime) to close the connection. Each
// adb server restart (every Unity build's kill-server) leaks one for the
// life of the daemon.
func TestDefectHunt_TrackDoesNotLeakAGoroutinePerRestart(t *testing.T) {
	const restarts = 40
	addr, accepted := flakyServer(t, 5*time.Millisecond, restarts)
	bin, _ := fakeADB(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	before := runtime.NumGoroutine()
	go func() {
		New(Args{Adb: bin, Addr: addr, Evidence: func() string { return "" }}).Run(ctx)
		close(done)
	}()
	deadline := time.Now().Add(60 * time.Second)
	for accepted.Load() <= restarts && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	during := runtime.NumGoroutine()
	cancel()
	<-done
	if accepted.Load() <= restarts {
		t.Fatalf("only %d connections in 60s", accepted.Load())
	}
	// Run itself, its one live track watcher, and the fake server's
	// goroutines account for a handful.
	if grown := during - before; grown > 10 {
		t.Fatalf("after %d adb server restarts the supervisor holds %d extra goroutines (want a bounded few)", restarts, grown)
	}
}
```

```
--- FAIL: TestDefectHunt_TrackDoesNotLeakAGoroutinePerRestart (0.95s)
    after 40 adb server restarts the supervisor holds 42 extra goroutines (want a bounded few)
```

- **Smallest fix.** Use `stop := context.AfterFunc(ctx, func() { _ = conn.Close() })` and call `stop()` after `drain`.

### DH-72. A released instance keeps its full linger after a re-acquire

- **Severity: low.** An instance is quit early, but only after its last holder released it.
- **Mechanism.** Each `Release` arms its own `time.AfterFunc`; a stale timer from an earlier release fires after a later release.
- **Where.** `internal/appchannel/factory.go:139`.
- **Reproduction:**

```go
// Release promises an idle instance survives the linger window "unless it's
// re-acquired first". The timer armed by an earlier release must not cut the
// linger of a later one: release, re-acquire, release again, and the
// instance is quit by the first timer, long before its new linger expires.
func TestDefectHunt_ReleaseHonoursLingerAfterReacquire(t *testing.T) {
	m, l, addr := dhStart(t)
	t.Cleanup(func() { dhStopBounded(l) })
	var (
		mu   sync.Mutex
		inst *dhApp
	)
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		if inst != nil {
			_ = inst.conn.Close()
		}
	})
	factoryConn := dhHello(t, addr, "factory", MethodSpawnInstance)
	ok, _ := PackParams(map[string]any{"ok": true})
	dhServe(factoryConn, func(env *Envelope) *Envelope {
		if env.Method != MethodSpawnInstance {
			return nil
		}
		go func() {
			a, _ := dhDialApp(addr, "instance", MethodPing, MethodQuit)
			mu.Lock()
			inst = a
			mu.Unlock()
		}()
		return &Envelope{ID: env.ID, Result: ok}
	})
	factory := dhWaitSession(t, m, "factory")

	const linger = 400 * time.Millisecond
	pool := NewInstancePool(m, WithLinger(linger))
	s1, err := pool.Acquire(context.Background(), factory, "tiltbuggy", "alice", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	pool.Release(s1.ID) // arms a timer for +400ms
	time.Sleep(200 * time.Millisecond)
	s2, err := pool.Acquire(context.Background(), factory, "tiltbuggy", "bob", 5*time.Second)
	if err != nil || s2.ID != s1.ID {
		t.Fatalf("re-acquire did not reuse the idle instance: %v %v", s2, err)
	}
	time.Sleep(150 * time.Millisecond)
	released := time.Now()
	pool.Release(s2.ID) // the instance must now live until released+linger

	mu.Lock()
	a := inst
	mu.Unlock()
	for time.Since(released) < linger-50*time.Millisecond {
		select {
		case meth := <-a.got:
			if meth == MethodQuit {
				t.Fatalf("instance quit %v after bob released it; linger is %v",
					time.Since(released).Round(time.Millisecond), linger)
			}
		case <-time.After(10 * time.Millisecond):
		}
	}
}
```

```
--- FAIL: TestDefectHunt_ReleaseHonoursLingerAfterReacquire (0.45s)
    instance quit 50ms after bob released it; linger is 400ms
```

- **Smallest fix.** Keep a generation counter per instance and have `gcIfIdle` ignore stale timers.

### DH-73. A simulator lease is released exactly once

- **Severity: low.** A `sim_release` racing the reaper releases the lease twice: `OnRelease` fires twice and (in the reader's run) the simulator was also shut down twice.
- **Where.** `internal/simlease/simlease.go:557-588`.
- **Reproduction:**

```go
// A lease is released once: an explicit sim_release racing the reaper
// must not undo the simulator twice or announce two releases.
func TestDefectHunt_ConcurrentReleaseAndReapReleaseOnce(t *testing.T) {
	f := &fakeSimctl{}
	f.add("AAAAAAAA-0000-4000-8000-000000000001", "shut", iphone16, runtime18, "Shutdown")
	d := &dhSimctl{fakeSimctl: f}
	var released atomic.Int32
	var nowNS atomic.Int64
	nowNS.Store(time.Unix(1_800_000_000, 0).UnixNano())
	m, err := New(&Args{Simctl: d, Now: func() time.Time { return time.Unix(0, nowNS.Load()) },
		OnRelease: func(Released) { released.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	l, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "a", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	nowNS.Add(int64(2 * time.Minute))

	// Both undo passes list before either finishes.
	var arrived sync.WaitGroup
	arrived.Add(2)
	d.hookMu.Lock()
	d.beforeList = func() { arrived.Done(); arrived.Wait() }
	d.hookMu.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); m.Reap(ctx) }()
	go func() { defer wg.Done(); _, _ = m.Release(ctx, l.ID, "a") }()
	wg.Wait()

	shutdowns := 0
	for _, c := range f.mutations() {
		if c == "shutdown "+l.UDID {
			shutdowns++
		}
	}
	if got := released.Load(); got != 1 || shutdowns != 1 {
		t.Fatalf("one lease released %d times, simulator shut down %d times (mutations %v)", got, shutdowns, f.mutations())
	}
}
```

```
--- FAIL: TestDefectHunt_ConcurrentReleaseAndReapReleaseOnce (0.00s)
    one lease released 2 times, simulator shut down 1 times (mutations [boot … shutdown …])
```

- **Smallest fix.** The same `Releasing` marker as above; the second caller gets the first caller's result or `ErrNotFound`.

### DH-74. A huge `ttl_s` clamps to the 12 h maximum

- **Severity: low.** `ttl_s=1e10` silently gives a 30-minute lease, and `sim_renew` with it keeps the old TTL. The reservation verbs (`reserve`, `renew`) use the same `time.Duration(seconds) * time.Second` pattern and fall back to the 1 h default the same way.
- **Mechanism.** `time.Duration(optNumber(...)) * time.Second` overflows `int64` and goes negative, which the lease code treats as "use the default".
- **Where.** `internal/mcp/sim_lease.go:81`, `128`; same pattern at `internal/mcp/tools.go:1119`, `1335`.
- **Reproduction** (`internal/mcp/zz_defecthunt_simlease_ttl_test.go`; uses the existing `leaseSimctl` helpers):

```go
// A huge ttl_s is clamped to the documented maximum (43200 s), not
// wrapped by int64 overflow into a negative duration that silently
// becomes the 30-minute default.
func TestDefectHunt_HugeTTLClampsToMax(t *testing.T) {
	t.Setenv("SPYDER_DATA_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	now := time.Unix(1_800_000_000, 0)
	m, err := simlease.New(&simlease.Args{Simctl: &leaseSimctl{state: "Shutdown"}, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{simLeases: m} // no simulator discovery, no device adapters

	res, err := h.handleSimAcquire(map[string]any{
		"device_type": leaseTestType, "runtime": leaseTestRuntime, "owner": "agent", "ttl_s": float64(1e10),
	})
	if err != nil || res.IsError {
		t.Fatalf("sim_acquire: %v %s", err, leaseText(res))
	}
	var l simlease.Lease
	if err := json.Unmarshal([]byte(leaseText(res)), &l); err != nil {
		t.Fatal(err)
	}
	if want := int64(simlease.MaxTTL / time.Second); l.TTLSeconds != want {
		t.Fatalf("ttl_s=1e10 gave a %d s lease (expires %s); want the %d s maximum", l.TTLSeconds, l.ExpiresAt, want)
	}

	// sim_renew with the same value should extend to the maximum too.
	res, err = h.handleSimRenew(map[string]any{"lease_id": l.ID, "owner": "agent", "ttl_s": float64(1e10)})
	if err != nil || res.IsError {
		t.Fatalf("sim_renew: %v %s", err, leaseText(res))
	}
	if err := json.Unmarshal([]byte(leaseText(res)), &l); err != nil {
		t.Fatal(err)
	}
	if want := int64(simlease.MaxTTL / time.Second); l.TTLSeconds != want {
		t.Fatalf("sim_renew ttl_s=1e10 left a %d s lease; want %d", l.TTLSeconds, want)
	}
}
```

```
--- FAIL: TestDefectHunt_HugeTTLClampsToMax (0.00s)
    ttl_s=1e10 gave a 1800 s lease (expires 2027-01-15 19:30:00 +1100 AEDT); want the 43200 s maximum
```

- **Smallest fix.** Clamp the seconds value to `MaxTTL/time.Second` before multiplying.

### DH-75. Pool adoption takes each device's template from that device's own ledger row

- **Severity: low.** Latent: ledger rows are written only by `Pool.Acquire`, which no production code calls today. Unheld devices get a template contrary to the documented "leave alone", and held devices can get another device's template depending on map order.
- **Where.** `internal/pool/pool.go:390-408`, `256`, `346-349`.
- **Reproduction:**

```go
// Adopt's documented rule: with several templates for a platform and no
// ledger entry for the device, the device is left alone. A ledger entry
// for some *other* device must not decide this one's template, and a
// held device must get its own ledger template.
func TestDefectHunt_AdoptAttributesTemplatesByDevice(t *testing.T) {
	cfg := func() *Config {
		phone := iOSTemplate("phone")
		tablet := iOSTemplate("tablet")
		tablet.DeviceType = "com.apple.CoreSimulator.SimDeviceType.iPad-Pro-13-inch-M4-8GB"
		return &Config{Templates: []TemplateConfig{phone, tablet}}
	}

	t.Run("unheld device is left alone", func(t *testing.T) {
		fe := newFakeExec()
		fe.sims["SIM-A"], fe.simNames["SIM-A"] = "Booted", "spyder-pool-aaaaaaaa"
		fe.sims["SIM-B"], fe.simNames["SIM-B"] = "Shutdown", "spyder-pool-bbbbbbbb"
		store := dhStore(t)
		if err := store.Put(poolstore.Hold{InstanceID: "inst-a", DeviceID: "SIM-A", Template: "tablet", Platform: "ios", Holder: "job-1", AcquiredAt: time.Unix(1_800_000_000, 0)}); err != nil {
			t.Fatal(err)
		}
		p := newWithClock(cfg(), fe, newFakeClock(time.Unix(1_800_000_000, 0)), WithStore(store))
		if err := p.Adopt(context.Background()); err != nil {
			t.Fatal(err)
		}
		for _, inst := range p.instances {
			if inst.DeviceID == "SIM-B" {
				t.Fatalf("unheld SIM-B adopted as template %q (tier %s) on the strength of SIM-A's ledger row", inst.Template, inst.Tier)
			}
		}
	})

	t.Run("held devices keep their own template", func(t *testing.T) {
		for i := 0; i < 40; i++ {
			fe := newFakeExec()
			fe.sims["SIM-A"], fe.simNames["SIM-A"] = "Booted", "spyder-pool-aaaaaaaa"
			fe.sims["SIM-C"], fe.simNames["SIM-C"] = "Booted", "spyder-pool-cccccccc"
			store := dhStore(t)
			at := time.Unix(1_800_000_000, 0)
			_ = store.Put(poolstore.Hold{InstanceID: "inst-a", DeviceID: "SIM-A", Template: "tablet", Platform: "ios", Holder: "job-1", AcquiredAt: at})
			_ = store.Put(poolstore.Hold{InstanceID: "inst-c", DeviceID: "SIM-C", Template: "phone", Platform: "ios", Holder: "job-2", AcquiredAt: at})
			p := newWithClock(cfg(), fe, newFakeClock(at), WithStore(store))
			if err := p.Adopt(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"SIM-A": "tablet", "SIM-C": "phone"}
			for _, inst := range p.instances {
				if inst.Template != want[inst.DeviceID] {
					t.Fatalf("run %d: held %s (holder %s) adopted as template %q; its ledger row says %q",
						i, inst.DeviceID, inst.Holder, inst.Template, want[inst.DeviceID])
				}
			}
		}
	})
}
```

```
--- FAIL: TestDefectHunt_AdoptAttributesTemplatesByDevice (0.02s)
    unheld SIM-B adopted as template "tablet" (tier available) on the strength of SIM-A's ledger row
    run 0: held SIM-C (holder job-2) adopted as template "tablet"; its ledger row says "phone"
```

- **Smallest fix.** Use `holdsByDevice[deviceID].Template` when the device has a row; otherwise the single platform match, or leave the device alone.

### DH-76. A failed simulator listing never drops pool ledger holds

- **Severity: low.** Latent for the same reason. When the listing errors, every hold is pruned as "device vanished".
- **Where.** `internal/pool/pool.go:247-250`, `273-276`, `301-314`.
- **Reproduction:**

```go
// A failed listing proves nothing about which devices exist: Adopt must
// not drop ledger holds because SimList errored.
func TestDefectHunt_AdoptListFailureKeepsHolds(t *testing.T) {
	d := &dhExec{fakeExec: newFakeExec(), simListErr: errors.New("simctl: CoreSimulatorService connection interrupted")}
	d.sims["SIM-A"], d.simNames["SIM-A"] = "Booted", "spyder-pool-aaaaaaaa"
	store := dhStore(t)
	if err := store.Put(poolstore.Hold{InstanceID: "inst-a", DeviceID: "SIM-A", Template: "iphone16", Platform: "ios", Holder: "job-1", AcquiredAt: time.Unix(1_800_000_000, 0)}); err != nil {
		t.Fatal(err)
	}
	p := newWithClock(&Config{Templates: []TemplateConfig{iOSTemplate("iphone16")}}, d, newFakeClock(time.Unix(1_800_000_000, 0)), WithStore(store))
	_ = p.Adopt(context.Background())

	holds, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(holds) != 1 {
		t.Fatalf("SimList failed during adopt, and the ledger now holds %d holds (want job-1's hold on SIM-A kept)", len(holds))
	}
}
```

```
--- FAIL: TestDefectHunt_AdoptListFailureKeepsHolds (0.01s)
    SimList failed during adopt, and the ledger now holds 0 holds (want job-1's hold on SIM-A kept)
```

- **Smallest fix.** Prune only platforms whose listing succeeded, and retry adoption.

### DH-77. `Pool.Acquire` never shares its internal instance with the linger goroutine

- **Severity: low.** Latent: a data race between the caller and the linger timer on the returned `*Instance`.
- **Where.** `internal/pool/pool.go:415-461` (write in `shutdownInstance` at `974`).
- **Reproduction:**

```go
// Acquire hands out the pool's internal *Instance, which the linger timer
// goroutine later mutates; a caller reading its own instance races it.
// (Run with -race.)
func TestDefectHunt_AcquiredInstanceIsNotSharedWithLingerGoroutine(t *testing.T) {
	fe := newFakeExec()
	fc := newFakeClock(time.Unix(1_800_000_000, 0))
	p := newWithClock(&Config{Templates: []TemplateConfig{iOSTemplate("iphone16")}}, fe, fc)
	inst, err := p.Acquire("iphone16", "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Release(inst.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { fc.Advance(time.Hour); close(done) }()
	for {
		select {
		case <-done:
			return
		default:
			if inst.Tier == "bogus" { // the caller still looking at what Acquire returned
				t.Log("unreachable")
			}
		}
	}
}
```

```
WARNING: DATA RACE
--- FAIL: TestDefectHunt_AcquiredInstanceIsNotSharedWithLingerGoroutine (0.00s)
    testing.go:1867: race detected during execution of test
```

- **Smallest fix.** Return a copy.

### DH-78. In-memory pool stores are independent of each other

- **Severity: low.** Tests only: `Open("")` uses `file::memory:?cache=shared`, so every in-memory store in a process is one database and holds leak between tests.
- **Where.** `internal/poolstore/poolstore.go:41`.
- **Reproduction** (`internal/poolstore/zz_defecthunt_poolstore_test.go`):

```go
// Open("") is documented as "an in-memory store (tests)". Two such stores
// must be independent; with the shared-cache DSN they are one database,
// so holds leak between stores (and between tests in one process).
func TestDefectHunt_InMemoryStoresAreIndependent(t *testing.T) {
	a, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if err := a.Put(Hold{InstanceID: "dh-inst", DeviceID: "dh-dev", Template: "t", Platform: "ios", Holder: "h", AcquiredAt: time.Unix(1_800_000_000, 0)}); err != nil {
		t.Fatal(err)
	}
	defer a.Delete("dh-inst")
	holds, err := b.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(holds) != 0 {
		t.Fatalf("a fresh in-memory store sees %d hold(s) written to a different store: %+v", len(holds), holds)
	}
}
```

```
--- FAIL: TestDefectHunt_InMemoryStoresAreIndependent (0.01s)
    a fresh in-memory store sees 1 hold(s) written to a different store: […]
```

- **Smallest fix.** Use a unique name per open: `file:mem-<rand>?mode=memory&cache=shared`.

### DH-79. iOS `List` returns one row per device

- **Severity: low.** A phone that is both cabled and paired over Wi-Fi appears twice (usbmux reports one entry per connection), which double-counts it in `devices` and selector candidates.
- **Where.** `internal/device/ios.go:683-707`.
- **Reproduction:**

```go
// usbmuxd lists one entry per connection: a phone that is cabled and also
// paired over Wi-Fi appears twice with the same SerialNumber
// (ConnectionType USB and Network). List appends both.
func TestDefectHunt_IOSListOneRowPerUDID(t *testing.T) {
	dhFakeTools(t, "")
	const udid = "00008110-001A2C3E0A12801E"
	usb := goios_ios.DeviceEntry{DeviceID: 3}
	usb.Properties.SerialNumber = udid
	usb.Properties.ConnectionType = "USB"
	wifi := goios_ios.DeviceEntry{DeviceID: 7}
	wifi.Properties.SerialNumber = udid
	wifi.Properties.ConnectionType = "Network"
	a := &IOSAdapter{
		listDevices: func() (goios_ios.DeviceList, error) {
			return goios_ios.DeviceList{DeviceList: []goios_ios.DeviceEntry{usb, wifi}}, nil
		},
		deviceValues: func(goios_ios.DeviceEntry) (goios_ios.GetAllValuesResponse, error) {
			var v goios_ios.GetAllValuesResponse
			v.Value.ProductVersion = "18.1"
			v.Value.DeviceName = "Phone"
			return v, nil
		},
		devicectlList: func(context.Context) ([]byte, error) {
			return nil, context.DeadlineExceeded
		},
	}
	list, err := a.List()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, d := range list {
		if d.UUID == udid {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("List has %d rows for %s, want 1: %+v", n, udid, list)
	}
}
```

```
--- FAIL: TestDefectHunt_IOSListOneRowPerUDID (0.00s)
    List has 2 rows for 00008110-001A2C3E0A12801E, want 1: […]
```

- **Smallest fix.** De-duplicate by `SerialNumber`, preferring the `USB` entry.

### DH-80. The desktop adapter writes a process's pid without a data race

- **Severity: low.** The race detector reports it, and the first log lines of a fast-printing app carry pid 0.
- **Mechanism.** The output scanners start before `cmd.Start` and read `p.pid` while `LaunchApp` writes it.
- **Where.** `internal/device/desktop.go:164`, `362`.
- **Reproduction:**

```go
// LaunchApp starts the stdout/stderr scanners before cmd.Start (ENT-002)
// and only then writes p.pid; scan reads p.pid for every line without a
// lock. Run with -race: an app that prints at once is a data race (and
// its first lines carry pid 0 / pid unavailable).
func TestDefectHunt_DesktopPIDRace(t *testing.T) {
	dhFakeTools(t, "")
	exe := dhScript(t, "echo ready\necho ready >&2\nexec sleep 2\n")
	a := NewDesktopAdapter(nil)
	if err := a.LaunchApp(exe, "com.example.game", nil); err != nil {
		t.Fatal(err)
	}
	pid, _ := a.AppPID(exe, "com.example.game")
	t.Cleanup(func() {
		if pid > 0 {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	})
	time.Sleep(500 * time.Millisecond)
	lines, _ := a.LogRange(exe, LogFilter{}, time.Time{}, time.Time{})
	for _, ll := range lines {
		if ll.Diagnostic == nil || ll.Diagnostic.PID != pid {
			got := 0
			if ll.Diagnostic != nil {
				got = ll.Diagnostic.PID
			}
			t.Errorf("line %q carries pid %d, want %d", ll.Message, got, pid)
		}
	}
}
```

```
WARNING: DATA RACE
--- FAIL: TestDefectHunt_DesktopPIDRace (0.50s)
```

- **Smallest fix.** Store the pid in an `atomic.Int64`, or set it under `p.mu`.

### DH-81. The Android 9 `FLAG_SECURE` format is detected

- **Severity: low.** On Android 9 and earlier, `screen_secure` reports `not_secure` for a secure window. The format is from the reader's knowledge of Android ≤ 9 `dumpsys window` output, not from a captured device.
- **Mechanism.** Only a line that starts with `fl=` is read; Android ≤ 9 prints `fl=#81812100` inside the `mAttrs=` line.
- **Where.** `internal/device/android_ui.go:641-642`, `683-693`.
- **Reproduction:**

```go
// Android 9 and earlier print LayoutParams inline with hex flags:
// "mAttrs=WM.LayoutParams{(0,0)(fillxfill) sim=#110 ty=1 fl=#81812100 ...}".
// 0x81812100 has FLAG_SECURE (0x2000). ParseDumpsysWindowSecure only reads
// a line that starts with "fl=", so screen_secure says not_secure.
func TestDefectHunt_AndroidPSecureFlagDetected(t *testing.T) {
	const out = `WINDOW MANAGER WINDOWS (dumpsys window windows)
  Window #3 Window{a1b2c3d u0 com.example.secure/com.example.secure.MainActivity}:
    mDisplayId=0 stackId=12 mSession=Session{7e1f2a3 12345:u0a10123} mClient=android.os.BinderProxy@4c5d6e7
    mOwnerUid=10123 mShowToOwnerOnly=true package=com.example.secure appop=NONE
    mAttrs=WM.LayoutParams{(0,0)(fillxfill) sim=#110 ty=1 fl=#81812100 wanim=0x10302fe needsMenuKey=2 colorMode=0}
    Requested w=1080 h=2160 mLayoutSeq=312
  mCurrentFocus=Window{a1b2c3d u0 com.example.secure/com.example.secure.MainActivity}
`
	info := ParseDumpsysWindowSecure(out)
	if info.FocusedHash != "a1b2c3d" {
		t.Fatalf("focused hash = %q", info.FocusedHash)
	}
	if !info.FocusedSecure {
		t.Fatalf("FocusedSecure = false for fl=#81812100 (FLAG_SECURE 0x2000 set); info=%+v", info)
	}
}
```

```
--- FAIL: TestDefectHunt_AndroidPSecureFlagDetected (0.00s)
    FocusedSecure = false for fl=#81812100 (FLAG_SECURE 0x2000 set); …
```

- **Smallest fix.** Also match ` fl=#?<hex>` inside the `mAttrs=` line.

### DH-82. Android logcat crash timestamps are read as device-local time

- **Severity: low.** West of UTC, a crash after `since` is dropped; in Sydney the reported times are 11 hours wrong.
- **Mechanism.** The crash parser uses `time.Parse` (UTC), while everything else treats logcat time as local.
- **Where.** `internal/device/android.go:666`.
- **Reproduction:**

```go
// logcat prints device-local wall time (parseAndroidLocalTimestamp says so
// and parses in time.Local). parseLogcatCrashBuffer parses the same text as
// UTC, so in a zone west of UTC a crash after `since` is dropped.
func TestDefectHunt_AndroidLogcatCrashTimestampIsDeviceLocal(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip(err)
	}
	prev := time.Local
	time.Local = la
	t.Cleanup(func() { time.Local = prev })

	year := time.Now().Year()
	since := time.Date(year, 11, 19, 9, 0, 0, 0, la)
	const buf = `11-19 10:00:00.000  1234  1234 E AndroidRuntime: FATAL EXCEPTION: main
11-19 10:00:00.001  1234  1234 E AndroidRuntime: Process: com.example.app, PID: 1234
`
	got := parseLogcatCrashBuffer(buf, since, "")
	if len(got) != 1 {
		t.Fatalf("crash at 10:00 local with since=09:00 local: %d reports, want 1", len(got))
	}
	if want := time.Date(year, 11, 19, 10, 0, 0, 0, la); !got[0].Timestamp.Equal(want) {
		t.Fatalf("Timestamp = %v, want %v", got[0].Timestamp, want)
	}
}
```

```
--- FAIL: TestDefectHunt_AndroidLogcatCrashTimestampIsDeviceLocal (0.00s)
    crash at 10:00 local with since=09:00 local: 0 reports, want 1
```

- **Smallest fix.** Use `parseAndroidLocalTimestamp`.

### DH-83. `logcat -t` is sent in device-local time

- **Severity: low.** West of UTC, logs between `since` and the offset are lost; east of UTC, extra lines are read.
- **Mechanism.** `since` is formatted in whatever zone it carries, and the documented RFC 3339 `Z` form arrives as UTC.
- **Where.** `internal/device/android.go:1066` (also `-T` at `618`).
- **Reproduction** (`internal/device/zz_defecthunt_androidlist_test.go`):

```go
// logcat reads -t "MM-DD hh:mm:ss.mmm" as device-local wall time, and
// the decoder parses logcat stamps as host-local (parseAndroidLocalTimestamp).
// LogRange formats `since` in whatever zone it carries; parseTimeArg
// gives UTC for the documented RFC3339 form ("2026-05-17T16:43:24Z").
// West of UTC that asks logcat for a later start than since, and the
// lines between are lost.
func TestDefectHunt_AndroidLogRangeSinceIsDeviceLocal(t *testing.T) {
	dhFakeTools(t, "")
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip(err)
	}
	prev := time.Local
	time.Local = la
	t.Cleanup(func() { time.Local = prev })

	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	bin := filepath.Join(dir, "logcat")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > '" + argsFile + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	a := NewAndroidAdapter()
	a.logcatBin = bin
	since := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC) // 2026-11-19 16:00 in LA
	if _, err := a.LogRange("R5CT1234ABC", LogFilter{}, since, time.Time{}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")
	for i := range args {
		if args[i] == "-t" && i+1 < len(args) {
			if want := "11-19 16:00:00.000"; args[i+1] != want {
				t.Fatalf("logcat -t %q for since=%s; the device reads it as local time, want %q", args[i+1], since.Format(time.RFC3339), want)
			}
			return
		}
	}
	t.Fatalf("no -t in logcat args %q", args)
}
```

```
--- FAIL: TestDefectHunt_AndroidLogRangeSinceIsDeviceLocal (0.25s)
    logcat -t "11-20 00:00:00.000" for since=2026-11-20T00:00:00Z; the device reads it as local time, want "11-19 16:00:00.000"
```

- **Smallest fix.** `since.In(time.Local).Format(...)`.

### DH-84. Android tombstones are attributed to the process, not the crashing thread

- **Severity: low.** Tombstones are readable only on rooted devices; there, `crashes(process=…)` drops native crashes on Android 11+.
- **Mechanism.** The parser takes `name:` from the pid line (the thread) and accepts only `Cmd line:`; Android 11+ writes `Cmdline:`, and the process is between `>>> … <<<`.
- **Where.** `internal/device/android.go:576-598`.
- **Reproduction:**

```go
// Android 11+ tombstones name the process on "Cmdline:" (not "Cmd line:",
// which is ANR-trace syntax), and "name:" on the pid line is the crashing
// thread; the process is between ">>> <<<".
func TestDefectHunt_AndroidTombstoneProcessIsTheCmdline(t *testing.T) {
	const tomb = `*** *** *** *** *** *** *** *** *** *** *** *** *** *** *** ***
Build fingerprint: 'google/raven/raven:13/TQ1A.230105.002/9325679:user/release-keys'
Revision: 'MP1.0'
ABI: 'arm64'
Timestamp: 2026-11-19 14:22:31.123456789+1100
Process uptime: 12s
Cmdline: com.example.app
pid: 12345, tid: 12367, name: RenderThread  >>> com.example.app <<<
uid: 10234
signal 11 (SIGSEGV), code 1 (SEGV_MAPERR), fault addr 0x0000000000000000
`
	cr := parseTombstone([]byte(tomb), time.Now(), "com.example.app")
	if cr == nil {
		got := parseTombstone([]byte(tomb), time.Now(), "")
		t.Fatalf("process filter com.example.app dropped the tombstone; parsed Process = %q (the crashing thread)", got.Process)
	}
	if cr.Process != "com.example.app" {
		t.Fatalf("Process = %q, want com.example.app", cr.Process)
	}
}
```

```
--- FAIL: TestDefectHunt_AndroidTombstoneProcessIsTheCmdline (0.00s)
    process filter com.example.app dropped the tombstone; parsed Process = "RenderThread" (the crashing thread)
```

- **Smallest fix.** Take the process from `>>> (.+?) <<<`, accept `Cmdline:`, never use `name:`.

### DH-85. The `adb devices -l` parser keeps serials that contain spaces

- **Severity: low.** Wireless-debugging serials with a collision suffix (`… (2)._adb-tls-connect._tcp`, relevant to 🎯T196) and `(no serial number)` are listed under ids that `adb -s` rejects.
- **Where.** `internal/device/android.go:64-69` (same split in `ParseForwardList`).
- **Reproduction:**

```go
// `adb devices -l` prints "%-22s %s" (serial, state) and then the
// key:value fields. A wireless-debugging serial whose mDNS instance name
// collided carries " (2)" inside it, and a USB device with no serial is
// listed as "(no serial number)". Splitting on whitespace takes the first
// word as the serial, so spyder lists ids that `adb -s` rejects.
func TestDefectHunt_AndroidListKeepsSerialsWithSpaces(t *testing.T) {
	const listing = "List of devices attached\n" +
		"adb-28131FDH2000N5-fCC1oL (2)._adb-tls-connect._tcp device product:raven model:Pixel_6_Pro device:raven transport_id:3\n" +
		"(no serial number)     unauthorized usb:1-1 transport_id:4\n\n"
	dhFakeTools(t, "'devices -l') printf '"+strings.ReplaceAll(listing, "\n", "\\n")+"'; exit 0;;\n"+
		"*getprop*) echo; exit 0;;\n")
	list, err := NewAndroidAdapter().List()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"adb-28131FDH2000N5-fCC1oL (2)._adb-tls-connect._tcp": false,
		"(no serial number)": false,
	}
	var got []string
	for _, d := range list {
		got = append(got, d.UUID)
		if _, ok := want[d.UUID]; ok {
			want[d.UUID] = true
		}
	}
	for serial, seen := range want {
		if !seen {
			t.Errorf("List UUIDs %q do not include the adb serial %q", got, serial)
		}
	}
}
```

```
--- FAIL: TestDefectHunt_AndroidListKeepsSerialsWithSpaces (0.25s)
    List UUIDs ["adb-28131FDH2000N5-fCC1oL" "(no"] do not include the adb serial "adb-28131FDH2000N5-fCC1oL (2)._adb-tls-connect._tcp"
```

- **Smallest fix.** Split at the known state word with a regular expression.

### DH-86. The install budget covers line-rate transfer on 12 Mb/s links

- **Severity: low.** A 1 GiB install over a degraded USB link (12 or 1.5 Mb/s, exactly what 🎯T131 flags) times out mid-transfer.
- **Mechanism.** `"12 Mb/s"` and `"1.5 Mb/s"` fall to the 4 MiB/s default, faster than the 3 MiB/s assumed for 480 Mb/s.
- **Where.** `internal/installdeadline/deadline.go:100-113`.
- **Reproduction** (`internal/installdeadline/zz_defecthunt_slowlink_test.go`):

```go
// usbspeed reports "12 Mb/s" and "1.5 Mb/s" (USB full/low speed: a bad
// cable or hub, exactly what 🎯T131 flags). effectiveBytesPerSec falls to
// its default of 4 MiB/s for those labels, faster than the 3 MiB/s it
// assumes for a 480 Mb/s link and ~2.8x faster than a 12 Mb/s wire can
// carry, so the budget expires mid-transfer.
func TestDefectHunt_BudgetCoversWireTimeOnFullSpeedUSB(t *testing.T) {
	p := filepath.Join(t.TempDir(), "Game.ipa")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	const size = 1 << 30 // 1 GiB, sparse
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	f.Close()

	wireBitsPerSec := 12e6
	wire := time.Duration(float64(size) * 8 / wireBitsPerSec * float64(time.Second))
	if got := Budget(p, "12 Mb/s", false); got < wire {
		t.Fatalf("Budget(1 GiB, 12 Mb/s) = %v, below the %v the wire needs at line rate", got, wire.Round(time.Second))
	}
	if slow, usb2 := Budget(p, "12 Mb/s", false), Budget(p, "480 Mb/s", false); slow < usb2 {
		t.Fatalf("Budget for a 12 Mb/s link (%v) is shorter than for 480 Mb/s (%v)", slow, usb2)
	}
}
```

```
--- FAIL: TestDefectHunt_BudgetCoversWireTimeOnFullSpeedUSB (0.00s)
    Budget(1 GiB, 12 Mb/s) = 7m16s, below the 11m56s the wire needs at line rate
```

- **Smallest fix.** About 1 MiB/s for 12 Mb/s, 128 KiB/s for 1.5 Mb/s, and a default no faster than the USB 2 figure.

### DH-87. `ParseUIAutomatorXML` never panics

- **Severity: low.** The trigger (a closing tag before the opening one) is unlikely, but a panic in `ui_tree` kills the daemon.
- **Where.** `internal/device/android_ui.go:338-350`.
- **Reproduction** (`internal/device/zz_defecthunt_fuzz_test.go`; a 45 s fuzz over 20 device parsers found no other panic):

```go
// ParseUIAutomatorXML slices s[start:end+12] with start = first
// "<hierarchy" and end = last "</hierarchy>". When the only closing tag
// precedes the opening one (truncated dump after stray text), the slice
// is inverted and it panics. Tool dispatch runs handlers on a goroutine
// with no recover (internal/mcp/server.go:591), so ui_tree would take the
// daemon down.
func TestDefectHunt_UIAutomatorParseNeverPanics(t *testing.T) {
	raw := []byte("</hierarchy>\nERROR: null root node returned by UiTestAutomationBridge.\n<hierarchy rotation=\"0\"><node text=\"x\"")
	var err error
	p := func() (p any) {
		defer func() { p = recover() }()
		_, err = ParseUIAutomatorXML(raw)
		return nil
	}()
	if p != nil {
		t.Fatalf("ParseUIAutomatorXML panicked: %v", p)
	}
	if err == nil {
		t.Fatal("ParseUIAutomatorXML accepted a dump with no closing tag after <hierarchy")
	}
}
```

```
--- FAIL: TestDefectHunt_UIAutomatorParseNeverPanics (0.00s)
    ParseUIAutomatorXML panicked: runtime error: slice bounds out of range [71:12]
```

- **Smallest fix.** `if end < start { return …truncated error }`.

### DH-88. An empty device reference matches no inventory entry

- **Severity: low.** An empty ref resolves to the first Android entry, because its `ios_uuid` is also empty; the reservation normaliser feeds refs straight into `Lookup`.
- **Where.** `internal/inventory/inventory.go:122-127`, `176-178`.
- **Reproduction** (`internal/inventory/zz_defecthunt_lookup_test.go`):

```go
// Lookup and AliasFor compare the reference against every id field, and
// an Android entry's ios_uuid / ios_coredevice are empty, so an empty
// reference (a devicectl row with neither udid nor identifier, a
// candidate whose UUID is unset) resolves to whichever entry comes first.
// The daemon's reservation normaliser feeds refs straight into Lookup.
func TestDefectHunt_EmptyRefMatchesNoEntry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SPYDER_DATA_DIR", dir)
	inv := `[{"alias":"Pixel","platform":"android","android_serial":"R5CT1234ABC"}]`
	if err := os.WriteFile(filepath.Join(dir, "inventory.json"), []byte(inv), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New()
	if e, ok := s.Lookup(""); ok {
		t.Errorf("Lookup(\"\") = %q, want no match", e.Alias)
	}
	if a := s.AliasFor(""); a != "" {
		t.Errorf("AliasFor(\"\") = %q, want \"\"", a)
	}
}
```

```
--- FAIL: TestDefectHunt_EmptyRefMatchesNoEntry (0.00s)
    Lookup("") = "Pixel", want no match
    AliasFor("") = "Pixel", want ""
```

- **Smallest fix.** Return no match for `ref == ""` and skip empty id fields when comparing.


## Unconfirmed suspicions

None of these has a failing test. Each says what was tried or why it was not run.

### Reservations, dispatch and handlers

- **A panic in any verb kills the daemon.** `Dispatch` runs each handler in its own goroutine (`internal/mcp/server.go:626-633`) with no `recover()`; there is no `recover` anywhere in the request path (only in `appchannel/jq.go`). mcp-go's or net/http's recovery cannot reach a panic in a goroutine spawned by the handler. The only reachable crash I proved is the `run_script` recursion, which no `recover` would catch anyway. I found no unchecked type assertion on client arguments in `internal/mcp`, `internal/rest` or `internal/verify`. A recover-and-report wrapper in that goroutine is cheap insurance against the next nil map. (My own harness hit two such panics, `nil` `recordings` and `networkByDevice` maps, but only because `NewHandlerWithAdapters` leaves them unset. `NewHandler` sets them, so production is not affected.)
- **A reservation outlives a crashed holder for up to its TTL.** If a `spyder run`, an MCP client or a script dies without releasing, the device stays reserved for 1 h by default (up to 24 h). Nothing ties a lease to the holder's liveness. This is by design (TTL), but it is the main reason "device left reserved after a crash" recurs. A short TTL renewed by a heartbeat would bound it.
- **`reserve` with a selector is not atomic, despite the usage text.** `spyder run --on` claims "The daemon resolves+acquires atomically — no two-phase race window". `resolveReserveDevice` picks a free device under `h.mu`, but `Acquire` runs after the unlock, so two concurrent selector reservations can pick the same device. The loser gets a `Conflict` while another matching device is free. This does not cause a double lease. I did not build a deterministic interleaving.
- **`battery_history sample=true` can trigger a self-restart.** It samples every connected device synchronously in a context-less handler with the 15 s FastRead deadline (`internal/mcp/battery.go:57-60`, `151-164`), so a slow fleet census would follow the same restart path as the confirmed handler-holds-`h.mu` and client-hang-up findings. I did not time a real census.
- **The battery sampler has no per-device deadline.** `stateForBattery` calls `adapter.State` with no context (`battery.go:198-216`). One hung device stops the sampler, and with it the drain trend, recharge holds and the idle policy. Not reproduced.
- **Notification records are never pruned and every publish fsyncs all of them under the reservation lock.** Terminal (`delivered`/`cancelled`/`expired`) records stay in `notifications.json` forever. `Publish` rewrites and fsyncs the whole file, and is called from the reservation `onChange` hook, which runs under the reservation store's mutex (`internal/mcp/notifications.go:33-36`, `internal/notify/events.go:191-235`). Growth is slow, so I did not measure the effect.
- **The Wi-Fi bounce confirmation dialog has no timeout.** `ConfirmBounce` runs `osascript display dialog` with the daemon context only, so the host Wi-Fi monitor stops polling for as long as the dialog is unanswered (`internal/hostwifi/confirm.go:15-31`). Not run, because it would put a dialog on the owner's screen.
- **REST bodies have no size limit.** `io.ReadAll(r.Body)` in `internal/rest/rest.go`, together with the LAN bind (`SPYDER_ADDR=:3030`) and no authentication, lets any LAN peer post an arbitrarily large body. The likely result is a memguard exit. Not run.
- **Simulator leases do not gate other owners.** `authorize` consults only the device reservation book. A simulator leased to job A by `sim_acquire` can be installed to, launched on or terminated by job B if B names its UDID. Any verb touching the UDID also renews A's lease (`touchSim` in `resolveAdapter`), so a dashboard polling it keeps a dead job's simulator alive. Not tested; the simulator-lease reader's findings cover the lease book itself.

### Child processes, stream relay and app channel

- **Shutdown never stops active screen recordings.** `daemon.Run`'s shutdown (`internal/daemon/daemon.go:245-267`) has no step for `h.recordings`. Recorder children run with `Setpgid`, so `xcrun simctl io recordVideo` or `adb shell screenrecord` would keep running after the daemon exits. Not run: it needs a real recorder.
- **`os.Exit` paths skip every Stop.** `memguardExit` and the self-restart exit with `os.Exit`, skipping the tunnel (🎯T193 takeover mitigates), recordings, `adb logcat` children (they die at their next write by SIGPIPE) and `procRunner` runwda children.
- **Recordings are never reaped.** `recording.Registry.Remove` has no caller. A recorder that exits on its own (Android `screenrecord` stops at 180 s) stays registered and blocks `record_start` on that device until someone calls `record_stop`.
- **`logcapture.Manager.Stop` waits on `<-s.done` without a bound.** If `oslog.Open` or `stream.Close` blocked on a wedged DTX connection, `log_capture_stop`, the TTL sweeper and shutdown's `Close` would hang. Not shown with the real adapters.
- **`iostunnel.Start` ignores its context.** A `Supervise` tick or takeover start that wins the `select` against cancellation could spawn a tunnel after shutdown's `Stop`. A timing race; not reproduced.
- **The wedge log tail can stop reading without ending.** A scanner error (a line over 64 KiB) stops reading but leaves `log stream` running and blocked until shutdown.
- **Android `LogStream` ends quietly.** It returns nil when `logcat` exits on its own, so a capture's integrity shows neither a transport error nor "incomplete".
- **No app-level liveness for relay sidebands or app sessions.** A peer that hangs but keeps its TCP connection open is never culled: a relay sideband (which `launch_player` may auto-pick) or an app session (`ProbeConnectivity` counts failed pings but never closes it). Vanished hosts are culled by TCP keepalive.
- **Relay wire ids are predictable and unauthenticated.** Session ids run `s1`, `s2`, … and `/ws/server/wire/<id>` has no authentication, so a LAN host could claim a player's wire; a second wire also overwrites `wireRemote`. The package documents the relay as "LAN/trusted dev only".
- **App-channel buffers are capped by count, not bytes.** Up to 100k log lines of up to 16 MiB each, and up to 100k capture samples (a 100 KB slice every 10 ms reaches about 10 GB). Not run, to avoid multi-gigabyte allocations on a shared machine.
- **`app_release` has no holder check.** Any caller can free another holder's instance. Instances that ignore `quit`, or connect after the spawn timeout, become live but untracked.
- **Smaller leaks and races in the app channel.** `handleConn` never cancels `sCtx` (one leaked child context per ended session). `ReapIdleKeyedListeners` checks then stops without re-checking (unlikely with the 24 h TTL). `wait_app_session` does not clamp `poll_ms` to the remaining timeout. `requireSession`, `liveSessionFor` and `findLiveSession` call `resolveAdapter` without `h.mu` while other handlers take it.
- **Stream relay churn: clean.** A churn stress test under `-race` (3 servers, 60 players on both paths, same-name replaces mid-stream, a quarter of attaches never wired, concurrent catalogue reads) found no races, and the catalogue and goroutine count returned to baseline.

### Simulator leases, pool and device adapters

- **Simulator claims dropped on a transient empty listing.** `forgetMissingLocked` (`internal/simlease/provenance.go:182-193`) deletes claims for any device missing from a successful listing. If `simctl list` ever returned an empty list with exit 0, every claim would go and `sim_prune` could delete claimed devices. No evidence that simctl does this.
- **A running AVD deleted when `ps` fails.** `deleteDevice` (`internal/simlease/inventory.go:390`) treats a `ps` failure as "no emulator running" and deletes the AVD. Needs a fake `ps`; not run.
- **Case-sensitive UDID compare in `sim_acquire`.** The leased set (`simlease.go:331`) compares UDIDs case-sensitively while every other path folds case. Unreachable unless the lease file holds a differently-cased UDID.
- **Pool gaps that are latent today.** Pool holds have no expiry or reaper, `Pool.Release` has no holder check, a stale linger timer can shut a re-acquired simulator down early, and a `sim_acquire` with a pinned name starting `spyder-pool-` would be adopted by the pool after a restart. All latent while nothing in production calls `Pool.Acquire`.
- **Simulator lease expiry after a crash or sleep.** `Touch` persists at most every 30 s (`simlease.go:658`), so an expiry up to 30 s stale can cause an early reap after a crash; this looks deliberate. Whether leases expire across a laptop sleep depends on Go's monotonic clock on darwin; untested.
- **`isSimulator` holds `simKnown.mu` across a simctl call** (`internal/mcp/sim_lease.go:323-332`). Bounded by `connectedListTimeout`, but it serialises all UUID routing.
- **AVD home mismatch.** `AVDClone` uses `~/.android/avd` (`internal/pool/executor.go:63-67`) while `FSAndroid` honours `ANDROID_AVD_HOME`.
- **iOS system-alert session outlives its caller.** In `internal/device/ios_alert.go:76-134` and `223-289`, when `withAccessibility` times out or is cancelled it returns while the session goroutine may still run `fn`, which writes values the caller reads, and the per-device semaphore is released while the abandoned session can still move the inspector focus. Needs a go-ios session; not run.
- **iOS adapter calls with no deadline.** `State` (`ios.go:945-996`), `ResolveExecutable` (`1150`), `InstallApp` (`1962`), `UninstallApp` (`1983`), `Crashes` (`1786-1800`) and the launch paths. Dispatch deadlines bound tool calls (and then trigger the self-restart), but the battery sampler calls `State` directly, the same exposure as the Android `State`/`List` finding.
- **Log decoder desync after an error.** After a mid-frame decode error `pump` continues with the stack partly consumed (`internal/oslog/stream.go:180-184`), so later rows can pop the wrong items until a table reset. Not asserted, because the decoder carries the stack across frames by design.
- **Selector reservations can land on unauthorised Android phones.** Unauthorised or offline devices appear in `List` with no state and an empty OS, and `os_min`/`os_max` skip an empty OS (`selector.go:230-238`).
- **`parseSimLaunchctlPID` stops at the first stale label.** It returns not-running at the first matching label with a `-` pid even if a later line has a live pid (`internal/device/sim.go:236-249`). Whether the simulator's launchd keeps stale entries is unknown.
- **USB ceiling store writes are not atomic.** `usbspeed.Store.Save` uses plain `os.WriteFile` (`internal/usbspeed/ceiling.go:103`); a torn write reads back as empty and resets the ratchet.
- **Android `StopRecording` neither waits nor pulls the file**, contrary to the interface documentation.

## Coverage

**Read line by line (by me):**
- `internal/reservations/reservations.go`, `internal/daemon/daemon.go`, `internal/daemon/health.go`, `main.go`
- `internal/mcp/`: `dispatch_deadline.go`, `dispatch_op.go`, `lifecycle_gate.go`, `ensure_session.go`, `launched.go`, `power_policy.go`, `self_restart_evidence.go`, `runwda.go`, `sim_lease.go` (handler half)
- `internal/health/self_restart.go`, `internal/rest/rest.go`, `internal/clitimeout/clitimeout.go`, `internal/verify/exec.go`, `internal/verify/hub.go`, `internal/hostwifi/` (`monitor.go`, `bounce.go`, `confirm.go`), `internal/paths/paths.go`

**Read in part (by me):**
- `internal/mcp/server.go`: construction, `Dispatch`, the self-restart path, the verb tables.
- `internal/mcp/tools.go`: reservation verbs, `authorize`, `release`, `network`, `rotate`, `crashes`, recording, `resolve`, `resolveAdapter`, `resolveUninventoried`, `buildCandidates`, `archiveArtefact`.
- `internal/mcp/`: `android_control.go` (lock sites), `battery.go`, `dev_policy.go`, `notifications.go`, `app_exec.go` (`handleAppExecContext`), `script_helpers.go` (`run_script`), `agent_control.go`.
- `internal/runs/runs.go`, `internal/inventory/inventory.go` (`Lookup`, `ClassifyRaw`), `internal/notify/notify.go` and `events.go` (`Open`, `Add`, `Run`, `Publish`, `saveLocked`), `cli.go` (`reserveViaDaemon`, `postTool`, CLI timeouts), `internal/wedge/monitor.go` (`reconcile`), `internal/iostunnel/supervisor.go` (`Start`/`Stop`), `internal/health/supervise.go`, `internal/logcapture/session.go`, `internal/adbserver/adbserver.go`.

**Read line by line by the four readers:**
- **Simulator leases and pool:** `internal/simlease/*` and `internal/pool/*`; `internal/poolstore/poolstore.go`; `internal/mcp/sim_lease.go`.
- **Stream relay and app channel:**
  - `internal/streamrelay/relay.go`, `path.go`;
  - all of `internal/appchannel/` (`session.go`, `protocol.go`, `state_capture*.go`, `factory.go`, `spawn.go`, `wait.go`, `connectivity.go`, `describe.go`, `jq.go`);
  - `internal/mcp/app_channel.go`, `app_channel_lan.go`, `launch_player.go`, `app_spawn.go`, `wait_app_session.go`, `wait_state.go`.
- **Child processes:**
  - `internal/iostunnel/supervisor.go`, `internal/daemon/tunnel_takeover.go`, `memguard.go`;
  - `internal/agent/agent.go`;
  - `internal/device/runwda.go`;
  - `internal/logcapture/*`, `internal/recording/recording.go`;
  - `internal/adbserver/adbserver.go`;
  - `internal/health/` (`supervise.go`, `watchdog.go`, `self_restart.go`, `model.go`, `notify*.go`, `poll.go`, `bridge.go`);
  - `internal/wedge/` (`monitor.go`, `parity.go`, `recover.go`, `episode.go`, `doctor_bridge.go`);
  - `internal/exitlog`, `cmd/spyder-killusbmuxd`.
- **Device adapters and parsing:**
  - `internal/device/`: every `android*.go`; `sim.go`, `desktop.go`, `ios_logparse.go`, `ios_alert.go`, `alert.go`, `ios_executable_identity.go`, `ios_usbnet.go`, `device.go`, `settings.go`, `process_context.go`, `versions.go`, `power.go`; `ios.go` lines 505-2321;
  - `internal/oslog/` (`parser.go`, `record.go`, `stream.go`), `internal/usbspeed/*`, `internal/inventory/inventory.go`, `internal/selector/*`, `internal/installdeadline/deadline.go`, `internal/clitimeout`.

**Fuzzing:** about 75 s over the oslog decoder and 20 device parsers; nothing beyond the panics listed.

**Not reached, by anyone:**
- **Verify:** `engine.go` and `workflow.go` beyond the shell runner and `emit`; `retain.go`, `appraise.go`, `review.go`.
- **Ship and front-door code:** `internal/ship/*` (fastlane, secrets, audit), `ship_cli.go`, `doctor.go`, most of `cli.go`.
- **Small packages:** `internal/dashboard`, `internal/baselines`, `internal/visualdiff`, `internal/scriptlib`, `internal/devpolicy` (package internals), `internal/battery` (package internals), `internal/network`, `internal/listenaddr`, `internal/listenpreflight`.
- **Runs:** `internal/runs/archive.go`, `internal/runs/provenance.go`.
- **iOS:** `internal/goios/*` (session and connection pools); `internal/device/ios.go` lines 1-504; `ios_agent.go` beyond the hold and shutdown paths; `ios_agent_devicekit.go` beyond orphan handling; `ios_control.go`, `ios_ddi.go`, `ios_forward.go`, `ios_power.go`, `diagnostic.go`.
- **`player/`:** the C++ player, entirely.
- **mcp-go:** its transport behaviour.
- **The existing test suite:** not run as a whole. It is hermetic by design, but I could not rule out a path that reaches a real `adb` or usbmux socket, and devices were in use.

## Appendix: harness for each reproduction file

Package clause, imports and every non-test declaration of each file. Add the `TestDefectHunt_…` functions shown in the defect sections to rebuild the file.

#### `internal/adbserver/zz_defecthunt_restart_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package adbserver

import (
	"context"
	"io"
	"net"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// flakyServer answers host:track-devices with OKAY and then drops the
// connection after hold, like an adb server that keeps exiting (a
// version fight with Unity's bundled adb, or a server crash loop).
// After maxDrops connections it keeps the next one open.
func flakyServer(t *testing.T, hold time.Duration, maxDrops int32) (addr string, accepted *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted = &atomic.Int32{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n := accepted.Add(1)
			go func(c net.Conn, n int32) {
				req := make([]byte, 4+len("host:track-devices"))
				if _, err := io.ReadFull(c, req); err != nil {
					_ = c.Close()
					return
				}
				_, _ = c.Write([]byte("OKAY0000"))
				if maxDrops > 0 && n > maxDrops {
					return // keep this one open
				}
				time.Sleep(hold)
				_ = c.Close()
			}(c, n)
		}
	}()
	return ln.Addr().String(), accepted
}
```

#### `internal/agent/zz_defecthunt_concurrent_launch_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)


```

#### `internal/appchannel/zz_defecthunt_crash_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const dhChildEnv = "SPYDER_DEFECTHUNT_CHILD"

func dhIsChild(name string) bool { return os.Getenv(dhChildEnv) == name }

// dhRunChild re-runs this test binary for testName only, in a child process.
func dhRunChild(t *testing.T, testName string) (string, error) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SPYDER_DATA_DIR", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+testName+"$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), dhChildEnv+"="+testName)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func dhFirstLines(s string) string {
	var keep []string
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "fatal error") || strings.Contains(line, "panic") ||
			strings.Contains(line, "stack exceeds") || strings.Contains(line, "goroutine ") {
			keep = append(keep, line)
		}
		if len(keep) >= 6 {
			break
		}
	}
	return strings.Join(keep, "\n")
}
```

#### `internal/appchannel/zz_defecthunt_pool_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

// dhApp is a goroutine-safe fake app: it acknowledges every request and
// reports the request method on got.
type dhApp struct {
	conn net.Conn
	got  chan string
}

func dhDialApp(addr, name string, methods ...string) (*dhApp, error) {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return nil, err
	}
	hp, _ := PackParams(Hello{AppName: name, AppVersion: "defecthunt", Methods: MethodDescriptors(methods...)})
	if err := WriteFrame(conn, &Envelope{ID: 1, Method: MethodHello, Params: hp}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if _, err := ReadFrame(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	a := &dhApp{conn: conn, got: make(chan string, 64)}
	go func() {
		ok, _ := PackParams(map[string]any{"ok": true})
		for {
			env, err := ReadFrame(conn)
			if err != nil {
				return
			}
			if env.IsRequest() {
				select {
				case a.got <- env.Method:
				default:
				}
				_ = WriteFrame(conn, &Envelope{ID: env.ID, Result: ok})
			}
		}
	}()
	return a, nil
}
```

#### `internal/appchannel/zz_defecthunt_session_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// dhStart starts a Manager and an unkeyed loopback listener. The listener is
// not stopped automatically: tests that can wedge Stop use dhStopBounded.
func dhStart(t *testing.T) (*Manager, *Listener, string) {
	t.Helper()
	m := NewManager()
	t.Cleanup(m.Close)
	l, err := m.Start(StartParams{Owner: "defecthunt"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return m, l, fmt.Sprintf("127.0.0.1:%d", l.Port)
}

// dhStopBounded stops l but gives up after 2s, so a wedged Stop cannot hang
// the test binary.
func dhStopBounded(l *Listener) {
	done := make(chan struct{})
	go func() { l.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

// dhHello dials addr as an app named name and completes the hello handshake.
func dhHello(t *testing.T, addr, name string, methods ...string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	hp, _ := PackParams(Hello{AppName: name, AppVersion: "defecthunt", Methods: MethodDescriptors(methods...)})
	if err := WriteFrame(conn, &Envelope{ID: 1, Method: MethodHello, Params: hp}); err != nil {
		t.Fatalf("hello: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	ack, err := ReadFrame(conn)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil || ack.ID != 1 || ack.Error != nil {
		t.Fatalf("hello ack: %+v %v", ack, err)
	}
	return conn
}

// dhServe answers each request read from conn with answer(env) (nil = no
// reply) until conn closes.
func dhServe(conn net.Conn, answer func(env *Envelope) *Envelope) {
	go func() {
		for {
			env, err := ReadFrame(conn)
			if err != nil {
				return
			}
			if env.IsRequest() {
				if r := answer(env); r != nil {
					_ = WriteFrame(conn, r)
				}
			}
		}
	}()
}

// dhEventually polls cond every 10ms until it holds or d elapses.
func dhEventually(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// dhWaitSession returns the live manager session whose hello named appName.
func dhWaitSession(t *testing.T, m *Manager, appName string) *Session {
	t.Helper()
	var found *Session
	if !dhEventually(3*time.Second, func() bool {
		for _, s := range m.Sessions() {
			if hi := s.HelloInfo(); hi != nil && hi.AppName == appName {
				found = s
				return true
			}
		}
		return false
	}) {
		t.Fatalf("no session for %q", appName)
	}
	return found
}
```

#### `internal/daemon/zz_defecthunt_selfheal_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/health"
)


```

#### `internal/device/zz_defecthunt_adberrors_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)


```

#### `internal/device/zz_defecthunt_adbshell_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"reflect"
	"sync"
	"testing"
)


```

#### `internal/device/zz_defecthunt_agent_shutdown_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"testing"
	"time"
)


```

#### `internal/device/zz_defecthunt_androidlist_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)


```

#### `internal/device/zz_defecthunt_crashes_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)


```

#### `internal/device/zz_defecthunt_desktop_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func dhScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "game")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func dhAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
```

#### `internal/device/zz_defecthunt_fuzz_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"strings"
	"testing"
	"time"
)

// FuzzDefectHunt_DeviceParsers: parsers of device-tool output must not
// panic on any input.
func FuzzDefectHunt_DeviceParsers(f *testing.F) {
	f.Add("11-19 14:30:22.000  1234  1234 E AndroidRuntime: FATAL EXCEPTION: main\n")
	f.Add("[ 11-19 14:30:22.000  1234: 1234 E/Tag ]\nmsg\n")
	f.Add(`<hierarchy rotation="0"><node bounds="[0,0][10,10]" text="a"/></hierarchy>`)
	f.Add("  Window #3 Window{a1 u0 p/p.A}:\n    fl=SECURE\n  mCurrentFocus=Window{a1 u0 p/p.A}\n")
	f.Add("pid: 1, tid: 1, name: x  >>> y <<<\nsignal 6\n")
	f.Add("2026-11-19 10:23:45.123456-0800 0x1a2b     Default     0x0                  123    0    SpringBoard: hi\n")
	f.Add("emulator-5554 tcp:8080 tcp:8080\n")
	f.Add("mWakefulness=Awake\nActive default network: 100\nNetworkAgentInfo{network{100} ni{WIFI CONNECTED} IS_VALIDATED}\n")
	f.Fuzz(func(t *testing.T, s string) {
		if i := strings.Index(s, "<hierarchy"); i >= 0 {
			if j := strings.LastIndex(s, "</hierarchy>"); j >= 0 && j+len("</hierarchy>") < i {
				t.Skip() // pinned by TestDefectHunt_UIAutomatorParseNeverPanics
			}
		}
		dec := NewAndroidLogDecoder()
		for i, line := range strings.Split(s, "\n") {
			_ = dec.Push(line, int64(i))
			_ = ParseIOSLogLine(line, int64(i))
		}
		_, _ = ParseUIAutomatorXML([]byte(s))
		_ = ParseDumpsysWindowSecure(s)
		_ = parseTombstone([]byte(s), time.Time{}, "x")
		_ = parseLogcatCrashBuffer(s, time.Time{}, "")
		_, _ = parseAndroidBattery([]byte(s))
		var p Power
		parseAndroidPowerLock(s, &p)
		_, _ = ParseForwardList(s, "")
		_, _, _, _ = ParseGfxInfo(s)
		_, _ = parseDumpsysPackage(s, "com.x")
		_, _ = parseDevicectlList([]byte(s))
		_, _ = parseDevicectlConnectedIOSDevices([]byte(s))
		_ = usbNetworkInterfacesForSerial(s, "00008110-001A2C3E0A12801E")
		_, _ = parseSimLaunchctlPID(s, "com.x")
		_, _ = PlanTextInput(s, "")
		_, _ = canonicalIOSExecutablePath(s)
	})
}
```

#### `internal/device/zz_defecthunt_helpers_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// dhFakeTools isolates a defect-hunt test from every real device tool.
// HOME and SPYDER_DATA_DIR point at temp dirs. PATH becomes
// <tmp>:/bin:/usr/bin with fake `adb` and `xcrun` first, so an exec that
// escapes the androidAdb hook lands in the fake (never in
// /opt/homebrew/bin/adb or /usr/bin/xcrun). The fake adb answers the
// script given in adbBody (sh case arms on "$*"); anything else is logged
// to escaped.log and exits 97. It returns the fake bin dir.
func dhFakeTools(t *testing.T, adbBody string) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SPYDER_DATA_DIR", t.TempDir())
	dir := t.TempDir()
	log := filepath.Join(dir, "escaped.log")
	adb := "#!/bin/sh\ncase \"$*\" in\n" + adbBody +
		"*) echo \"$*\" >> '" + log + "'; echo 'fake adb: unexpected exec' >&2; exit 97;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "adb"), []byte(adb), 0o755); err != nil {
		t.Fatal(err)
	}
	xcrun := "#!/bin/sh\necho \"xcrun $*\" >> '" + log + "'\nexit 97\n"
	if err := os.WriteFile(filepath.Join(dir, "xcrun"), []byte(xcrun), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if b, err := os.ReadFile(log); err == nil {
			t.Logf("unexpected execs that reached the fake adb/xcrun:\n%s", b)
		}
	})
	t.Setenv("PATH", dir+":/bin:/usr/bin")
	if p, err := exec.LookPath("adb"); err != nil || p != filepath.Join(dir, "adb") {
		t.Fatalf("adb resolves to %q (%v), want the fake in %s", p, err, dir)
	}
	if p, err := exec.LookPath("xcrun"); err != nil || p != filepath.Join(dir, "xcrun") {
		t.Fatalf("xcrun resolves to %q (%v), want the fake in %s", p, err, dir)
	}
	return dir
}

// dhHook installs an androidAdb hook for the test and restores it after.
func dhHook(t *testing.T, hook androidAdbHook) {
	t.Helper()
	restore := SetAndroidAdbForTest(hook)
	t.Cleanup(restore)
}

// dhDeviceShell models what really happens to `adb -s X shell ARGS...`:
// adb joins ARGS with single spaces without escaping (adb
// client/commandline.cpp: "We don't escape here, just like ssh(1)") and
// the device's sh runs the joined string. The model runs it in a local
// /bin/sh with PATH emptied, where each name in stubs is a function that
// records its argv instead of doing anything. It returns one argv per
// stub invocation, in order.
func dhDeviceShell(t *testing.T, args []string, stubs ...string) [][]string {
	t.Helper()
	i := 0
	for i < len(args) && args[i] != "shell" {
		i++
	}
	if i == len(args) {
		t.Fatalf("not an adb shell invocation: %q", args)
	}
	joined := strings.Join(args[i+1:], " ")
	var script strings.Builder
	script.WriteString("rec() { printf '%s' \"$1\"; shift; for a in \"$@\"; do printf '\\037%s' \"$a\"; done; printf '\\036'; }\n")
	for _, s := range stubs {
		script.WriteString(s + "() { rec " + s + " \"$@\"; }\n")
	}
	script.WriteString("PATH=/nonexistent\neval \"$1\"\n")
	ctx, cancel := context.WithTimeout(context.Background(), 10e9)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "/bin/sh", "-c", script.String(), "sh", joined).Output()
	var calls [][]string
	for rec := range strings.SplitSeq(string(out), "\036") {
		if rec == "" {
			continue
		}
		calls = append(calls, strings.Split(rec, "\037"))
	}
	return calls
}
```

#### `internal/device/zz_defecthunt_parsers_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"testing"

	goios_ios "github.com/danielpaulus/go-ios/ios"
)


```

#### `internal/device/zz_defecthunt_runwda_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRunwdaBin is an `ios` stand-in whose runwda fails the way a real
// one does on a consent/stream error: it prints a startup-failure marker
// and stays up. On SIGTERM it takes two seconds to tear down, as go-ios
// runwda does while it closes the XCTest session.
func fakeRunwdaBin(t *testing.T) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	t.Setenv("SPYDER_DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("HOME", filepath.Join(dir, "home"))
	bin = filepath.Join(dir, "ios")
	script := "#!/bin/sh\ntrap '/bin/sleep 2; exit 0' TERM\necho 'runwda: ERROR stream_closed'\nwhile :; do /bin/sleep 1; done\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("/usr/bin/pkill", "-KILL", "-f", bin).Run() })
	return bin, dir
}

func liveFakes(bin string) int {
	out, _ := exec.Command("/usr/bin/pgrep", "-f", bin).Output()
	return len(strings.Fields(string(out)))
}
```

#### `internal/health/zz_defecthunt_supervise_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// diesAfterStart starts fine every time and is dead by the next probe:
// a crash-looping child (bad flags, port already taken, immediate panic).
type diesAfterStart struct{ starts atomic.Int32 }

func (p *diesAfterStart) Name() string                { return "dies-after-start" }

func (p *diesAfterStart) Start(context.Context) error { p.starts.Add(1); return nil }

func (p *diesAfterStart) Alive() bool                 { return false }

func (p *diesAfterStart) Stop(context.Context) error  { return nil }
```

#### `internal/installdeadline/zz_defecthunt_slowlink_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package installdeadline

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)


```

#### `internal/inventory/zz_defecthunt_classify_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package inventory

import "testing"


```

#### `internal/inventory/zz_defecthunt_lookup_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package inventory

import (
	"os"
	"path/filepath"
	"testing"
)


```

#### `internal/iostunnel/zz_defecthunt_lifecycle_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package iostunnel

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/health"
)

// defectHuntEnv isolates paths.Base() and gives the supervisor a stub
// registry so nothing reaches the real tunnel on :60105.
func defectHuntEnv(t *testing.T) (dir string, port int) {
	t.Helper()
	dir = t.TempDir()
	t.Setenv("SPYDER_DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("HOME", filepath.Join(dir, "home"))
	mux := http.NewServeMux()
	mux.HandleFunc("/tunnels", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("[]")) })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	t.Cleanup(func() { _ = exec.Command("pkill", "-KILL", "-f", dir).Run() })
	return dir, ln.Addr().(*net.TCPAddr).Port
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func readPIDs(t *testing.T, path string) []int {
	t.Helper()
	b, _ := os.ReadFile(path)
	var pids []int
	for _, f := range strings.Fields(string(b)) {
		if n, err := strconv.Atoi(f); err == nil {
			pids = append(pids, n)
		}
	}
	return pids
}
```

#### `internal/logcapture/zz_defecthunt_ttl_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package logcapture

import (
	"context"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
)

// chattyAdapter emits a line every few milliseconds until cancelled, the
// way a live iOS syslog or Android logcat stream does.
type chattyAdapter struct{}

func (chattyAdapter) LogStream(ctx context.Context, id string, _ device.LogFilter, out chan<- device.LogLine) error {
	t := time.NewTicker(5 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			select {
			case out <- device.LogLine{Timestamp: time.Now(), Process: "p", Level: "default", Message: "tick"}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}
```

#### `internal/mcp/zz_defecthunt_appchannel_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/appchannel"
	"github.com/marcelocantos/spyder/internal/paths"
)

// dhAppConn dials an app-channel listener as app name and completes hello.
// Requests are answered with answer(method); a nil answer never replies (a
// game busy on its main thread).
func dhAppConn(t *testing.T, port int, name string, answer func(method string) any, methods ...string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	hp, _ := appchannel.PackParams(appchannel.Hello{AppName: name, AppVersion: "defecthunt",
		Methods: appchannel.MethodDescriptors(methods...)})
	if err := appchannel.WriteFrame(conn, &appchannel.Envelope{ID: 1, Method: appchannel.MethodHello, Params: hp}); err != nil {
		t.Fatalf("hello: %v", err)
	}
	if _, err := appchannel.ReadFrame(conn); err != nil {
		t.Fatalf("hello ack: %v", err)
	}
	go func() {
		for {
			env, err := appchannel.ReadFrame(conn)
			if err != nil {
				return
			}
			if answer != nil && env.IsRequest() {
				raw, _ := appchannel.PackParams(answer(env.Method))
				_ = appchannel.WriteFrame(conn, &appchannel.Envelope{ID: env.ID, Result: raw})
			}
		}
	}()
	return conn
}

func dhSessionID(t *testing.T, mgr *appchannel.Manager) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ss := mgr.Sessions(); len(ss) == 1 {
			return ss[0].ID
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no app-channel session")
	return ""
}
```

#### `internal/mcp/zz_defecthunt_simlease_ttl_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/simlease"
)


```

#### `internal/mcp/zz_defecthunt_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/appchannel"
	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/health"
	"github.com/marcelocantos/spyder/internal/paths"
	"github.com/marcelocantos/spyder/internal/recording"
	"github.com/marcelocantos/spyder/internal/reservations"
	"github.com/marcelocantos/spyder/internal/runs"
)

// defectHuntHandler: stub iOS and Android adapters (no adb, no go-ios),
// inventory with iPad, self-heal on with a counting exit.
func defectHuntHandler(t *testing.T, ios *stubAdapter) (*Handler, *atomic.Int32) {
	t.Helper()
	t.Setenv(paths.DataDirEnv, t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if err := os.WriteFile(paths.InventoryPath(), []byte(testInventory), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithAdapters(ios, &stubAdapter{})
	// Fields NewHandler (production) initialises and NewHandlerWithAdapters does not.
	h.recordings = recording.NewRegistry()
	h.networkByDevice = map[string]appliedNetwork{}
	h.EnableSelfHeal(time.Hour, 100*time.Millisecond)
	var exits atomic.Int32
	h.selfRestart = health.NewSelfRestartLimiterForTest(3, time.Hour, func(int) { exits.Add(1) })
	return h, &exits
}
```

#### `internal/oslog/zz_defecthunt_decoder_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package oslog

import (
	"encoding/binary"
	"fmt"
	"testing"
)

// dhWords encodes 16-bit little-endian opcode words into one frame.
func dhWords(words ...uint16) []byte {
	b := make([]byte, 2*len(words))
	for i, w := range words {
		binary.LittleEndian.PutUint16(b[2*i:], w)
	}
	return b
}

// dhDecode runs Decode and converts a panic into a test failure, so the
// test reports the defect instead of aborting the binary.
func dhDecode(t *testing.T, name string, frame []byte) {
	t.Helper()
	var err error
	panicked := func() (p any) {
		defer func() { p = recover() }()
		_, err = NewDecoder().Decode(frame, nil)
		return nil
	}()
	if panicked != nil {
		t.Fatalf("%s: Decode panicked on device-supplied frame % x: %v "+
			"(pump runs Decode on a goroutine with no recover, so this kills the daemon)",
			name, frame, panicked)
	}
	if err == nil {
		t.Fatalf("%s: Decode accepted a long-form copy whose back-reference is outside the stack", name)
	}
}

// FuzzDefectHunt_Decode: Decode must never panic on arbitrary frames.
func FuzzDefectHunt_Decode(f *testing.F) {
	f.Add(dhWords(0xC001, 0xC002, 0x6902))
	f.Fuzz(func(t *testing.T, frame []byte) {
		// The long-form copy panic is pinned by the test above; skip it so
		// the fuzzer can look for other crashes.
		for i := 0; i+1 < len(frame); i += 2 {
			if binary.LittleEndian.Uint16(frame[i:]) == 0x65FF {
				t.Skip()
			}
		}
		d := NewDecoder()
		_, _ = d.Decode(frame, nil)
		_, _ = d.Decode(frame, nil) // state carries across frames
	})
}
```

#### `internal/pool/zz_defecthunt_pool_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package pool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/poolstore"
)

// dhExec wraps fakeExec with a failing SimList and a gated SimBoot.
type dhExec struct {
	*fakeExec
	simListErr  error
	bootEntered chan struct{} // closed when SimBoot is entered (if non-nil)
	bootGate    chan struct{} // SimBoot waits for this (if non-nil)
}

func (d *dhExec) SimList() ([]SimInfo, error) {
	if d.simListErr != nil {
		return nil, d.simListErr
	}
	return d.fakeExec.SimList()
}

func (d *dhExec) SimBoot(udid string) error {
	if d.bootEntered != nil {
		close(d.bootEntered)
		d.bootEntered = nil
	}
	if d.bootGate != nil {
		<-d.bootGate
	}
	return d.fakeExec.SimBoot(udid)
}

func dhStore(t *testing.T) *poolstore.Store {
	t.Helper()
	s, err := poolstore.Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
```

#### `internal/poolstore/zz_defecthunt_poolstore_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package poolstore

import (
	"testing"
	"time"
)


```

#### `internal/reservations/zz_defecthunt_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package reservations

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)


```

#### `internal/selector/zz_defecthunt_osversion_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package selector

import (
	"errors"
	"testing"

	"github.com/marcelocantos/spyder/internal/device"
)


```

#### `internal/simlease/zz_defecthunt_simlease_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package simlease

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// dhSimctl wraps the package's fakeSimctl with failure and timing hooks.
type dhSimctl struct {
	*fakeSimctl
	createErr error

	hookMu sync.Mutex
	// afterList, when set, runs once after a List snapshot is taken and
	// before it is returned (the snapshot is already stale when it runs).
	afterList func()
	// beforeList, when set, runs (every time) before a List snapshot.
	beforeList func()
}

func (d *dhSimctl) List(ctx context.Context) ([]Sim, error) {
	d.hookMu.Lock()
	before := d.beforeList
	d.hookMu.Unlock()
	if before != nil {
		before()
	}
	sims, err := d.fakeSimctl.List(ctx)
	d.hookMu.Lock()
	after := d.afterList
	d.afterList = nil
	d.hookMu.Unlock()
	if after != nil {
		after()
	}
	return sims, err
}

func (d *dhSimctl) Create(ctx context.Context, name, typ, rt string) (string, error) {
	if d.createErr != nil {
		return "", d.createErr
	}
	return d.fakeSimctl.Create(ctx, name, typ, rt)
}

func dhManager(t *testing.T, s Simctl, now func() time.Time, path string) *Manager {
	t.Helper()
	m, err := New(&Args{Simctl: s, Path: path, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func fixedNow() func() time.Time {
	t0 := time.Unix(1_800_000_000, 0)
	return func() time.Time { return t0 }
}

// slowCreateSimctl makes the created simulator visible in `simctl list`
// before Create returns, as CoreSimulator does while `simctl create`'s
// process exits and its UDID is read back.
type slowCreateSimctl struct {
	*fakeSimctl
	visible chan struct{}
	finish  chan struct{}
}

func (s *slowCreateSimctl) Create(ctx context.Context, name, typ, rt string) (string, error) {
	udid, err := s.fakeSimctl.Create(ctx, name, typ, rt)
	close(s.visible)
	<-s.finish
	return udid, err
}
```

#### `internal/usbspeed/zz_defecthunt_legacyudid_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package usbspeed

import (
	"testing"

	"github.com/marcelocantos/spyder/internal/device"
)


```

#### `internal/verify/zz_defecthunt_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)


```

#### `internal/wedge/zz_defecthunt_stale_ring_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package wedge

import (
	"context"
	"testing"
)


```

#### `zz_defecthunt_test.go`

```go
// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/paths"
	"github.com/marcelocantos/spyder/internal/reservations"
)

// Re-exec hook: the child process runs runCmd (which calls os.Exit).
func TestDefectHuntRunCmdHelper(t *testing.T) {
	if os.Getenv("DEFECTHUNT_RUNCMD") != "1" {
		t.Skip("helper")
	}
	runCmd(strings.Split(os.Getenv("DEFECTHUNT_ARGS"), "\x1f"))
}
```


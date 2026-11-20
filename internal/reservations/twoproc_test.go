// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package reservations

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Two processes, each a reservation book opened once the way the daemon
// and `spyder run` open theirs. They share one file. The parent speaks
// one command per line and reads one reply.

func TestMain(m *testing.M) {
	if os.Getenv("SPYDER_RESV_PROC") == "" {
		os.Exit(m.Run())
	}
	os.Exit(procMain())
}

func procMain() int {
	path := os.Getenv("SPYDER_RESV_PATH")
	if os.Getenv("SPYDER_RESV_CRASH") == "1" {
		testHookBeforeRename = func() { os.Exit(42) }
	}
	s, err := New(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		return 1
	}
	in := bufio.NewScanner(os.Stdin)
	out := bufio.NewWriter(os.Stdout)
	fmt.Fprintln(out, "READY")
	out.Flush()
	for in.Scan() {
		fields := strings.Fields(in.Text())
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "ACQUIRE":
			_, err = s.Acquire(fields[1], fields[2], DefaultTTL, "")
			writeProcResult(out, err)
		case "RENEW":
			_, err = s.Renew(fields[1], fields[2], DefaultTTL)
			writeProcResult(out, err)
		case "RELEASE":
			writeProcResult(out, s.Release(fields[1], fields[2]))
		case "GET":
			r, ok, err := s.Get(fields[1])
			if err != nil {
				writeProcResult(out, err)
				continue
			}
			if !ok {
				fmt.Fprintln(out, "FREE")
			} else {
				fmt.Fprintf(out, "HELD %s\n", r.Owner)
			}
			out.Flush()
		case "QUIT":
			return 0
		default:
			fmt.Fprintf(out, "ERR unknown %s\n", fields[0])
			out.Flush()
		}
	}
	return 0
}

func writeProcResult(out *bufio.Writer, err error) {
	switch {
	case err == nil:
		fmt.Fprintln(out, "OK")
	case IsConflict(err):
		fmt.Fprintln(out, "CONFLICT")
	default:
		fmt.Fprintf(out, "ERR %v\n", err)
	}
	out.Flush()
}

type bookProc struct {
	cmd    *exec.Cmd
	stdin  *bufio.Writer
	stdout *bufio.Reader
}

func startBookProc(t *testing.T, path, crash string) *bookProc {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		"SPYDER_RESV_PROC=1",
		"SPYDER_RESV_PATH="+path,
		"SPYDER_RESV_CRASH="+crash,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &bookProc{cmd: cmd, stdin: bufio.NewWriter(stdin), stdout: bufio.NewReader(stdout)}
	t.Cleanup(func() {
		if p.cmd.Process != nil {
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		}
		_ = p.cmd.Wait()
	})
	if got := p.read(t); got != "READY" {
		t.Fatalf("proc ready = %q", got)
	}
	return p
}

func (p *bookProc) roundTrip(line string) (string, error) {
	if _, err := fmt.Fprintln(p.stdin, line); err != nil {
		return "", err
	}
	if err := p.stdin.Flush(); err != nil {
		return "", err
	}
	type got struct {
		line string
		err  error
	}
	ch := make(chan got, 1)
	go func() {
		reply, err := p.stdout.ReadString('\n')
		ch <- got{strings.TrimSpace(reply), err}
	}()
	select {
	case <-time.After(15 * time.Second):
		return "", fmt.Errorf("timed out waiting for %q", line)
	case g := <-ch:
		if g.err != nil {
			return "", g.err
		}
		return g.line, nil
	}
}

func (p *bookProc) call(t *testing.T, line string) string {
	t.Helper()
	got, err := p.roundTrip(line)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (p *bookProc) read(t *testing.T) string {
	t.Helper()
	type got struct {
		line string
		err  error
	}
	ch := make(chan got, 1)
	go func() {
		line, err := p.stdout.ReadString('\n')
		ch <- got{strings.TrimSpace(line), err}
	}()
	select {
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for the reservation process")
	case g := <-ch:
		if g.err != nil {
			t.Fatalf("read: %v", g.err)
		}
		return g.line
	}
	return ""
}

func bookPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "reservations.json")
}

// The daemon's book is already open. It grants the device. `spyder run`
// (its own book, opened earlier) then asks.
func TestTwoProc_DaemonHoldsRunAsks(t *testing.T) {
	path := bookPath(t)
	daemon := startBookProc(t, path, "")
	run := startBookProc(t, path, "")
	if got := daemon.call(t, "ACQUIRE dev daemon-job"); got != "OK" {
		t.Fatalf("daemon acquire: %s", got)
	}
	if got := run.call(t, "ACQUIRE dev run-job"); got != "CONFLICT" {
		t.Fatalf("run was granted dev while the daemon holds it: %s", got)
	}
}

// `spyder run` grants the device. The daemon, whose book was already
// open, then asks.
func TestTwoProc_RunHoldsDaemonAsks(t *testing.T) {
	path := bookPath(t)
	daemon := startBookProc(t, path, "")
	run := startBookProc(t, path, "")
	if got := run.call(t, "ACQUIRE dev run-job"); got != "OK" {
		t.Fatalf("run acquire: %s", got)
	}
	if got := daemon.call(t, "ACQUIRE dev daemon-job"); got != "CONFLICT" {
		t.Fatalf("daemon was granted dev while spyder run holds it: %s", got)
	}
}

// The holder is killed after the grant. The survivor must still refuse
// a second owner, and the book on disk must still name the dead holder.
func TestTwoProc_KilledWhileHolding(t *testing.T) {
	path := bookPath(t)
	daemon := startBookProc(t, path, "")
	run := startBookProc(t, path, "")
	if got := run.call(t, "ACQUIRE dev run-job"); got != "OK" {
		t.Fatalf("run acquire: %s", got)
	}
	if err := syscall.Kill(-run.cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = run.cmd.Wait()
	run.cmd.Process = nil // cleanup must not kill the daemon's group

	if got := daemon.call(t, "ACQUIRE dev daemon-job"); got != "CONFLICT" {
		t.Fatalf("after run was killed, the daemon granted dev: %s", got)
	}
	fresh, err := New(path)
	if err != nil {
		t.Fatalf("reopen book: %v", err)
	}
	r, ok, err := fresh.Get("dev")
	if err != nil || !ok || r.Owner != "run-job" {
		t.Fatalf("book after the kill = %+v ok=%v; want run-job", r, ok)
	}
}

// Both grant the same device at once. Exactly one may hold it.
func TestTwoProc_BothStartAtOnce(t *testing.T) {
	path := bookPath(t)
	daemon := startBookProc(t, path, "")
	run := startBookProc(t, path, "")
	var daemonGot, runGot string
	var daemonErr, runErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		daemonGot, daemonErr = daemon.roundTrip("ACQUIRE dev daemon-job")
	}()
	go func() {
		defer wg.Done()
		runGot, runErr = run.roundTrip("ACQUIRE dev run-job")
	}()
	wg.Wait()
	if daemonErr != nil || runErr != nil {
		t.Fatalf("daemon=%q %v run=%q %v", daemonGot, daemonErr, runGot, runErr)
	}
	wins := 0
	for _, got := range []string{daemonGot, runGot} {
		switch got {
		case "OK":
			wins++
		case "CONFLICT":
		default:
			t.Fatalf("unexpected replies daemon=%s run=%s", daemonGot, runGot)
		}
	}
	if wins != 1 {
		t.Fatalf("want exactly one holder; daemon=%s run=%s", daemonGot, runGot)
	}
}

// DH-11: a release through the other process must reach the daemon's
// already-open book. A long job's renewals must too.
func TestTwoProc_ReleaseAndRenewReachTheOtherBook(t *testing.T) {
	path := bookPath(t)
	daemon := startBookProc(t, path, "")
	run := startBookProc(t, path, "")
	if got := daemon.call(t, "ACQUIRE dev job-A"); got != "OK" {
		t.Fatal(got)
	}
	if got := run.call(t, "ACQUIRE dev job-A"); got != "OK" {
		t.Fatalf("same-owner renew: %s", got)
	}
	if got := run.call(t, "RELEASE dev job-A"); got != "OK" {
		t.Fatal(got)
	}
	if got := daemon.call(t, "GET dev"); got != "FREE" {
		t.Fatalf("daemon book after run released: %s", got)
	}
	if got := daemon.call(t, "ACQUIRE dev job-B"); got != "OK" {
		t.Fatalf("next owner after release: %s", got)
	}
}

func TestSharedBook_RenewKeepsTheDaemonFromRegranting(t *testing.T) {
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	path := bookPath(t)
	daemon, err := New(path, WithNow(clock))
	if err != nil {
		t.Fatal(err)
	}
	run, err := New(path, WithNow(clock))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := daemon.Acquire("dev", "job-A", DefaultTTL, "spyder run --on"); err != nil {
		t.Fatal(err)
	}
	if _, err := run.Acquire("dev", "job-A", DefaultTTL, "spyder run"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		now = now.Add(DefaultTTL / 3)
		if _, err := run.Renew("dev", "job-A", DefaultTTL); err != nil {
			t.Fatalf("renew %d: %v", i, err)
		}
	}
	if _, err := daemon.Acquire("dev", "job-B", DefaultTTL, ""); err == nil {
		t.Fatal("daemon granted dev while run was still renewing")
	}
}

// DH-13: two writers must not tear the book through one staging file.
func TestSharedBook_ConcurrentWritersKeepTheBookParseable(t *testing.T) {
	path := bookPath(t)
	a, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	owner := "owner-with-a-long-name-" + strings.Repeat("x", 200)
	note := strings.Repeat("n", 300)
	for i := 0; i < 20; i++ {
		if _, err := a.Acquire(fmt.Sprintf("dev-a-%02d", i), owner, DefaultTTL, note); err != nil {
			t.Fatal(err)
		}
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
			_, _ = a.Renew("dev-a-00", owner, DefaultTTL)
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
			_, _ = b.Acquire("short", "b", DefaultTTL, "")
			_ = b.Release("short", "b")
		}
	}()
	deadline := time.Now().Add(400 * time.Millisecond)
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
		t.Fatalf("reservations.json became unparseable: %v; New() now returns: %v", bad, err)
	}
}

// A process that dies after staging the new book and before replacing
// the old one must leave the previous book in place.
func TestCrashBeforeRenameLeavesPreviousBook(t *testing.T) {
	path := bookPath(t)
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire("dev", "keep", DefaultTTL, ""); err != nil {
		t.Fatal(err)
	}
	crasher := startBookProc(t, path, "1")
	reply, err := crasher.roundTrip("ACQUIRE other intruder")
	waitErr := crasher.cmd.Wait()
	crasher.cmd.Process = nil
	ee, ok := waitErr.(*exec.ExitError)
	if !ok || ee.ExitCode() != 42 {
		t.Fatalf("crash during save: reply %q wait %v", reply, waitErr)
	}
	fresh, err := New(path)
	if err != nil {
		t.Fatalf("book did not survive the crash: %v", err)
	}
	r, held, err := fresh.Get("dev")
	if err != nil || !held || r.Owner != "keep" {
		t.Fatalf("previous holder after crash = %+v held=%v", r, held)
	}
	if _, held, err := fresh.Get("other"); err != nil || held {
		t.Fatal("the grant that died before replace is in the book")
	}
}

// DH-32: a grant that was not written is not success.
func TestUnpersistedAcquireReportsAnError(t *testing.T) {
	path := bookPath(t)
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	testHookBeforeRename = func() {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { testHookBeforeRename = nil })
	_, acqErr := s.Acquire("dev", "job-A", DefaultTTL, "")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if acqErr == nil {
		t.Error("Acquire returned success even though replacement failed")
	}
	restarted, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, held, err := restarted.Get("dev"); err != nil || held {
		t.Fatalf("after failed rename, book held=%v err=%v", held, err)
	}
}

func TestOpenBook_CorruptStartsEmptyAndEnforces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reservations.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, moved, err := OpenBook(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if moved == "" {
		t.Fatal("corrupt book was not moved aside")
	}
	if _, err := os.Stat(moved); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire("dev", "job-A", time.Minute, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire("dev", "job-B", time.Minute, ""); !IsConflict(err) {
		t.Fatalf("empty replacement book did not enforce: %v", err)
	}
}

func TestOpenBook_QuarantineUsesTransactionLock(t *testing.T) {
	path := bookPath(t)
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.lockFile(); err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			s.unlockFile()
		}
	}()
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	type result struct {
		store *Store
		moved string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		store, moved, err := OpenBook(path, nil)
		done <- result{store, moved, err}
	}()
	select {
	case got := <-done:
		t.Fatalf("OpenBook raced the writer's lock: moved=%q error=%v", got.moved, got.err)
	case <-time.After(200 * time.Millisecond):
	}
	now := time.Now()
	s.state["dev"] = Reservation{Device: "dev", Owner: "writer", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := s.saveLocked(); err != nil {
		t.Fatal(err)
	}
	s.unlockFile()
	locked = false
	select {
	case got := <-done:
		if got.err != nil || got.moved != "" {
			t.Fatalf("OpenBook quarantined the replacement: moved=%q error=%v", got.moved, got.err)
		}
		if got.store.Authorize("dev", "other") == nil {
			t.Fatal("OpenBook lost the writer's lease")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("OpenBook did not finish after the writer unlocked")
	}
}

func TestOpenBook_ReadsPreviousVersion(t *testing.T) {
	path := bookPath(t)
	now := time.Now().Truncate(time.Second)
	body := fmt.Sprintf(`[{
  "device": "iPad",
  "owner": "legacy",
  "expires_at": %q,
  "note": "existing lease",
  "created_at": %q
}]`, now.Add(time.Hour).Format(time.RFC3339), now.Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s, moved, err := OpenBook(path, nil)
	if err != nil || moved != "" {
		t.Fatalf("open previous book: moved=%q err=%v", moved, err)
	}
	if r, held, err := s.Get("iPad"); err != nil || !held || r.Owner != "legacy" ||
		r.Note != "existing lease" || !r.CreatedAt.Equal(now) || !r.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("previous lease: %+v held=%v err=%v", r, held, err)
	}
	if _, err := s.Acquire("iPad", "new", DefaultTTL, ""); !IsConflict(err) {
		t.Fatalf("previous lease did not block a new grant: %v", err)
	}
}

// DH-66: at the expiry instant only one rule of "held" applies.
func TestExpiryInstantHasOneHolder(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	s, err := New("", WithNow(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire("dev", "job-A", time.Hour, ""); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	// Authorize(job-A) is nil both when A still holds and when the hold
	// has expired. A different owner is refused only while A still holds.
	aHolds := s.Authorize("dev", "someone-else") != nil
	_, bErr := s.Acquire("dev", "job-B", time.Hour, "")
	if aHolds && bErr == nil {
		t.Fatal("at the expiry instant job-A still excludes others and job-B was granted dev")
	}
	if bErr != nil {
		t.Fatalf("expiry instant still blocked a new grant: %v", bErr)
	}
}

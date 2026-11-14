// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package simlease

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeAndroid is an in-memory AndroidHost.
type fakeAndroid struct {
	mu     sync.Mutex
	avds   []AVD
	images []SystemImage
	calls  []string
}

func (f *fakeAndroid) AVDs(context.Context) ([]AVD, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.avds), nil
}

func (f *fakeAndroid) SystemImages(context.Context) ([]SystemImage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.images), nil
}

func (f *fakeAndroid) DeleteAVD(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "delete-avd "+name)
	for i := range f.avds {
		if f.avds[i].Name == name {
			f.avds = slices.Delete(f.avds, i, i+1)
			return nil
		}
	}
	return fmt.Errorf("no AVD %s", name)
}

func (f *fakeAndroid) DeleteSystemImage(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "delete-image "+id)
	for i := range f.images {
		if f.images[i].ID == id {
			f.images = slices.Delete(f.images, i, i+1)
			return nil
		}
	}
	return fmt.Errorf("no image %s", id)
}

const (
	udidLeased  = "11111111-0000-4000-8000-000000000001"
	udidPool    = "22222222-0000-4000-8000-000000000001"
	udidClaimed = "33333333-0000-4000-8000-000000000001"
	udidIdle    = "44444444-0000-4000-8000-000000000001"
	udidRecent  = "55555555-0000-4000-8000-000000000001"
	udidBooted  = "66666666-0000-4000-8000-000000000001"
	udidNever   = "77777777-0000-4000-8000-000000000001"
)

// fixture builds a Mac with one simulator per ownership class plus AVDs
// and runtimes, at a clock of day 100.
func fixture(t *testing.T) (*Manager, *fakeSimctl, *fakeAndroid, *clock, string) {
	t.Helper()
	day := func(n int) time.Time { return time.Unix(0, 0).Add(time.Duration(n) * 24 * time.Hour) }
	c := &clock{t: day(100)}
	f := &fakeSimctl{}
	add := func(udid, name, state string, lastUsed time.Time, bytes int64) {
		f.sims = append(f.sims, Sim{UDID: udid, Name: name, DeviceType: iphone16, Runtime: runtime18,
			State: state, IsAvailable: true, LastUsedAt: lastUsed, DataBytes: bytes,
			DataPath: "/sims/" + udid + "/data"})
	}
	add(udidLeased, "leased", "Shutdown", day(1), 100)
	add(udidPool, "spyder-pool-abcd1234", "Shutdown", day(1), 200)
	add(udidClaimed, "iPad Pro 13-inch (M4)", "Shutdown", day(1), 300)
	add(udidIdle, "yw2-layout-se2", "Shutdown", day(80), 400)
	add(udidRecent, "yw2-layout-16pm", "Shutdown", day(97), 500)
	add(udidBooted, "booted-old", "Booted", day(1), 600)
	add(udidNever, "never-used", "Shutdown", time.Time{}, 700)
	f.images = []RuntimeImage{
		{ImageID: "IMG-17", RuntimeID: runtime17, Version: "17.5", Bytes: 7000, Deletable: true, State: "Ready"},
		{ImageID: "IMG-18", RuntimeID: runtime18, Version: "18.6", Bytes: 8000, Deletable: true, State: "Ready"},
		{ImageID: "IMG-18-2", RuntimeID: "com.apple.CoreSimulator.SimRuntime.iOS-18-2", Version: "18.2", Bytes: 9000, Deletable: true, State: "Ready"},
	}
	a := &fakeAndroid{
		avds: []AVD{
			{Name: "Pixel_old", SystemImage: "system-images;android-34;google_apis;arm64-v8a", LastUsedAt: day(10), Bytes: 1000},
			{Name: "Pixel_live", SystemImage: "system-images;android-34;google_apis;arm64-v8a", Running: true, LastUsedAt: day(10), Bytes: 2000},
		},
		images: []SystemImage{
			{ID: "system-images;android-34;google_apis;arm64-v8a", Version: "android-34", Bytes: 3000},
			{ID: "system-images;android-33;google_apis;arm64-v8a", Version: "android-33", Bytes: 4000},
			{ID: "system-images;android-37.1;google_apis_ps16k;arm64-v8a", Version: "android-37.1", Bytes: 5000},
		},
	}
	dir := t.TempDir()
	m, err := New(&Args{Simctl: f, Android: a, Now: c.now,
		Path:           filepath.Join(dir, "leases.json"),
		ProvenancePath: filepath.Join(dir, "prov.json"),
		AuditPath:      filepath.Join(dir, "prune.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "agent", Project: "yourworld2", Name: "leased"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Claim(ClaimArgs{ID: udidClaimed, Owner: "marcelo", Note: "my iPad"}); err != nil {
		t.Fatal(err)
	}
	return m, f, a, c, dir
}

func deviceByID(inv Inventory, id string) *InventoryDevice {
	for i := range inv.Devices {
		if inv.Devices[i].ID == id {
			return &inv.Devices[i]
		}
	}
	return nil
}

func TestInventoryOwnershipClasses(t *testing.T) {
	m, _, _, _, _ := fixture(t)
	inv := m.Inventory(ctx)
	if inv.SchemaVersion != 1 || len(inv.Errors) != 0 {
		t.Fatalf("schema %d errors %v", inv.SchemaVersion, inv.Errors)
	}
	want := map[string]string{
		udidLeased: OwnerLease, udidPool: OwnerPool, udidClaimed: OwnerClaim,
		udidIdle: OwnerNone, udidBooted: OwnerNone, "Pixel_old": OwnerNone,
	}
	for id, kind := range want {
		d := deviceByID(inv, id)
		if d == nil || d.Owner.Kind != kind {
			t.Fatalf("%s owner = %+v, want %s", id, d, kind)
		}
	}
	if d := deviceByID(inv, udidLeased); d.Owner.Project != "yourworld2" || d.Owner.LeaseID == "" || d.State != "booted" {
		t.Fatalf("leased device = %+v", d)
	}
	if d := deviceByID(inv, udidIdle); d.Path != "/sims/"+udidIdle || d.Bytes != 400 || d.LastUsedSource != "simctl" {
		t.Fatalf("idle device = %+v", d)
	}
	if d := deviceByID(inv, "Pixel_live"); d.State != "running" || d.Kind != KindAndroidAVD {
		t.Fatalf("running AVD = %+v", d)
	}
	rt := map[string]InventoryRuntime{}
	for _, r := range inv.Runtimes {
		rt[r.ID] = r
	}
	if r := rt[runtime18]; r.UsedBy.Count != 7 || r.Keep != "newest" {
		t.Fatalf("iOS 18.6 runtime = %+v", r)
	}
	if r := rt[runtime17]; r.UsedBy.Count != 0 || r.Keep != "" {
		t.Fatalf("iOS 17.5 runtime = %+v", r)
	}
	if r := rt["system-images;android-34;google_apis;arm64-v8a"]; r.UsedBy.Count != 2 {
		t.Fatalf("android-34 = %+v", r)
	}
	if r := rt["system-images;android-37.1;google_apis_ps16k;arm64-v8a"]; r.Keep != "newest" {
		t.Fatalf("android-37.1 = %+v", r)
	}
}

// Who a simulator was made for outlives its lease: release of a booted
// simulator keeps the record; a created one that is deleted is forgotten.
func TestProvenanceSurvivesRelease(t *testing.T) {
	m, f, _, c, dir := fixture(t)
	l, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime17, Owner: "agent", Project: "ge"})
	if err != nil {
		t.Fatal(err)
	}
	if l.Action != ActionCreated {
		t.Fatalf("action = %s", l.Action)
	}
	// Keep the created simulator by booting-lease semantics: release a
	// lease that only booted it, then check the record survives.
	booted, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "other", Project: "yw2", Name: "yw2-layout-16pm"})
	if err != nil || booted.Action != ActionBooted {
		t.Fatalf("booted acquire = %+v %v", booted, err)
	}
	c.t = c.t.Add(time.Hour)
	if _, err := m.Release(ctx, booted.ID, "other"); err != nil {
		t.Fatal(err)
	}
	// A fresh manager (restart) still knows who last leased it.
	m2, err := New(&Args{Simctl: f, Now: c.now, Path: filepath.Join(dir, "leases.json"), ProvenancePath: filepath.Join(dir, "prov.json")})
	if err != nil {
		t.Fatal(err)
	}
	d := deviceByID(m2.Inventory(ctx), udidRecent)
	if d == nil || d.Owner.Kind != OwnerNone || d.LastLeasedBy == nil || d.LastLeasedBy.Project != "yw2" ||
		d.LastUsedSource != "spyder" || !d.LastUsedAt.Equal(c.t) {
		t.Fatalf("released device = %+v", d)
	}
	created := deviceByID(m2.Inventory(ctx), l.UDID)
	if created == nil || !created.CreatedBySpyder || created.CreatedFor == nil || created.CreatedFor.Project != "ge" {
		t.Fatalf("created device = %+v", created)
	}
	if _, err := m.Release(ctx, l.ID, "agent"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	_, kept := m.prov[provKey(l.UDID)]
	m.mu.Unlock()
	if kept {
		t.Fatal("provenance kept for a deleted simulator")
	}
}

// Prune selects exactly the unowned, shut-down, idle devices, and spares
// booted, leased, claimed, pool and recently used ones.
func TestPruneSelectsOnlyUnownedIdleShutdown(t *testing.T) {
	m, f, a, _, dir := fixture(t)
	dry, err := m.PruneDevices(ctx, PruneArgs{IdleDays: 7, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range dry.Candidates {
		ids = append(ids, c.ID)
	}
	slices.Sort(ids)
	want := []string{udidIdle, udidNever, "Pixel_old"}
	slices.Sort(want)
	if !slices.Equal(ids, want) || dry.Bytes != 400+700+1000 || len(dry.Deleted) != 0 {
		t.Fatalf("dry run = %+v", dry)
	}
	for _, call := range f.mutations() {
		if len(call) > 6 && call[:6] == "delete" {
			t.Fatalf("dry run deleted: %s", call)
		}
	}

	if _, err := m.PruneDevices(ctx, PruneArgs{IdleDays: 7}); err == nil {
		t.Fatal("delete without owner accepted")
	}
	res, err := m.PruneDevices(ctx, PruneArgs{IdleDays: 7, Owner: "marcelo"})
	if err != nil || len(res.Deleted) != 3 || len(res.Errors) != 0 {
		t.Fatalf("prune = %+v %v", res, err)
	}
	for _, gone := range []string{udidIdle, udidNever} {
		if f.state(gone) != "gone" {
			t.Fatalf("%s survived prune", gone)
		}
	}
	for _, kept := range []string{udidLeased, udidPool, udidClaimed, udidRecent, udidBooted} {
		if f.state(kept) == "gone" {
			t.Fatalf("%s was pruned", kept)
		}
	}
	if len(a.avds) != 1 || a.avds[0].Name != "Pixel_live" {
		t.Fatalf("AVDs after prune = %+v", a.avds)
	}
	lines := 0
	file, _ := os.Open(filepath.Join(dir, "prune.jsonl"))
	sc := bufio.NewScanner(file)
	for sc.Scan() {
		var e struct{ By, ID string }
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.By != "marcelo" {
			t.Fatalf("audit line %q", sc.Text())
		}
		lines++
	}
	file.Close()
	if lines != 3 {
		t.Fatalf("%d audit lines", lines)
	}
}

// A claimed simulator is not reused by sim_acquire for another owner.
func TestAcquireSkipsClaimed(t *testing.T) {
	f := &fakeSimctl{}
	f.add(udidClaimed, "mine", iphone16, runtime18, "Shutdown")
	m := newManager(t, f, &clock{t: time.Unix(1_800_000_000, 0)}, "")
	if _, err := m.Claim(ClaimArgs{ID: udidClaimed, Owner: "marcelo"}); err != nil {
		t.Fatal(err)
	}
	l, err := m.Acquire(ctx, AcquireArgs{DeviceType: iphone16, Runtime: runtime18, Owner: "agent"})
	if err != nil || l.UDID == udidClaimed || l.Action != ActionCreated {
		t.Fatalf("acquire took claimed sim: %+v %v", l, err)
	}
	if _, err := m.Unclaim(udidClaimed, "agent"); err == nil {
		t.Fatal("unclaim by another owner succeeded")
	}
}

// Runtime prune removes unused images but keeps the newest per platform.
func TestPruneRuntimesKeepsNewestAndUsed(t *testing.T) {
	m, f, a, _, _ := fixture(t)
	dry, err := m.PruneRuntimes(ctx, PruneArgs{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range dry.Candidates {
		ids = append(ids, c.ID)
	}
	slices.Sort(ids)
	want := []string{"IMG-17", "IMG-18-2", "system-images;android-33;google_apis;arm64-v8a"}
	if !slices.Equal(ids, want) || dry.Bytes != 7000+9000+4000 {
		t.Fatalf("runtime dry run = %+v", dry)
	}
	res, err := m.PruneRuntimes(ctx, PruneArgs{Owner: "marcelo"})
	if err != nil || len(res.Deleted) != 3 {
		t.Fatalf("runtime prune = %+v %v", res, err)
	}
	if len(f.images) != 1 || f.images[0].ImageID != "IMG-18" || len(a.images) != 2 {
		t.Fatalf("left images %+v / %+v", f.images, a.images)
	}
}

func TestFSAndroid(t *testing.T) {
	root := t.TempDir()
	avdHome := filepath.Join(root, "avd")
	sdk := filepath.Join(root, "sdk")
	img := filepath.Join(sdk, "system-images", "android-35", "google_apis", "arm64-v8a")
	for _, d := range []string{filepath.Join(avdHome, "Pixel_9.avd"), img} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(avdHome, "Pixel_9.ini"), "avd.ini.encoding=UTF-8\npath="+filepath.Join(avdHome, "Pixel_9.avd")+"\n")
	write(filepath.Join(avdHome, "Pixel_9.avd", "config.ini"), "hw.device.name=pixel_9\nimage.sysdir.1=system-images/android-35/google_apis/arm64-v8a/\n")
	write(filepath.Join(img, "system.img"), string(make([]byte, 64*1024)))

	a := &FSAndroid{AVDHome: avdHome, SDKRoot: sdk}
	avds, err := a.AVDs(ctx)
	if err != nil || len(avds) != 1 {
		t.Fatalf("avds = %+v %v", avds, err)
	}
	if avds[0].SystemImage != "system-images;android-35;google_apis;arm64-v8a" || avds[0].DeviceType != "pixel_9" || avds[0].LastUsedAt.IsZero() {
		t.Fatalf("avd = %+v", avds[0])
	}
	imgs, err := a.SystemImages(ctx)
	if err != nil || len(imgs) != 1 || imgs[0].ID != "system-images;android-35;google_apis;arm64-v8a" || imgs[0].Bytes < 64*1024 {
		t.Fatalf("images = %+v %v", imgs, err)
	}
	if err := a.DeleteAVD(ctx, "../evil"); err == nil {
		t.Fatal("path-traversal AVD name accepted")
	}
	if err := a.DeleteSystemImage(ctx, "system-images;..;x;y"); err == nil {
		t.Fatal("path-traversal image id accepted")
	}
	if err := a.DeleteAVD(ctx, "Pixel_9"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(avdHome, "Pixel_9.avd")); !os.IsNotExist(err) {
		t.Fatal("AVD directory survived delete")
	}
	if err := a.DeleteSystemImage(ctx, imgs[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(sdk, "system-images", "android-35")); !os.IsNotExist(err) {
		t.Fatal("empty API directory left behind")
	}
}

func TestParseRunningAVDs(t *testing.T) {
	ps := "/Users/m/Library/Android/sdk/emulator/qemu/darwin-aarch64/qemu-system-aarch64 -netdelay none -avd Pixel_9 -no-window\n" +
		"/Users/m/Library/Android/sdk/emulator/emulator @Tablet_API35\n/usr/bin/vim -avd notme\n"
	got := parseRunningAVDs(ps)
	if !got["Pixel_9"] || !got["Tablet_API35"] || got["notme"] || len(got) != 2 {
		t.Fatalf("running = %v", got)
	}
}

func TestParseRuntimeImagesJSON(t *testing.T) {
	js := `{"5C11":{"identifier":"5C11","runtimeIdentifier":"com.apple.CoreSimulator.SimRuntime.iOS-18-3","version":"18.3.1","sizeBytes":8708125252,"deletable":true,"state":"Ready","path":"/x.dmg"}}`
	imgs, err := ParseRuntimeImagesJSON([]byte(js))
	if err != nil || len(imgs) != 1 || imgs[0].Bytes != 8708125252 || imgs[0].RuntimeID != "com.apple.CoreSimulator.SimRuntime.iOS-18-3" {
		t.Fatalf("images = %+v %v", imgs, err)
	}
}

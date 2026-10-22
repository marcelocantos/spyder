// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goios_ios "github.com/danielpaulus/go-ios/ios"
)

// 🎯T170: concurrent List callers share one census instead of each running
// usbmux enumeration, lockdown reads and devicectl.
func TestList_ConcurrentCallersShareOneCensus(t *testing.T) {
	hideDevicectl(t)
	var censuses atomic.Int32
	release := make(chan struct{})
	a := &IOSAdapter{
		listDevices: func() (goios_ios.DeviceList, error) {
			censuses.Add(1)
			<-release
			return stubDevices("phone"), nil
		},
	}
	const callers = 8
	var wg sync.WaitGroup
	results := make([][]Info, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _ = a.List()
		}()
	}
	// Let every caller reach List before the census completes.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := censuses.Load(); got != 1 {
		t.Fatalf("censuses = %d, want 1 shared by %d callers", got, callers)
	}
	for i, r := range results {
		if strings.Join(infoUUIDs(r), ",") != "phone" {
			t.Fatalf("caller %d got %v", i, infoUUIDs(r))
		}
	}
	// A caller after the census finished runs a fresh one.
	if _, err := a.List(); err != nil {
		t.Fatal(err)
	}
	if got := censuses.Load(); got != 2 {
		t.Fatalf("censuses after a later call = %d, want 2", got)
	}
}

// 🎯T170: lockdown identity is read once per attached device, not once per
// listing, and is read again after the device leaves and comes back.
func TestList_CachesLockdownIdentityWhileAttached(t *testing.T) {
	hideDevicectl(t)
	serials := []string{"phone"}
	reads := map[string]int{}
	a := &IOSAdapter{
		listDevices: func() (goios_ios.DeviceList, error) {
			return stubDevices(serials...), nil
		},
	}
	a.deviceValues = func(dev goios_ios.DeviceEntry) (goios_ios.GetAllValuesResponse, error) {
		udid := dev.Properties.SerialNumber
		reads[udid]++
		var v goios_ios.GetAllValuesResponse
		v.Value.DeviceName = "name-" + udid
		v.Value.ProductVersion = "18.0"
		return v, nil
	}
	for range 3 {
		list, err := a.List()
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].Name != "name-phone" {
			t.Fatalf("list = %+v", list)
		}
	}
	if reads["phone"] != 1 {
		t.Fatalf("lockdown reads = %d over three listings, want 1", reads["phone"])
	}
	serials = nil
	if _, err := a.List(); err != nil {
		t.Fatal(err)
	}
	serials = []string{"phone"}
	if _, err := a.List(); err != nil {
		t.Fatal(err)
	}
	if reads["phone"] != 2 {
		t.Fatalf("lockdown reads after replug = %d, want 2", reads["phone"])
	}
}

// A failed lockdown read is not cached: the next listing probes again.
func TestList_DoesNotCacheFailedLockdownRead(t *testing.T) {
	hideDevicectl(t)
	var reads atomic.Int32
	a := &IOSAdapter{
		listDevices: func() (goios_ios.DeviceList, error) {
			return stubDevices("phone"), nil
		},
	}
	a.deviceValues = func(goios_ios.DeviceEntry) (goios_ios.GetAllValuesResponse, error) {
		reads.Add(1)
		return goios_ios.GetAllValuesResponse{}, errors.New("lockdown not ready")
	}
	for range 2 {
		if _, err := a.List(); err != nil {
			t.Fatal(err)
		}
	}
	if got := reads.Load(); got != 2 {
		t.Fatalf("lockdown reads = %d, want 2 (failures are not cached)", got)
	}
}

// 🎯T169: one devicectl census per listing feeds both the connected filter
// (TunnelPending) and the merge of usbmux-invisible devices.
func TestList_OneDevicectlCensusFeedsFilterAndMerge(t *testing.T) {
	var runs atomic.Int32
	a := &IOSAdapter{
		listDevices: func() (goios_ios.DeviceList, error) {
			return stubDevices("phone", "pad"), nil
		},
		deviceValues: func(dev goios_ios.DeviceEntry) (goios_ios.GetAllValuesResponse, error) {
			var v goios_ios.GetAllValuesResponse
			v.Value.ProductVersion = "18.0"
			return v, nil
		},
		devicectlList: func(ctx context.Context) ([]byte, error) {
			runs.Add(1)
			if _, ok := ctx.Deadline(); !ok {
				t.Error("devicectl census has no deadline")
			}
			return []byte(`{"result":{"devices":[
				{"identifier":"core-phone","hardwareProperties":{"udid":"phone","platform":"iOS","marketingName":"iPhone 13"},
				 "connectionProperties":{"tunnelState":"connected","transportType":"wired"},"deviceProperties":{"name":"Phone"}}
			]}}`), nil
		},
	}
	list, err := a.List()
	if err != nil {
		t.Fatal(err)
	}
	if got := runs.Load(); got != 1 {
		t.Fatalf("devicectl runs = %d, want 1", got)
	}
	byUDID := map[string]Info{}
	for _, d := range list {
		byUDID[d.UUID] = d
	}
	if d := byUDID["phone"]; d.TunnelPending || d.Name != "Phone" {
		t.Fatalf("phone = %+v, want connected and named from devicectl", d)
	}
	if d := byUDID["pad"]; !d.TunnelPending {
		t.Fatalf("pad = %+v, want tunnel pending (not in devicectl's connected set)", d)
	}
}

// A devicectl failure leaves the usbmux rows intact (filter unavailable).
func TestList_DevicectlFailureKeepsUsbmuxRows(t *testing.T) {
	a := &IOSAdapter{
		listDevices: func() (goios_ios.DeviceList, error) {
			return stubDevices("phone"), nil
		},
		deviceValues: func(goios_ios.DeviceEntry) (goios_ios.GetAllValuesResponse, error) {
			var v goios_ios.GetAllValuesResponse
			v.Value.ProductVersion = "18.0"
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
	if len(list) != 1 || list[0].UUID != "phone" || list[0].TunnelPending {
		t.Fatalf("list = %+v, want phone without the pending flag", list)
	}
}

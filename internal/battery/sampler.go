// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package battery

import (
	"context"
	"log/slog"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
)

// SamplerArgs constructs a Sampler. Interval and Retention default to
// DefaultInterval / DefaultRetention when zero. Now defaults to time.Now.
type SamplerArgs struct {
	Store     *Store
	List      func() ([]device.Info, error)
	State     func(id string) (device.State, error)
	Interval  time.Duration
	Retention time.Duration
	Now       func() time.Time
}

// Sampler periodically Collects connected-device battery and appends
// to a Store.
type Sampler struct {
	store     *Store
	list      func() ([]device.Info, error)
	state     func(id string) (device.State, error)
	interval  time.Duration
	retention time.Duration
	now       func() time.Time
}

// NewSampler returns a sampler. Store, List, and State must be set.
func NewSampler(args SamplerArgs) *Sampler {
	interval := args.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	retention := args.Retention
	if retention <= 0 {
		retention = DefaultRetention
	}
	now := args.Now
	if now == nil {
		now = time.Now
	}
	return &Sampler{
		store:     args.Store,
		list:      args.List,
		state:     args.State,
		interval:  interval,
		retention: retention,
		now:       now,
	}
}

// Tick lists connected devices, appends Collect results, and prunes.
func (s *Sampler) Tick() error {
	if s == nil || s.store == nil || s.list == nil || s.state == nil {
		return nil
	}
	now := s.now()
	infos, err := s.list()
	if err != nil {
		return err
	}
	for _, sm := range Collect(now, infos, s.state) {
		if err := s.store.Append(sm); err != nil {
			slog.Warn("battery: append failed", "device", sm.DeviceID, "error", err)
		}
	}
	if err := s.store.Prune(now, s.retention); err != nil {
		slog.Warn("battery: prune failed", "error", err)
	}
	return nil
}

// Run ticks immediately, then on Interval until ctx is cancelled.
func (s *Sampler) Run(ctx context.Context) {
	if s == nil {
		return
	}
	if err := s.Tick(); err != nil {
		slog.Warn("battery: initial tick failed", "error", err)
	}
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Tick(); err != nil {
				slog.Warn("battery: tick failed", "error", err)
			}
		}
	}
}

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package devpolicy stores host-local development flags and reconciles short
// leases against freshly verified physical USB connections. It has no defaults.
package devpolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	SchemaVersion        = 1
	MaxConfigBytes       = 1 << 20
	MaxOwnerBytes        = 256
	MaxGroups            = 64
	MaxApps              = 512
	MaxFlagsPerScope     = 256
	MaxFlagKeyBytes      = 128
	MaxFlagValueBytes    = 4096
	MaxGroupNameBytes    = 128
	MinBundlePrefixBytes = 3
)

type Flags map[string]string

type Group struct {
	Name           string   `json:"name"`
	BundleIDs      []string `json:"bundle_ids,omitempty"`
	BundlePrefixes []string `json:"bundle_prefixes,omitempty"`
	Flags          Flags    `json:"flags"`
}

type Config struct {
	Version int              `json:"version"`
	Owner   string           `json:"owner,omitempty"`
	Global  Flags            `json:"global"`
	Groups  []Group          `json:"groups"`
	Apps    map[string]Flags `json:"apps"`
}

type Resolution struct {
	Flags      Flags             `json:"flags"`
	Provenance map[string]string `json:"provenance"`
}

func Empty() Config {
	return Config{Version: SchemaVersion, Global: Flags{}, Groups: []Group{}, Apps: map[string]Flags{}}
}

func (c Config) Validate() error {
	if c.Version != SchemaVersion {
		return fmt.Errorf("unsupported dev policy version %d", c.Version)
	}
	if len(c.Owner) > MaxOwnerBytes || len(c.Groups) > MaxGroups || len(c.Apps) > MaxApps {
		return errors.New("dev policy exceeds owner/group/app limits")
	}
	names := map[string]bool{}
	check := func(flags Flags) error {
		if len(flags) > MaxFlagsPerScope {
			return errors.New("at most 256 flags per scope")
		}
		for k, v := range flags {
			if strings.TrimSpace(k) == "" || len(k) > MaxFlagKeyBytes || len(v) > MaxFlagValueBytes {
				return errors.New("flag keys must be nonempty and at most 128 bytes; values at most 4096 bytes")
			}
		}
		return nil
	}
	if err := check(c.Global); err != nil {
		return err
	}
	for _, g := range c.Groups {
		if strings.TrimSpace(g.Name) == "" || len(g.Name) > MaxGroupNameBytes || names[g.Name] {
			return errors.New("group names must be nonempty and unique")
		}
		names[g.Name] = true
		if len(g.BundleIDs)+len(g.BundlePrefixes) == 0 {
			return fmt.Errorf("group %s needs explicit bundle membership", g.Name)
		}
		for _, id := range g.BundleIDs {
			if strings.TrimSpace(id) == "" {
				return errors.New("empty bundle ID")
			}
		}
		for _, p := range g.BundlePrefixes {
			if !strings.HasSuffix(p, ".") || len(p) < MinBundlePrefixBytes {
				return errors.New("bundle prefixes must end in a dot and include a namespace")
			}
		}
		if err := check(g.Flags); err != nil {
			return err
		}
	}
	for id, f := range c.Apps {
		if strings.TrimSpace(id) == "" {
			return errors.New("empty app bundle ID")
		}
		if err := check(f); err != nil {
			return err
		}
	}
	return nil
}

func (c Config) Resolve(bundle string, override Flags) Resolution {
	r := Resolution{Flags: Flags{}, Provenance: map[string]string{}}
	merge := func(f Flags, source string) {
		for k, v := range f {
			r.Flags[k] = v
			r.Provenance[k] = source
		}
	}
	merge(c.Global, "global")
	for _, g := range c.Groups {
		match := false
		for _, id := range g.BundleIDs {
			if bundle == id {
				match = true
			}
		}
		for _, p := range g.BundlePrefixes {
			if strings.HasPrefix(bundle, p) {
				match = true
			}
		}
		if match {
			merge(g.Flags, "group:"+g.Name)
		}
	}
	merge(c.Apps[bundle], "app:"+bundle)
	merge(override, "session")
	return r
}

type Store struct {
	mu   sync.Mutex
	Path string
}

func (s *Store) Load() (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Empty(), nil
	}
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxConfigBytes+1))
	if err != nil {
		return Config{}, err
	}
	if len(b) > MaxConfigBytes {
		return Config{}, errors.New("dev policy exceeds 1 MiB")
	}
	var c Config
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return Config{}, err
	}
	var tail any
	if err = d.Decode(&tail); err != io.EOF {
		return Config{}, errors.New("dev policy must contain one JSON object")
	}
	return c, c.Validate()
}
func (s *Store) Save(c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > MaxConfigBytes {
		return errors.New("dev policy exceeds 1 MiB")
	}
	dir := filepath.Dir(s.Path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".dev-policy-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.Path)
}

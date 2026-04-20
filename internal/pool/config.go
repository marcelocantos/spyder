// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package pool manages a warm sim/emu pool with two readiness tiers:
// available (created on disk, not booted) and running (booted, idle).
// Clients call Acquire/Release; linger, warm-pool size, and lifecycle
// decisions are entirely server-side.
package pool

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// defaultLingerSeconds is the global fallback linger duration when
// SPYDER_POOL_LINGER_SECONDS is not set and the template has no
// per-template override.
const defaultLingerSeconds = 120

// lingerEnv is the environment variable that overrides the global linger
// duration (per-template values still take precedence).
const lingerEnv = "SPYDER_POOL_LINGER_SECONDS"

// TemplateConfig is the per-template declaration in pool.yaml.
type TemplateConfig struct {
	// Name is a unique, human-readable identifier for this template.
	Name string `yaml:"name"`

	// Platform is "ios" or "android".
	Platform string `yaml:"platform"`

	// DeviceType is the sim device-type identifier (iOS) or AVD device
	// profile (Android). For iOS: full simctl identifier string, e.g.
	// "com.apple.CoreSimulator.SimDeviceType.iPhone-16". For Android:
	// avdmanager device ID, e.g. "pixel_9".
	DeviceType string `yaml:"device_type"`

	// RuntimeOrSystemImage is the runtime identifier (iOS) or system-image
	// package (Android).
	// iOS:     e.g. "com.apple.CoreSimulator.SimRuntime.iOS-18-3"
	// Android: e.g. "system-images;android-35;google_apis;arm64-v8a"
	RuntimeOrSystemImage string `yaml:"runtime_or_system_image"`

	// Tags is an optional set of labels for fuzzy selector matching (T23).
	Tags []string `yaml:"tags,omitempty"`

	// AvailableMin is the floor for the available tier: Reconcile will
	// create new instances until at least this many exist (not booted).
	AvailableMin int `yaml:"available_min"`

	// AvailableMax is the cap for the available tier: when a running
	// instance transitions to available after linger expires, and the
	// available tier is already at cap, the instance is deleted instead.
	AvailableMax int `yaml:"available_max"`

	// RunningWarm is the number of instances Reconcile will pre-boot.
	// Keeping a small warm pool enables near-instant acquisition.
	RunningWarm int `yaml:"running_warm"`

	// LingerSeconds overrides the global SPYDER_POOL_LINGER_SECONDS for
	// this template. Zero means "use the global value".
	LingerSeconds int `yaml:"linger_seconds,omitempty"`
}

// Config is the top-level pool.yaml structure.
type Config struct {
	Templates []TemplateConfig `yaml:"templates"`
}

// LingerDuration returns the effective linger duration for t: the
// template-local value beats the env override, which beats the built-in
// default.
func (t *TemplateConfig) LingerDuration() time.Duration {
	if t.LingerSeconds > 0 {
		return time.Duration(t.LingerSeconds) * time.Second
	}
	if v := os.Getenv(lingerEnv); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return defaultLingerSeconds * time.Second
}

// LoadConfig reads and parses a pool.yaml file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("pool config: read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("pool config: parse %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("pool config: %w", err)
	}
	return &cfg, nil
}

// validate checks for required fields and duplicate names.
func (c *Config) validate() error {
	seen := map[string]bool{}
	for i, t := range c.Templates {
		if t.Name == "" {
			return fmt.Errorf("template[%d]: name is required", i)
		}
		if seen[t.Name] {
			return fmt.Errorf("template[%d]: duplicate name %q", i, t.Name)
		}
		seen[t.Name] = true
		if t.Platform != "ios" && t.Platform != "android" {
			return fmt.Errorf("template %q: platform must be ios or android, got %q", t.Name, t.Platform)
		}
		if t.DeviceType == "" {
			return fmt.Errorf("template %q: device_type is required", t.Name)
		}
		if t.RuntimeOrSystemImage == "" {
			return fmt.Errorf("template %q: runtime_or_system_image is required", t.Name)
		}
		if t.AvailableMax < t.AvailableMin {
			return fmt.Errorf("template %q: available_max (%d) must be >= available_min (%d)",
				t.Name, t.AvailableMax, t.AvailableMin)
		}
	}
	return nil
}

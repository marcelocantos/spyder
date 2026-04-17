// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import "errors"

// AndroidAdapter is a stub — Android support is deferred beyond the initial
// iOS-first milestone. Present so the Adapter seam is exercised cross-platform
// from the start.
type AndroidAdapter struct{}

// NewAndroidAdapter returns a new Android adapter stub.
func NewAndroidAdapter() *AndroidAdapter { return &AndroidAdapter{} }

func (a *AndroidAdapter) List() ([]Info, error) {
	return nil, nil // empty list, not an error — Android not yet supported
}

func (a *AndroidAdapter) State(id string) (State, error) {
	return State{}, errors.New("Android support not yet implemented")
}

func (a *AndroidAdapter) LaunchKeepAwake(id string) error {
	return errors.New("Android support not yet implemented")
}

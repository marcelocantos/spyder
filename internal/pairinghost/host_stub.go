// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

//go:build !pairinglive

package pairinghost

import "context"

// attachLivePairer is a no-op in the default build. Live ceremony is
// compiled only with -tags pairinglive (see host_live.go) because
// pigeon/pairing pulls cwire + a quic-go version that conflicts with
// the go-ios pin used by spyder's tunnel path.
func (h *Host) attachLivePairer(_ context.Context) error {
	h.pairer = nil
	return nil
}

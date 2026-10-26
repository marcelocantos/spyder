// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import "context"

type dispatchOpKey struct{}

func withDispatchOpID(ctx context.Context, id uint64) context.Context {
	return context.WithValue(ctx, dispatchOpKey{}, id)
}

func dispatchOpID(ctx context.Context) uint64 {
	if v, ok := ctx.Value(dispatchOpKey{}).(uint64); ok {
		return v
	}
	return 0
}

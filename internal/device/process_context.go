// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import "context"

// ContextProcessAdapter is an optional capability for providers whose process
// service reads own a cancellable connection. Opaque connection setup can still
// block; callers retain their existing timeout/watchdog fallback for that case.
type ContextProcessAdapter interface {
	AppPIDContext(context.Context, string, string) (int, error)
	TerminateAppContext(context.Context, string, string) error
	ListAppsContext(context.Context, string) ([]AppInfo, error)
}

// These optional capabilities retain Adapter compatibility while allowing
// providers with owned service connections to interrupt established I/O.
func AppPIDContext(ctx context.Context, adapter Adapter, id, bundleID string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if a, ok := adapter.(interface {
		AppPIDContext(context.Context, string, string) (int, error)
	}); ok {
		return a.AppPIDContext(ctx, id, bundleID)
	}
	pid, err := adapter.AppPID(id, bundleID)
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	return pid, err
}

func TerminateAppContext(ctx context.Context, adapter Adapter, id, bundleID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a, ok := adapter.(interface {
		TerminateAppContext(context.Context, string, string) error
	}); ok {
		return a.TerminateAppContext(ctx, id, bundleID)
	}
	err := adapter.TerminateApp(id, bundleID)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func ListAppsContext(ctx context.Context, adapter Adapter, id string) ([]AppInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a, ok := adapter.(interface {
		ListAppsContext(context.Context, string) ([]AppInfo, error)
	}); ok {
		return a.ListAppsContext(ctx, id)
	}
	apps, err := adapter.ListApps(id)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return apps, err
}

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package pmd3bridge provides a typed Go client and process supervisor for the
// pmd3-bridge FastAPI subprocess. The bridge exposes pymobiledevice3 operations
// over a Unix-domain socket using a JSON/HTTP protocol, enabling the spyder
// daemon to call pmd3 operations without the ~1 s Python startup overhead per
// call.
//
// # Client
//
// Client wraps a standard http.Client whose transport dials over a Unix socket.
// One typed method is provided per bridge endpoint; every method accepts a
// context.Context as its first argument and honours cancellation.
//
// # Supervisor
//
// Supervisor owns the bridge subprocess lifecycle: it starts the binary,
// waits for the "ready\n" signal on stdout, and restarts the process on
// unexpected exit using a bounded exponential backoff. Cleanup (socket removal)
// is performed on Stop and before each restart attempt.
package pmd3bridge

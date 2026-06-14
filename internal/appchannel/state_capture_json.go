// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import "encoding/json"

// jsonMarshal forwards to encoding/json.Marshal. Kept in a separate
// file so the package's main types don't pull encoding/json into their
// import surface directly.
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

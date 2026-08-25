// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

//go:build darwin && !cgo

package ship

import "fmt"

func GetItem(service, account string) ([]byte, error) {
	return nil, fmt.Errorf("studio keychain requires cgo (build with CGO_ENABLED=1)")
}

func SetItem(service, account string, value []byte) error {
	return fmt.Errorf("studio keychain requires cgo (build with CGO_ENABLED=1)")
}

func DeleteItem(service, account string) error {
	return fmt.Errorf("studio keychain requires cgo (build with CGO_ENABLED=1)")
}

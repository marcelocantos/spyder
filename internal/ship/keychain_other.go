// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

//go:build !darwin

package ship

import "fmt"

func GetItem(service, account string) ([]byte, error) {
	return nil, fmt.Errorf("studio keychain is darwin-only")
}

func SetItem(service, account string, value []byte) error {
	return fmt.Errorf("studio keychain is darwin-only")
}

func DeleteItem(service, account string) error {
	return fmt.Errorf("studio keychain is darwin-only")
}

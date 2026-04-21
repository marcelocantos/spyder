// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package pmd3bridge

import "fmt"

// BridgeError is the structured error returned by the bridge on 4xx/5xx
// responses. It carries the error code (machine-readable) and a human-readable
// message, plus the HTTP status code.
type BridgeError struct {
	Code    string
	Message string
	Status  int
}

func (e *BridgeError) Error() string {
	return fmt.Sprintf("bridge error %s (%d): %s", e.Code, e.Status, e.Message)
}

// bridgeErrorBody is the JSON shape returned by the bridge on error.
type bridgeErrorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// IsDeviceNotPaired reports whether err indicates the device is not paired with
// the host (bridge error code "device_not_paired").
func IsDeviceNotPaired(err error) bool {
	var be *BridgeError
	if asErr(err, &be) {
		return be.Code == "device_not_paired"
	}
	return false
}

// IsBundleNotInstalled reports whether err indicates the bundle is not
// installed on the device (bridge error code "bundle_not_installed").
func IsBundleNotInstalled(err error) bool {
	var be *BridgeError
	if asErr(err, &be) {
		return be.Code == "bundle_not_installed"
	}
	return false
}

// IsTunneldUnavailable reports whether err indicates tunneld is not running
// (bridge error code "tunneld_unavailable").
func IsTunneldUnavailable(err error) bool {
	var be *BridgeError
	if asErr(err, &be) {
		return be.Code == "tunneld_unavailable"
	}
	return false
}

// IsPMD3Error reports whether err is a generic pymobiledevice3 error
// (bridge error code "pmd3_error").
func IsPMD3Error(err error) bool {
	var be *BridgeError
	if asErr(err, &be) {
		return be.Code == "pmd3_error"
	}
	return false
}

// asErr is a type-assertion helper that avoids importing the errors package for
// a simple *BridgeError check (errors.As would also work but adds a package dep
// for callers that don't already import it).
func asErr(err error, target **BridgeError) bool {
	if err == nil {
		return false
	}
	if be, ok := err.(*BridgeError); ok {
		*target = be
		return true
	}
	return false
}

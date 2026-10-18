// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package goios

import "testing"

func TestBootSessionUUID_FromHandshakeProperties(t *testing.T) {
	msg := map[string]interface{}{
		"MessageType": "Handshake",
		"Properties": map[string]interface{}{
			"BootSessionUUID": "aeaf223a-9447-4771-bc4c-1572f330b68c",
			"UniqueDeviceID":  "00008020-001950CC01EA002E",
		},
	}
	if got := bootSessionUUID(msg); got != "aeaf223a-9447-4771-bc4c-1572f330b68c" {
		t.Fatalf("boot id %q", got)
	}
	if bootSessionUUID(map[string]interface{}{"Properties": map[string]interface{}{}}) != "" {
		t.Fatal("missing uuid was not empty")
	}
	if bootSessionUUID(nil) != "" {
		t.Fatal("nil message was not empty")
	}
}

// uuidText is the shape go-ios stores for a live BootSessionUUID:
// a uuid.UUID value, not a string. The parser must call String().
type uuidText string

func (u uuidText) String() string { return string(u) }

func TestBootSessionUUID_FromUUIDValue(t *testing.T) {
	const id = "c15cde9a-6fab-4fa0-b456-0dbdbb92830e"
	msg := map[string]interface{}{
		"Properties": map[string]interface{}{
			"BootSessionUUID": uuidText(id),
		},
	}
	if got := bootSessionUUID(msg); got != id {
		t.Fatalf("boot id %q", got)
	}
	blank := map[string]interface{}{
		"Properties": map[string]interface{}{
			"BootSessionUUID": uuidText(""),
		},
	}
	if bootSessionUUID(blank) != "" {
		t.Fatal("empty stringer was not empty")
	}
}

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"errors"
	"testing"
)

const dumpsysFixture = `Packages:
  Package [com.other.app] (1a2b3c):
    versionCode=7 minSdk=24 targetSdk=34
    versionName=9.9
  Package [com.example.scr] (4d5e6f):
    userId=10234
    versionCode=241 minSdk=26 targetSdk=34
    versionName=2.4.1
    flags=[ HAS_CODE ALLOW_CLEAR_USER_DATA ]
`

func TestParseDumpsysPackage_PicksNamedPackage(t *testing.T) {
	v, err := parseDumpsysPackage(dumpsysFixture, "com.example.scr")
	if err != nil {
		t.Fatal(err)
	}
	if v.Version != "2.4.1" || v.Build != "241" || v.Name != "" {
		t.Errorf("got %+v", v)
	}
}

func TestParseDumpsysPackage_NotInstalled(t *testing.T) {
	_, err := parseDumpsysPackage("Unable to find package: com.nope\n", "com.nope")
	if !errors.Is(err, ErrAppNotInstalled) {
		t.Errorf("err = %v; want ErrAppNotInstalled", err)
	}
}

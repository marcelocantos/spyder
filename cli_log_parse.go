// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"flag"
	"io"
	"os"

	"github.com/marcelocantos/spyder/internal/cliexit"
	"github.com/marcelocantos/spyder/internal/device"
)

// runLogParse normalizes Android or iOS log text on stdin into the
// diagnostic schema, one JSON object per line. It does not talk to the
// daemon: the parser is the shared contract the regression harness uses
// instead of a second platform log parser.
func runLogParse(args []string) {
	fs := flag.NewFlagSet("log parse", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	platform := fs.String("platform", "", "android or ios")
	channels := fs.Bool("channels", false, "print the crash-channel contract and exit")
	if err := fs.Parse(args); err != nil {
		cliexit.Errorf(cliexit.ExitUsage, "spyder log parse: %v", err)
	}
	if *channels {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(device.CrashChannels()); err != nil {
			cliexit.Errorf(cliexit.ExitGeneric, "spyder log parse: %v", err)
		}
		return
	}
	if *platform != "android" && *platform != "ios" {
		cliexit.Errorf(cliexit.ExitUsage, "spyder log parse: --platform android|ios is required")
	}
	if err := device.ParseDiagnosticStream(*platform, os.Stdin, os.Stdout); err != nil {
		cliexit.Errorf(cliexit.ExitGeneric, "spyder log parse: %v", err)
	}
}

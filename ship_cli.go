// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/marcelocantos/spyder/internal/ship"
)

// runSecret dispatches `spyder secret <subcommand>` (🎯T133). These
// commands talk to the keychain in-process and must work with the
// daemon down. No MCP/REST secret verbs.
func runSecret(args []string) {
	if len(args) < 1 {
		fatalUsage("secret", fmt.Errorf("missing subcommand — expected status|import|mint|missing"))
	}
	switch args[0] {
	case "status":
		runSecretStatus(args[1:])
	case "import", "mint", "missing":
		fmt.Fprintf(os.Stderr, "secret %s: not implemented yet (see docs/ship-front-door.md / 🎯T133.2+)\n", args[0])
		os.Exit(2)
	default:
		fatalUsage("secret", fmt.Errorf("unknown subcommand %q — expected status|import|mint|missing", args[0]))
	}
}

func runSecretStatus(args []string) {
	studio := ""
	for len(args) > 0 {
		switch args[0] {
		case "--studio":
			if len(args) < 2 {
				fatalUsage("secret status", fmt.Errorf("--studio requires squz|minicades"))
			}
			studio = args[1]
			args = args[2:]
		default:
			fatalUsage("secret status", fmt.Errorf("unknown flag %q", args[0]))
		}
	}

	sig, err := ship.InspectSelf()
	if err != nil {
		fmt.Fprintf(os.Stderr, "secret status: codesign inspect: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("codesign: %s\n", ship.FormatSignature(sig))
	fmt.Printf("path: %s\n", sig.Path)

	if err := ship.RequireSecretsAccess(); err != nil {
		fmt.Printf("secrets: refused (%v)\n", err)
	} else {
		fmt.Printf("secrets: allowed\n")
	}

	studios := []string{ship.StudioSquz, ship.StudioMinicades}
	if studio != "" {
		studio = strings.ToLower(studio)
		if _, ok := ship.AppleTeamID[studio]; !ok {
			fatalUsage("secret status", fmt.Errorf("unknown studio %q (want squz|minicades)", studio))
		}
		studios = []string{studio}
	}
	for _, s := range studios {
		env, err := ship.LoadStudio(s)
		switch {
		case err != nil:
			fmt.Printf("studio %s: error: %v\n", s, err)
		default:
			st := env.RedactedStatus(s)
			kinds := []string{}
			for k, ok := range st.Present {
				if ok {
					kinds = append(kinds, k)
				}
			}
			if len(kinds) == 0 {
				fmt.Printf("studio %s: envelope empty (team %s)\n", s, st.TeamID)
				continue
			}
			fmt.Printf("studio %s: present=%v fingerprints=%v\n", s, kinds, st.Fingerprints)
		}
	}
}

// runFastlane is the consumer front door (🎯T133.4). Stub until wrap lands;
// still enforces the codesign gate so unsigned binaries cannot rehearse.
func runFastlane(args []string) {
	if err := ship.RequireSecretsAccess(); err != nil {
		fmt.Fprintf(os.Stderr, "fastlane: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "fastlane: not implemented yet (see docs/ship-front-door.md / 🎯T133.4); args=%v\n", args)
	os.Exit(2)
}

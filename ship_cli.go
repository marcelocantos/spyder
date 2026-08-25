// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

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
	case "missing":
		runSecretMissing(args[1:])
	case "import":
		runSecretImport(args[1:])
	case "mint":
		fmt.Fprintf(os.Stderr, "secret mint: not implemented yet (see docs/ship-front-door.md / 🎯T133.3)\n")
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
			sort.Strings(kinds)
			if len(kinds) == 0 {
				fmt.Printf("studio %s: envelope empty (team %s)\n", s, st.TeamID)
				continue
			}
			fmt.Printf("studio %s: present=%v fingerprints=%v\n", s, kinds, st.Fingerprints)
		}
	}
}

func runSecretMissing(args []string) {
	studio, forLane := "", ""
	for len(args) > 0 {
		switch args[0] {
		case "--studio":
			if len(args) < 2 {
				fatalUsage("secret missing", fmt.Errorf("--studio requires squz|minicades"))
			}
			studio = args[1]
			args = args[2:]
		case "--for":
			if len(args) < 2 {
				fatalUsage("secret missing", fmt.Errorf("--for requires match|pilot|deliver|supply|firebase"))
			}
			forLane = args[1]
			args = args[2:]
		default:
			fatalUsage("secret missing", fmt.Errorf("unknown flag %q", args[0]))
		}
	}
	if studio == "" || forLane == "" {
		fatalUsage("secret missing", fmt.Errorf("require --studio and --for"))
	}
	if err := ship.RequireSecretsAccess(); err != nil {
		fmt.Fprintf(os.Stderr, "secret missing: %v\n", err)
		os.Exit(1)
	}
	env, err := ship.LoadStudio(studio)
	if err != nil {
		fmt.Fprintf(os.Stderr, "secret missing: %v\n", err)
		os.Exit(1)
	}
	missing, err := env.MissingKinds(ship.LaneFor(forLane))
	if err != nil {
		fatalUsage("secret missing", err)
	}
	if len(missing) == 0 {
		fmt.Printf("ok: studio %s has kinds for %s\n", studio, forLane)
		return
	}
	fmt.Printf("missing: %s\n", strings.Join(missing, ","))
	os.Exit(20)
}

func runSecretImport(args []string) {
	studio, now, noVerify := "", false, false
	wait := 2 * time.Minute
	for len(args) > 0 {
		switch args[0] {
		case "--studio":
			if len(args) < 2 {
				fatalUsage("secret import", fmt.Errorf("--studio requires squz|minicades"))
			}
			studio = args[1]
			args = args[2:]
		case "--now":
			now = true
			args = args[1:]
		case "--no-verify":
			noVerify = true
			args = args[1:]
		case "--wait":
			if len(args) < 2 {
				fatalUsage("secret import", fmt.Errorf("--wait requires a duration"))
			}
			d, err := time.ParseDuration(args[1])
			if err != nil {
				fatalUsage("secret import", err)
			}
			wait = d
			args = args[2:]
		default:
			fatalUsage("secret import", fmt.Errorf("unknown flag %q", args[0]))
		}
	}
	if studio == "" {
		fatalUsage("secret import", fmt.Errorf("require --studio squz|minicades"))
	}
	if err := ship.RequireSecretsAccess(); err != nil {
		fmt.Fprintf(os.Stderr, "secret import: %v\n", err)
		os.Exit(1)
	}
	_ = noVerify // live verify lands with T133.3 verify path

	pb := ship.DefaultPasteboard
	absorb := func(text string) error {
		d, err := ship.AbsorbOnce(studio, text)
		if err != nil {
			return err
		}
		fmt.Printf("absorbed:")
		if d.ASCIssuerID != "" {
			fmt.Printf(" asc.issuer_id")
		}
		if d.ASCKeyID != "" {
			fmt.Printf(" asc.key_id")
		}
		if d.ASCP8 != "" {
			fmt.Printf(" asc.p8")
		}
		if len(d.PlaySAJSON) > 0 {
			fmt.Printf(" play_service_account(%s)", d.PlaySAEmail)
		}
		if len(d.FirebaseAdminJSON) > 0 {
			fmt.Printf(" firebase_admin")
		}
		if d.MatchPassword != "" {
			fmt.Printf(" match_password")
		}
		fmt.Println()
		return nil
	}

	if now {
		text, err := pb.Get()
		if err != nil {
			fmt.Fprintf(os.Stderr, "secret import: %v\n", err)
			os.Exit(1)
		}
		if err := pb.Set(""); err != nil {
			fmt.Fprintf(os.Stderr, "secret import: clear clipboard: %v\n", err)
			os.Exit(1)
		}
		if err := absorb(text); err != nil {
			fmt.Fprintf(os.Stderr, "secret import: %v\n", err)
			os.Exit(1)
		}
		return
	}

	fmt.Printf("clipboard cleared — copy a credential (waiting up to %s, Ctrl-C to stop)\n", wait)
	for {
		text, err := ship.AwaitPaste(pb, wait)
		if err == ship.ErrNoPaste {
			fmt.Println("\nnothing was copied.")
			return
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "secret import: %v\n", err)
			os.Exit(1)
		}
		fmt.Println()
		if err := absorb(text); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n\nstill waiting — copy a credential\n", err)
			continue
		}
		fmt.Println("still waiting — copy the next one (Ctrl-C to stop), or Ctrl-C if done")
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

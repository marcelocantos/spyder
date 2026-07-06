// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Command gedharness is the CLI for the ged↔spyder differential harness:
// it records a golden corpus of ged's HTTP responses, diffs a candidate
// corpus against a golden, and prints a side-by-side study of one case.
//
// Usage:
//
//	gedharness record --url http://localhost:42069 --out corpus.json [--fixture tiltbuggy]
//	gedharness diff  <golden.json> <candidate.json>
//	gedharness study <golden.json> <candidate.json> --capability info --label t0
//
// A *populated* corpus needs a live app connected to ged; recording
// against a bare ged captures the empty state — that's expected here.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/marcelocantos/spyder/internal/gedharness"
)

// waypointT0 is the single waypoint label the v1 recorder emits: one
// snapshot at "time zero". A sequence recorder (multiple labels) is a
// later concern.
const waypointT0 = "t0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]

	var err error
	switch cmd {
	case "record":
		err = runRecord(args)
	case "diff":
		err = runDiff(args)
	case "study":
		err = runStudy(args)
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "gedharness: unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "gedharness: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `gedharness — ged↔spyder differential harness

Commands:
  record --url URL --out FILE [--fixture NAME]   record ged into a corpus
  diff   GOLDEN CANDIDATE                         diff two corpora (exit 1 on diffs)
  study  GOLDEN CANDIDATE --capability C --label L   side-by-side one sample
`)
}

func runRecord(args []string) error {
	fs := flag.NewFlagSet("record", flag.ContinueOnError)
	url := fs.String("url", "http://localhost:42069", "ged base URL")
	out := fs.String("out", "corpus.json", "corpus output path")
	fixture := fs.String("fixture", "", "fixture name recorded into the corpus")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx := context.Background()
	client := gedharness.NewClient(*url)
	corpus := &gedharness.Corpus{Fixture: *fixture}

	// info and tweaks are the plain-HTTP capabilities; logs is captured via
	// ged's /mcp `logs` tool (no plain-HTTP route).
	info, err := client.Info(ctx)
	if err != nil {
		return fmt.Errorf("record info: %w", err)
	}
	corpus.Samples = append(corpus.Samples, gedharness.Sample{
		Capability: "info", Label: waypointT0, Response: info,
	})
	fmt.Printf("recorded info @ %s: %s\n", waypointT0, compactLine(info))

	tweaks, err := client.Tweaks(ctx)
	if err != nil {
		return fmt.Errorf("record tweaks: %w", err)
	}
	corpus.Samples = append(corpus.Samples, gedharness.Sample{
		Capability: "tweaks", Label: waypointT0, Response: tweaks,
	})
	fmt.Printf("recorded tweaks @ %s: %s\n", waypointT0, compactLine(tweaks))

	// logs: ged serves them only as an MCP tool (no plain-HTTP route), so
	// capture via the /mcp streamable-HTTP endpoint. A ged with no active
	// session returns "(no log entries)" — a valid, recordable baseline.
	logText, logErr := gedharness.LogsViaMCP(ctx, *url, gedharness.DefaultLogCount)
	if logErr != nil {
		fmt.Printf("logs unavailable (mcp): %v\n", logErr)
	} else {
		logJSON, err := json.Marshal(logText)
		if err != nil {
			return fmt.Errorf("record logs: encode: %w", err)
		}
		corpus.Samples = append(corpus.Samples, gedharness.Sample{
			Capability: "logs", Label: waypointT0, Response: logJSON,
		})
		fmt.Printf("recorded logs @ %s: %s\n", waypointT0, compactLine(logJSON))
	}

	if err := corpus.WriteFile(*out); err != nil {
		return err
	}
	fmt.Printf("wrote %d sample(s) to %s\n", len(corpus.Samples), *out)
	return nil
}

// booleanFlags names the flags that take no value, so permute knows not
// to swallow the following token as a value. All gedharness flags are
// value-taking, so this is currently empty; it exists so permute stays
// correct if a bool flag is ever added.
var booleanFlags = map[string]bool{}

// permute reorders args so all flags (and their values) come before
// positional arguments. Go's flag package stops at the first positional,
// which breaks the natural "diff GOLDEN CANDIDATE --capability info"
// ordering; this restores GNU-style interspersed parsing. Parsing stops
// permuting at a literal "--".
func permute(args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			// A "--name=value" form carries its own value; a bare
			// "--name value" form consumes the next token unless the flag
			// is boolean.
			name := strings.TrimLeft(a, "-")
			if !containsRune(name, '=') && !booleanFlags[name] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, a)
	}
	return append(flags, positional...)
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}

func runDiff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	if err := fs.Parse(permute(args)); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return errors.New("diff needs exactly two corpus paths: GOLDEN CANDIDATE")
	}
	golden, err := gedharness.LoadCorpus(fs.Arg(0))
	if err != nil {
		return err
	}
	candidate, err := gedharness.LoadCorpus(fs.Arg(1))
	if err != nil {
		return err
	}
	rep, err := gedharness.DiffCorpus(golden, candidate, gedharness.DefaultGedRules())
	if err != nil {
		return err
	}
	fmt.Print(rep.String())
	if !rep.Passed {
		os.Exit(1)
	}
	return nil
}

func runStudy(args []string) error {
	fs := flag.NewFlagSet("study", flag.ContinueOnError)
	capability := fs.String("capability", "", "capability of the sample to study")
	label := fs.String("label", "", "label of the sample to study")
	if err := fs.Parse(permute(args)); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return errors.New("study needs exactly two corpus paths: GOLDEN CANDIDATE")
	}
	if *capability == "" || *label == "" {
		return errors.New("study needs --capability and --label")
	}
	golden, err := gedharness.LoadCorpus(fs.Arg(0))
	if err != nil {
		return err
	}
	candidate, err := gedharness.LoadCorpus(fs.Arg(1))
	if err != nil {
		return err
	}
	g, ok := findSample(golden, *capability, *label)
	if !ok {
		return fmt.Errorf("golden has no sample %s/%s", *capability, *label)
	}
	c, ok := findSample(candidate, *capability, *label)
	if !ok {
		return fmt.Errorf("candidate has no sample %s/%s", *capability, *label)
	}
	fmt.Print(gedharness.Study(g.Response, c.Response, gedharness.DefaultGedRules()))
	return nil
}

func findSample(c *gedharness.Corpus, capability, label string) (gedharness.Sample, bool) {
	for _, s := range c.Samples {
		if s.Capability == capability && s.Label == label {
			return s, true
		}
	}
	return gedharness.Sample{}, false
}

// compactLine renders raw JSON on one line for record's progress output.
func compactLine(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(b)
}

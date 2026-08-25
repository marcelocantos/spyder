// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package ship

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// FastlaneOpts configures a spyder fastlane invocation (🎯T133.4/T133.5).
type FastlaneOpts struct {
	Studio  string
	Args    []string // fastlane argv after spyder flags
	Confirm bool
	DryRun  bool
	Cwd     string
	// LookPath resolves "bundle" (injectable for tests).
	LookPath func(file string) (string, error)
	// Environ is the parent process env (default os.Environ).
	Environ []string
}

// FastlanePlan is the resolved exec plan (also used for --dry-run output).
type FastlanePlan struct {
	Studio       string
	Class        LaneClass
	Action       string
	Argv         []string // bundle exec fastlane …
	EnvKeys      []string // sorted keys injected for the child (no values)
	TempFiles    []string // paths that must be wiped
	ChildEnv     []string // full child environ (secrets included) — never log
	PassEnv      map[string]string
	Present      map[string]bool
	Fingerprints map[string]string
	Cwd          string
}

// passthroughEnvKeys are copied from the parent into the child.
var passthroughEnvKeys = []string{
	"PATH", "HOME", "TMPDIR", "USER", "LOGNAME", "SHELL", "TERM",
	"LANG", "LC_ALL", "LC_CTYPE",
	"APP_ID", "SHIP_SCHEME", "STUDIO",
	"BUNDLE_GEMFILE", "BUNDLE_PATH", "GEM_HOME", "GEM_PATH",
	"FASTLANE_OPT_OUT_USAGE", "FASTLANE_SKIP_UPDATE_CHECK",
	"FASTLANE_HIDE_CHANGELOG", "DELIVER_ITMSTRANSPORTER_ADDITIONAL_UPLOAD_PARAMETERS",
}

// PrepareFastlane loads secrets, writes 0600 temps, builds child env.
// Caller must WipeTemps after the child exits (including on signal).
func PrepareFastlane(opts FastlaneOpts) (*FastlanePlan, error) {
	studio, err := NormalizeStudio(opts.Studio)
	if err != nil {
		return nil, err
	}
	cwd := opts.Cwd
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	if err := CheckStudioMatchfile(studio, cwd); err != nil {
		return nil, err
	}
	env, err := LoadStudio(studio)
	if err != nil {
		return nil, err
	}
	return prepareFastlaneFromEnv(studio, env, opts, cwd)
}

func prepareFastlaneFromEnv(studio string, env *Envelope, opts FastlaneOpts, cwd string) (*FastlanePlan, error) {
	class, action, err := ClassifyLane(opts.Args)
	if err != nil {
		return nil, err
	}
	if class.NeedsConfirm() && !opts.Confirm && !opts.DryRun {
		return nil, fmt.Errorf("fastlane %s is class %s — pass --confirm (or --dry-run to rehearse)",
			action, class)
	}

	look := opts.LookPath
	if look == nil {
		look = exec.LookPath
	}
	bundlePath, err := look("bundle")
	if err != nil {
		return nil, fmt.Errorf("bundle not on PATH (run from a consumer with bundler): %w", err)
	}

	parentEnv := opts.Environ
	if parentEnv == nil {
		parentEnv = os.Environ()
	}
	// Refuse if the parent already exports MATCH_PASSWORD — that defeats
	// the front-door model (secrets must not live in Make's environment).
	for _, kv := range parentEnv {
		if strings.HasPrefix(kv, "MATCH_PASSWORD=") && kv != "MATCH_PASSWORD=" {
			return nil, fmt.Errorf("parent env has MATCH_PASSWORD set — unset it; spyder injects it into the fastlane child only")
		}
	}

	tmpdir, err := os.MkdirTemp("", "spyder-ship-*")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(tmpdir, 0o700); err != nil {
		_ = os.RemoveAll(tmpdir)
		return nil, err
	}

	st := env.RedactedStatus(studio)
	plan := &FastlanePlan{
		Studio:       studio,
		Class:        class,
		Action:       action,
		Argv:         append([]string{bundlePath, "exec", "fastlane"}, opts.Args...),
		PassEnv:      map[string]string{},
		Present:      st.Present,
		Fingerprints: st.Fingerprints,
		Cwd:          cwd,
	}
	plan.TempFiles = append(plan.TempFiles, tmpdir)

	child := map[string]string{}
	for _, k := range passthroughEnvKeys {
		if v, ok := lookupEnv(parentEnv, k); ok {
			child[k] = v
			plan.PassEnv[k] = "(passthrough)"
		}
	}
	child["STUDIO"] = studio
	plan.PassEnv["STUDIO"] = studio

	if env.MatchPassword != "" {
		child["MATCH_PASSWORD"] = env.MatchPassword
		plan.EnvKeys = append(plan.EnvKeys, "MATCH_PASSWORD")
	}
	if env.ASC != nil && env.ASC.P8 != "" {
		p8path := filepath.Join(tmpdir, "AuthKey_"+env.ASC.KeyID+".p8")
		if err := os.WriteFile(p8path, []byte(env.ASC.P8), 0o600); err != nil {
			WipeTemps(plan.TempFiles)
			return nil, err
		}
		plan.TempFiles = append(plan.TempFiles, p8path)
		child["APP_STORE_CONNECT_API_KEY_KEY_ID"] = env.ASC.KeyID
		child["APP_STORE_CONNECT_API_KEY_ISSUER_ID"] = env.ASC.IssuerID
		child["APP_STORE_CONNECT_API_KEY_KEY_PATH"] = p8path
		plan.EnvKeys = append(plan.EnvKeys,
			"APP_STORE_CONNECT_API_KEY_KEY_ID",
			"APP_STORE_CONNECT_API_KEY_ISSUER_ID",
			"APP_STORE_CONNECT_API_KEY_KEY_PATH")
	}
	if env.PlayUpload != nil && len(env.PlayUpload.Keystore) > 0 {
		ext := "p12"
		if env.PlayUpload.Format == "jks" {
			ext = "jks"
		}
		ks := filepath.Join(tmpdir, "play-upload."+ext)
		if err := os.WriteFile(ks, env.PlayUpload.Keystore, 0o600); err != nil {
			WipeTemps(plan.TempFiles)
			return nil, err
		}
		plan.TempFiles = append(plan.TempFiles, ks)
		child["ANDROID_KEYSTORE_PATH"] = ks
		child["ANDROID_KEYSTORE_PASSWORD"] = env.PlayUpload.Password
		if env.PlayUpload.Alias != "" {
			child["ANDROID_KEY_ALIAS"] = env.PlayUpload.Alias
		}
		plan.EnvKeys = append(plan.EnvKeys, "ANDROID_KEYSTORE_PATH", "ANDROID_KEYSTORE_PASSWORD")
		if env.PlayUpload.Alias != "" {
			plan.EnvKeys = append(plan.EnvKeys, "ANDROID_KEY_ALIAS")
		}
	}
	if len(env.PlayServiceAccount) > 0 {
		sa := filepath.Join(tmpdir, "play-sa.json")
		if err := os.WriteFile(sa, env.PlayServiceAccount, 0o600); err != nil {
			WipeTemps(plan.TempFiles)
			return nil, err
		}
		plan.TempFiles = append(plan.TempFiles, sa)
		child["SUPPLY_JSON_KEY"] = sa
		child["GOOGLE_APPLICATION_CREDENTIALS"] = sa
		plan.EnvKeys = append(plan.EnvKeys, "SUPPLY_JSON_KEY", "GOOGLE_APPLICATION_CREDENTIALS")
	}

	sort.Strings(plan.EnvKeys)
	plan.ChildEnv = flattenEnv(child)
	return plan, nil
}

// WipeTemps removes temp files/dirs created for a fastlane run.
func WipeTemps(paths []string) {
	// Remove files first, then dirs (paths may list both).
	for i := len(paths) - 1; i >= 0; i-- {
		_ = os.RemoveAll(paths[i])
	}
}

// RunFastlane executes the plan (or prints dry-run). Returns child exit code.
// Non-dry-run test_publish / prod_publish always leave a redacted audit
// record and reflection stub under ~/.spyder/ship-audit/ (🎯T133.6).
func RunFastlane(opts FastlaneOpts) (int, error) {
	plan, err := PrepareFastlane(opts)
	if err != nil {
		return 2, err
	}
	defer WipeTemps(plan.TempFiles)
	return executeFastlanePlan(plan, opts)
}

func executeFastlanePlan(plan *FastlanePlan, opts FastlaneOpts) (int, error) {
	start := time.Now()
	cwd := plan.Cwd
	if cwd == "" {
		cwd = opts.Cwd
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	runID := newRunID()
	gitSHA, gitDirty := GitMeta(cwd)

	if opts.DryRun {
		fmt.Printf("dry-run: studio=%s class=%s action=%s\n", plan.Studio, plan.Class, plan.Action)
		fmt.Printf("argv: %s\n", strings.Join(plan.Argv, " "))
		fmt.Printf("env_keys: %s\n", strings.Join(plan.EnvKeys, ","))
		fmt.Printf("temps: %d paths under wipe-on-exit\n", len(plan.TempFiles))
		_ = WriteAudit(&AuditRecord{
			ID:             runID,
			Studio:         plan.Studio,
			Class:          plan.Class,
			Action:         plan.Action,
			Argv:           plan.Argv,
			Cwd:            cwd,
			GitSHA:         gitSHA,
			GitDirty:       gitDirty,
			SecretsPresent: plan.Present,
			Fingerprints:   plan.Fingerprints,
			ExitCode:       0,
			DurationMS:     time.Since(start).Milliseconds(),
			DryRun:         true,
			Confirm:        opts.Confirm,
			CDHash:         CDHashSelf(),
		})
		return 0, nil
	}

	cmd := exec.Command(plan.Argv[0], plan.Argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = plan.ChildEnv
	var buf bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &buf)
	cmd.Stderr = io.MultiWriter(os.Stderr, &buf)
	cmd.Stdin = os.Stdin

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigCh)
	go func() {
		<-sigCh
		WipeTemps(plan.TempFiles)
	}()

	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
			err = nil
		} else {
			code = 1
		}
	}

	logPath, _ := WriteRunLog(runID, buf.String())
	_ = WriteAudit(&AuditRecord{
		ID:             runID,
		Studio:         plan.Studio,
		Class:          plan.Class,
		Action:         plan.Action,
		Argv:           plan.Argv,
		Cwd:            cwd,
		GitSHA:         gitSHA,
		GitDirty:       gitDirty,
		SecretsPresent: plan.Present,
		Fingerprints:   plan.Fingerprints,
		ExitCode:       code,
		DurationMS:     time.Since(start).Milliseconds(),
		DryRun:         false,
		Confirm:        opts.Confirm,
		LogPath:        logPath,
		CDHash:         CDHashSelf(),
	})
	return code, err
}

func lookupEnv(environ []string, key string) (string, bool) {
	prefix := key + "="
	for _, kv := range environ {
		if strings.HasPrefix(kv, prefix) {
			return kv[len(prefix):], true
		}
	}
	return "", false
}

func flattenEnv(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+m[k])
	}
	return out
}

// FormatDryRun is kept for tests that assert redaction (no secret values).
func (p *FastlanePlan) RedactedSummary() string {
	return fmt.Sprintf("studio=%s class=%s argv=%q env_keys=%v",
		p.Studio, p.Class, strings.Join(p.Argv, " "), p.EnvKeys)
}

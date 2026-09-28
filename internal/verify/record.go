// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Pass records live at <cwd>/verify-runs/resume/<workflow>.json unless
// SPYDER_VERIFY_RESUME_DIR is set. A pass keeps the file; delete it to
// rerun every step.
const resumeEnv = "SPYDER_VERIFY_RESUME_DIR"

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Record is the persistent pass file for one workflow in one cwd.
type Record struct {
	Workflow         string            `json:"workflow"`
	Passed           []string          `json:"passed"`
	Deferred         []string          `json:"deferred,omitempty"`
	DefinitionSHA256 string            `json:"definition_sha256,omitempty"`
	FailedStepID     string            `json:"failed_step_id,omitempty"`
	Params           map[string]string `json:"params,omitempty"`
	// Appraisals are model verdicts on deferred static gates, carried to the
	// owner review so it can confirm or override each one.
	Appraisals map[string]*Appraisal `json:"appraisals,omitempty"`
}

// ResumeDir is the directory that holds pass records for cwd.
func ResumeDir(cwd string) string {
	if v := strings.TrimSpace(os.Getenv(resumeEnv)); v != "" {
		return v
	}
	return filepath.Join(cwd, "verify-runs", "resume")
}

// ResumePath is the pass-record file for a workflow name.
func ResumePath(cwd, workflowName string) string {
	name := unsafeName.ReplaceAllString(workflowName, "-") + ".json"
	return filepath.Join(ResumeDir(cwd), name)
}

// LoadRecord reads a pass record, or nil if none exists.
func LoadRecord(cwd, workflowName string) *Record {
	path := ResumePath(cwd, workflowName)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var rec Record
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil
	}
	return &rec
}

// SaveRecord writes the pass record, creating the resume directory.
func SaveRecord(cwd, workflowName string, rec *Record) error {
	path := ResumePath(cwd, workflowName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if rec.Workflow == "" {
		rec.Workflow = workflowName
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// RerunIDs are the failed step plus any spyder_script it requires, so a
// retry restages the device before asking the owner again.
func RerunIDs(steps []Step, failedStepID string) map[string]bool {
	out := map[string]bool{}
	if failedStepID == "" {
		return out
	}
	out[failedStepID] = true
	byID := map[string]Step{}
	for _, s := range steps {
		byID[s.ID] = s
	}
	failed, ok := byID[failedStepID]
	if !ok {
		return out
	}
	for _, req := range failed.Requires {
		if step, ok := byID[req]; ok && step.Type == KindSpyderScript {
			out[req] = true
		}
	}
	return out
}

// ResumeRerunIDs invalidates a step marked always_run and every step that
// depends on it. This prevents a prior owner judgment from being reused after
// a deployment is repeated with a new build.
func ResumeRerunIDs(steps []Step, failedStepID string) map[string]bool {
	out := RerunIDs(steps, failedStepID)
	for _, step := range steps {
		if step.AlwaysRun {
			out[step.ID] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, step := range steps {
			if out[step.ID] {
				continue
			}
			for _, req := range step.Requires {
				if out[req] {
					out[step.ID] = true
					changed = true
					break
				}
			}
		}
	}
	return out
}

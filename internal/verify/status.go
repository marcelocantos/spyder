// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"fmt"
	"strings"
)

const (
	StatusPassed      = "passed"
	StatusInvestigate = "investigate"
	StatusFailed      = "failed"
	StatusAborted     = "aborted"
	StatusPrepared    = "prepared"

	StepOK       = "ok"
	StepFailed   = "failed"
	StepSkipped  = "skipped"
	StepRunning  = "running"
	StepPending  = "pending"
	StepWaiting  = "waiting_human"
	StepAborted  = "aborted"
	StepDeferred = "deferred"
)

const (
	ExitPassed      = 0
	ExitError       = 1
	ExitInvestigate = 2
	ExitAborted     = 3
	ExitPrepared    = 4
)

// StepRecord is one executed (or skipped) step in the STATUS block.
type StepRecord struct {
	StepID     string `json:"step_id"`
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	ChoiceID   string `json:"choice_id,omitempty"`
	Comment    string `json:"comment,omitempty"`
	Type       string `json:"type,omitempty"`
	Label      string `json:"label,omitempty"`
}

// StatusInput is everything FormatStatus needs.
type StatusInput struct {
	Status       string
	Workflow     string
	Device       string
	FailedStepID string
	FailedReason string
	SkipN        int
	Steps        []StepRecord
	OwnerComment string
	ReportDir    string
}

// FormatStatus builds the closing stdio block. Exit 0 plus this block is
// the result; a passed run is passed.
func FormatStatus(in StatusInput) string {
	ok, failed, deferred := 0, 0, 0
	for _, rec := range in.Steps {
		switch rec.Status {
		case StepOK:
			ok++
		case StepFailed:
			failed++
		case StepDeferred:
			deferred++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "======== STATUS %s ========\n", in.Status)
	fmt.Fprintf(&b, "workflow: %s\n", in.Workflow)
	if in.Device != "" {
		fmt.Fprintf(&b, "device: %s\n", in.Device)
	}
	if in.FailedStepID != "" {
		fmt.Fprintf(&b, "failed_step: %s\n", in.FailedStepID)
	}
	if in.FailedReason != "" {
		fmt.Fprintf(&b, "reason: %s\n", in.FailedReason)
	}
	fmt.Fprintf(&b, "ok: %d  skipped: %d  deferred: %d  failed: %d\n", ok, in.SkipN, deferred, failed)
	if len(in.Steps) > 0 {
		b.WriteString("steps:\n")
		for _, rec := range in.Steps {
			bits := []string{rec.StepID, rec.Status}
			if rec.DurationMS != 0 || rec.Status == StepOK || rec.Status == StepFailed || rec.Status == StepSkipped {
				bits = append(bits, fmt.Sprintf("%dms", rec.DurationMS))
			}
			if rec.ChoiceID != "" {
				bits = append(bits, "choice="+rec.ChoiceID)
			}
			if strings.TrimSpace(rec.Comment) != "" {
				bits = append(bits, "comment="+oneLine(rec.Comment))
			}
			fmt.Fprintf(&b, "  %s\n", strings.Join(bits, "  "))
		}
	}
	if c := strings.TrimSpace(in.OwnerComment); c != "" {
		fmt.Fprintf(&b, "owner_comment: %s\n", oneLine(c))
	}
	if in.ReportDir != "" {
		fmt.Fprintf(&b, "report: %s\n", in.ReportDir)
	}
	b.WriteString("======== END STATUS ========")
	return b.String()
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

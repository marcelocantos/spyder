// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package verify is spyder's product-neutral verification workflow engine
// (🎯T138). A workflow YAML names steps; this package schedules them as a
// leaf-level frontier on a daemon-wide resource pool.
package verify

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/marcelocantos/claudia"
	"gopkg.in/yaml.v3"
)

// Step kinds the engine knows. Device names, deploys, prompts, and
// scripts live only in the workflow file.
const (
	KindShell        = "shell"
	KindSpyderScript = "spyder_script"
	KindModel        = "model"
	KindHumanGate    = "human_gate"
)

// A human_gate's judgment is static when a screenshot of the device is
// enough to decide it, so an unattended run may ask a model for a first-cut
// verdict. A dynamic judgment needs the owner to play or watch motion.
const (
	JudgmentStatic  = "static"
	JudgmentDynamic = "dynamic"
)

// defaultAppraiseModel selects a Claude task model for a static gate that
// names no model. Screen appraisal needs Claude's image-reading tool.
const defaultAppraiseModel = `{"mode":"task","purpose":"analysis","quality":"standard","prefer_provider":"claude","exclude_providers":["grok","codex","cursor","bedrock","ollama"]}`

const (
	OutcomeContinue    = "continue"
	OutcomeInvestigate = "investigate"
	OutcomeWaive       = "waive"
)

var (
	stepKinds = map[string]bool{
		KindShell:        true,
		KindSpyderScript: true,
		KindModel:        true,
		KindHumanGate:    true,
	}
	outcomes = map[string]bool{
		OutcomeContinue:    true,
		OutcomeInvestigate: true,
		OutcomeWaive:       true,
	}
	paramRE = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
)

// WorkflowError is one or more validation problems.
type WorkflowError struct {
	Errors []string
}

func (e *WorkflowError) Error() string {
	return strings.Join(e.Errors, "\n")
}

// Workflow is a loaded, validated verification graph.
type Workflow struct {
	Name              string
	AgentPrompt       string
	SuggestedCommands []string
	Params            map[string]string
	Groups            []Group
	Steps             []Step
	Cleanup           []Step
	IdleTimeoutSec    float64
	// ScreenModel is the Claudia model predicates for preconditions that
	// name none (defaults.screen_model).
	ScreenModel json.RawMessage
	Raw         []byte
}

const (
	defaultIdleTimeoutSec    = 300
	maximumIdleTimeoutSec    = 86400
	defaultCleanupTimeoutSec = 30
)

// Group is dashboard collapse only — it does not schedule.
type Group struct {
	ID     string `json:"id"`
	Label  string `json:"label,omitempty"`
	Parent string `json:"parent,omitempty"`
	// Number is the dotted outline position, set only in run views.
	Number string `json:"number,omitempty"`
}

// Step is one leaf in the DAG.
type Step struct {
	ID            string
	Type          string
	Label         string
	Requires      []string
	Next          string
	Group         string
	Mutex         string
	Device        string
	Env           map[string]string
	TimeoutSec    *float64
	Retry         *Retry
	OnFail        string
	Command       string
	Argv          []string
	Expect        map[string]any
	Script        string
	ModelSpec     json.RawMessage
	CaptureScreen bool
	Accept        string
	Params        map[string]string
	Prompt        string
	Hint          string
	Choices       []Choice
	AllowComment  bool
	AlwaysRun     bool
	ReviewReplay  bool
	// human_gate only.
	Judgment           string
	AppraiseModel      json.RawMessage
	AppraisePrompt     string
	AppraiseTimeoutSec *float64
	Precondition       *Precondition
	// ScreenPNG makes a model step read this saved screenshot instead of
	// capturing the device (set by the engine, not by workflows).
	ScreenPNG string
}

// Precondition is a best-effort model check that the device shows the
// expected starting screen before an owner gate. It is front matter to the
// gate, not a reviewable step, and it never blocks: whatever the model says,
// the gate proceeds, carrying the verdict as a note (a warning when not met).
type Precondition struct {
	Screen     string
	Model      json.RawMessage
	Mutex      string
	TimeoutSec float64
	Retry      Retry
}

const (
	defaultPreconditionTimeoutSec = 90
	defaultPreconditionRetries    = 2
	defaultPreconditionBackoffSec = 2
)

// Retry is optional command-step retry.
type Retry struct {
	Count      int
	BackoffSec float64
}

// Choice is one human_gate answer.
type Choice struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Outcome string `json:"outcome,omitempty"`
	Comment string `json:"comment,omitempty"` // "required" or empty
	Next    string `json:"next,omitempty"`
}

// LoadFile reads and validates a workflow YAML file.
func LoadFile(path string) (*Workflow, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Load(raw)
}

// Load parses and validates workflow YAML.
func Load(raw []byte) (*Workflow, error) {
	var data any
	if err := yaml.Unmarshal(raw, &data); err != nil {
		return nil, &WorkflowError{Errors: []string{err.Error()}}
	}
	m, ok := data.(map[string]any)
	if !ok {
		return nil, &WorkflowError{Errors: []string{"workflow must be a mapping"}}
	}
	wf, errs := parseWorkflow(m)
	if len(errs) > 0 {
		return nil, &WorkflowError{Errors: errs}
	}
	wf.Raw = append([]byte(nil), raw...)
	return wf, nil
}

func parseWorkflow(m map[string]any) (*Workflow, []string) {
	var errs []string
	wf := &Workflow{Params: map[string]string{}, IdleTimeoutSec: defaultIdleTimeoutSec}

	name, ok := asString(m["name"])
	if !ok || strings.TrimSpace(name) == "" {
		errs = append(errs, "name is required")
	} else {
		wf.Name = name
	}

	if v, exists := m["agent_prompt"]; exists && v != nil {
		s, ok := asString(v)
		if !ok {
			errs = append(errs, "agent_prompt must be a string")
		} else {
			wf.AgentPrompt = s
		}
	}

	if v, exists := m["suggested_commands"]; exists && v != nil {
		list, ok := asStringList(v)
		if !ok {
			errs = append(errs, "suggested_commands must be a list of strings")
		} else {
			wf.SuggestedCommands = list
		}
	}

	if v, exists := m["params"]; exists && v != nil {
		pm, ok := v.(map[string]any)
		if !ok {
			errs = append(errs, "params must be a mapping")
		} else {
			for k, val := range pm {
				wf.Params[k] = stringify(val)
			}
		}
	}
	if v, exists := m["idle_timeout_sec"]; exists && v != nil {
		n, ok := asNumber(v)
		if !ok || n <= 0 || n > maximumIdleTimeoutSec || math.IsNaN(n) || math.IsInf(n, 0) {
			errs = append(errs, "idle_timeout_sec must be a positive number no greater than 86400")
		} else {
			wf.IdleTimeoutSec = n
		}
	}

	if v, exists := m["defaults"]; exists && v != nil {
		dm, ok := v.(map[string]any)
		if !ok {
			errs = append(errs, "defaults must be a mapping")
		} else {
			for key, val := range dm {
				switch key {
				case "screen_model":
					raw, modelErrs := parseModelPredicates("defaults.screen_model", val)
					errs = append(errs, modelErrs...)
					wf.ScreenModel = raw
				default:
					errs = append(errs, "unknown defaults field "+key)
				}
			}
		}
	}

	groupIDs, groups, gErrs := parseGroups(m["groups"])
	errs = append(errs, gErrs...)
	wf.Groups = groups

	rawSteps, ok := m["steps"].([]any)
	if !ok || len(rawSteps) == 0 {
		errs = append(errs, "steps must be a non-empty list")
		return wf, errs
	}

	ids := map[string]bool{}
	for i, item := range rawSteps {
		sm, ok := item.(map[string]any)
		if !ok {
			errs = append(errs, fmt.Sprintf("steps[%d] must be a mapping", i))
			continue
		}
		step, sErrs := parseStep(i, sm)
		errs = append(errs, sErrs...)
		if step.ID == "" {
			continue
		}
		if ids[step.ID] {
			errs = append(errs, "duplicate step id "+step.ID)
		}
		ids[step.ID] = true
		wf.Steps = append(wf.Steps, step)
	}
	graphIDs := make(map[string]bool, len(ids))
	for id := range ids {
		graphIDs[id] = true
	}
	if rawCleanup, exists := m["cleanup"]; exists && rawCleanup != nil {
		list, ok := rawCleanup.([]any)
		if !ok || len(list) == 0 {
			errs = append(errs, "cleanup must be a non-empty list")
		} else {
			for i, item := range list {
				sm, ok := item.(map[string]any)
				if !ok {
					errs = append(errs, fmt.Sprintf("cleanup[%d] must be a mapping", i))
					continue
				}
				step, stepErrs := parseStep(i, sm)
				errs = append(errs, stepErrs...)
				if step.ID == "" {
					continue
				}
				if ids[step.ID] {
					errs = append(errs, "duplicate step id "+step.ID)
				}
				ids[step.ID] = true
				if step.Type != KindShell || len(step.Requires) > 0 || step.Next != "" || step.Retry != nil || step.AlwaysRun {
					errs = append(errs, step.ID+": cleanup must be a shell command without graph dependencies or retry")
				}
				if step.TimeoutSec == nil {
					seconds := float64(defaultCleanupTimeoutSec)
					step.TimeoutSec = &seconds
				} else if *step.TimeoutSec <= 0 {
					errs = append(errs, step.ID+": cleanup timeout_sec must be positive")
				}
				wf.Cleanup = append(wf.Cleanup, step)
			}
		}
	}

	for _, step := range wf.Steps {
		for _, req := range step.Requires {
			if !graphIDs[req] {
				errs = append(errs, step.ID+": requires unknown step "+req)
			}
		}
		if step.Next != "" && !graphIDs[step.Next] {
			errs = append(errs, step.ID+": next unknown step "+step.Next)
		}
		if step.Group != "" {
			if groupIDs == nil || !groupIDs[step.Group] {
				errs = append(errs, step.ID+": unknown group "+step.Group)
			}
		}
		if step.Type != KindHumanGate {
			continue
		}
		for _, c := range step.Choices {
			if c.Next != "" && !graphIDs[c.Next] {
				errs = append(errs, fmt.Sprintf("%s: choice %s next unknown step %s", step.ID, c.ID, c.Next))
			}
		}
	}
	errs = append(errs, requiresCycleErrors(wf.Steps)...)
	return wf, errs
}

func parseGroups(raw any) (map[string]bool, []Group, []string) {
	if raw == nil {
		return nil, nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return map[string]bool{}, nil, []string{"groups must be a list"}
	}
	var errs []string
	var groups []Group
	ids := map[string]bool{}
	parents := map[string]string{}
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			errs = append(errs, fmt.Sprintf("groups[%d] must be a mapping", i))
			continue
		}
		id, ok := asString(m["id"])
		if !ok || strings.TrimSpace(id) == "" {
			errs = append(errs, fmt.Sprintf("groups[%d] missing id", i))
			continue
		}
		if ids[id] {
			errs = append(errs, "duplicate group id "+id)
		}
		ids[id] = true
		g := Group{ID: id}
		if v := m["label"]; v != nil {
			s, ok := asString(v)
			if !ok {
				errs = append(errs, id+": label must be a string")
			} else {
				g.Label = s
			}
		}
		if v := m["parent"]; v != nil {
			p, ok := asString(v)
			if !ok || strings.TrimSpace(p) == "" {
				errs = append(errs, id+": parent must be a string")
			} else {
				g.Parent = p
				parents[id] = p
			}
		}
		groups = append(groups, g)
	}
	for id, parent := range parents {
		if !ids[parent] {
			errs = append(errs, id+": unknown parent "+parent)
		} else if parent == id {
			errs = append(errs, id+": group cannot parent itself")
		}
	}
	for _, g := range groups {
		seen := map[string]bool{}
		cursor := g.ID
		for {
			parent, ok := parents[cursor]
			if !ok {
				break
			}
			if seen[cursor] {
				errs = append(errs, g.ID+": group parent cycle")
				break
			}
			seen[cursor] = true
			cursor = parent
		}
	}
	return ids, groups, errs
}

func parseStep(index int, m map[string]any) (Step, []string) {
	var errs []string
	id, ok := asString(m["id"])
	if !ok || strings.TrimSpace(id) == "" {
		return Step{}, []string{fmt.Sprintf("steps[%d] missing id", index)}
	}
	step := Step{ID: id, OnFail: "investigate"}
	kind, _ := asString(m["type"])
	if !stepKinds[kind] {
		return step, []string{fmt.Sprintf("%s: unknown type %q", id, kind)}
	}
	step.Type = kind

	if v := m["label"]; v != nil {
		s, ok := asString(v)
		if !ok {
			errs = append(errs, id+": label must be a string")
		} else {
			step.Label = s
		}
	}
	if v := m["requires"]; v != nil {
		list, ok := asStringList(v)
		if !ok {
			errs = append(errs, id+": requires must be a list of step ids")
		} else {
			step.Requires = list
		}
	}
	if v := m["always_run"]; v != nil {
		always, ok := v.(bool)
		if !ok {
			errs = append(errs, id+": always_run must be a boolean")
		} else {
			step.AlwaysRun = always
		}
	}
	if v := m["review_replay"]; v != nil {
		replay, ok := v.(bool)
		if !ok {
			errs = append(errs, id+": review_replay must be a boolean")
		} else {
			step.ReviewReplay = replay
		}
	}
	if v := m["next"]; v != nil {
		s, ok := asString(v)
		if ok {
			step.Next = s
		}
	}
	if v := m["group"]; v != nil {
		s, ok := asString(v)
		if !ok || strings.TrimSpace(s) == "" {
			errs = append(errs, id+": group must be a string")
		} else {
			step.Group = s
		}
	}
	if v := m["mutex"]; v != nil {
		s, ok := asString(v)
		if !ok || strings.TrimSpace(s) == "" {
			errs = append(errs, id+": mutex must be a string")
		} else {
			step.Mutex = s
		}
	}
	if v := m["device"]; v != nil {
		s, ok := asString(v)
		if !ok {
			errs = append(errs, id+": device must be a string")
		} else {
			step.Device = s
		}
	}
	if v := m["env"]; v != nil {
		em, ok := v.(map[string]any)
		if !ok {
			errs = append(errs, id+": env must be a mapping")
		} else {
			step.Env = map[string]string{}
			for k, val := range em {
				step.Env[k] = stringify(val)
			}
		}
	}
	if v := m["expect"]; v != nil {
		em, ok := v.(map[string]any)
		if !ok {
			errs = append(errs, id+": expect must be a mapping")
		} else {
			step.Expect = em
		}
	}
	if v := m["timeout_sec"]; v != nil {
		n, ok := asNumber(v)
		if !ok || n < 0 {
			errs = append(errs, id+": timeout_sec must be a non-negative number")
		} else {
			step.TimeoutSec = &n
		}
	}
	if v := m["on_fail"]; v != nil {
		s, _ := asString(v)
		if s != "investigate" {
			errs = append(errs, id+": on_fail must be investigate")
		}
	}
	if v := m["retry"]; v != nil {
		if kind == KindHumanGate {
			errs = append(errs, id+": human_gate does not retry")
		} else {
			rm, ok := v.(map[string]any)
			if !ok {
				errs = append(errs, id+": retry must be a mapping")
			} else {
				r := &Retry{}
				if c, exists := rm["count"]; exists {
					n, ok := asInt(c)
					if !ok || n < 0 {
						errs = append(errs, id+": retry.count must be a non-negative integer")
					} else {
						r.Count = n
					}
				}
				if b, exists := rm["backoff_sec"]; exists {
					n, ok := asNumber(b)
					if !ok || n < 0 {
						errs = append(errs, id+": retry.backoff_sec must be a non-negative number")
					} else {
						r.BackoffSec = n
					}
				}
				step.Retry = r
			}
		}
	}

	switch kind {
	case KindShell:
		errs = append(errs, validateShell(id, m, &step)...)
	case KindSpyderScript:
		script, ok := asString(m["script"])
		if !ok || strings.TrimSpace(script) == "" {
			errs = append(errs, id+": spyder_script requires script")
		} else {
			step.Script = script
		}
		if v := m["params"]; v != nil {
			pm, ok := v.(map[string]any)
			if !ok {
				errs = append(errs, id+": params must be a mapping")
			} else {
				step.Params = map[string]string{}
				for k, val := range pm {
					step.Params[k] = stringify(val)
				}
			}
		}
	case KindModel:
		prompt, ok := asString(m["prompt"])
		if !ok || strings.TrimSpace(prompt) == "" {
			errs = append(errs, id+": model requires prompt")
		} else {
			step.Prompt = prompt
		}
		raw, modelErrs := parseModelPredicates(id, m["model"])
		errs = append(errs, modelErrs...)
		step.ModelSpec = raw
		if v, exists := m["capture_screen"]; exists {
			b, ok := v.(bool)
			if !ok {
				errs = append(errs, id+": capture_screen must be a boolean")
			} else {
				step.CaptureScreen = b
			}
		}
		if step.CaptureScreen && step.Device == "" {
			errs = append(errs, id+": capture_screen requires device")
		}
		if v, exists := m["accept"]; exists {
			s, ok := asString(v)
			if !ok || strings.TrimSpace(s) == "" {
				errs = append(errs, id+": accept must be a non-empty string")
			} else {
				step.Accept = s
			}
		}
	case KindHumanGate:
		errs = append(errs, validateGate(id, m, &step)...)
	}
	return step, errs
}

// parseModelPredicates checks a Claudia model predicates mapping and returns
// it as wire JSON.
func parseModelPredicates(id string, v any) (json.RawMessage, []string) {
	model, ok := v.(map[string]any)
	if !ok {
		return nil, []string{id + ": model requires a Claudia model predicates mapping"}
	}
	raw, err := json.Marshal(model)
	if err != nil {
		return nil, []string{id + ": invalid model predicates: " + err.Error()}
	}
	var errs []string
	allowed := map[string]bool{"mode": true, "purpose": true, "skill": true, "quality": true, "model": true, "effort": true, "prefer_plan": true, "background": true, "prefer_provider": true, "exclude_providers": true, "require_usage": true, "thresholds": true}
	for key := range model {
		if !allowed[key] {
			errs = append(errs, id+": unknown Claudia model predicate "+key)
		}
	}
	if strings.Contains(string(raw), "${") {
		errs = append(errs, id+": model predicates cannot contain workflow parameters")
	}
	pred, decodeErr := claudia.DecodePredicatesWire(raw)
	if decodeErr != nil {
		errs = append(errs, id+": invalid Claudia model predicates: "+decodeErr.Error())
	} else if pred.Mode != "" && pred.Mode != claudia.CapabilityTask {
		errs = append(errs, id+": model mode must be task")
	}
	return raw, errs
}

func parsePrecondition(id string, v any) (*Precondition, []string) {
	pm, ok := v.(map[string]any)
	if !ok {
		return nil, []string{id + ": precondition must be a mapping"}
	}
	p := &Precondition{
		TimeoutSec: defaultPreconditionTimeoutSec,
		Retry:      Retry{Count: defaultPreconditionRetries, BackoffSec: defaultPreconditionBackoffSec},
	}
	var errs []string
	for key, val := range pm {
		switch key {
		case "screen":
			s, ok := asString(val)
			if !ok || strings.TrimSpace(s) == "" {
				errs = append(errs, id+": precondition.screen must describe the expected screen")
			} else {
				p.Screen = s
			}
		case "model":
			raw, modelErrs := parseModelPredicates(id+".precondition", val)
			errs = append(errs, modelErrs...)
			p.Model = raw
		case "mutex":
			s, ok := asString(val)
			if !ok || strings.TrimSpace(s) == "" {
				errs = append(errs, id+": precondition.mutex must be a string")
			} else {
				p.Mutex = s
			}
		case "timeout_sec":
			n, ok := asNumber(val)
			if !ok || n <= 0 {
				errs = append(errs, id+": precondition.timeout_sec must be a positive number")
			} else {
				p.TimeoutSec = n
			}
		case "retry":
			rm, ok := val.(map[string]any)
			if !ok {
				errs = append(errs, id+": precondition.retry must be a mapping")
				continue
			}
			if c, has := rm["count"]; has {
				n, ok := asInt(c)
				if !ok || n < 0 {
					errs = append(errs, id+": precondition.retry.count must be a non-negative integer")
				} else {
					p.Retry.Count = n
				}
			}
			if b, has := rm["backoff_sec"]; has {
				n, ok := asNumber(b)
				if !ok || n < 0 {
					errs = append(errs, id+": precondition.retry.backoff_sec must be a non-negative number")
				} else {
					p.Retry.BackoffSec = n
				}
			}
		default:
			errs = append(errs, id+": unknown precondition field "+key)
		}
	}
	if p.Screen == "" && len(errs) == 0 {
		errs = append(errs, id+": precondition.screen must describe the expected screen")
	}
	return p, errs
}

func validateShell(id string, m map[string]any, step *Step) []string {
	_, hasCmd := m["command"]
	_, hasArgv := m["argv"]
	if hasCmd && m["command"] != nil && hasArgv && m["argv"] != nil {
		return []string{id + ": set command or argv, not both"}
	}
	if hasArgv && m["argv"] != nil {
		list, ok := asStringList(m["argv"])
		if !ok || len(list) == 0 {
			return []string{id + ": argv must be a non-empty list of strings"}
		}
		for _, p := range list {
			if strings.TrimSpace(p) == "" {
				return []string{id + ": argv must be a non-empty list of strings"}
			}
		}
		step.Argv = list
		return nil
	}
	cmd, ok := asString(m["command"])
	if !ok || strings.TrimSpace(cmd) == "" {
		return []string{id + ": shell requires command or argv"}
	}
	step.Command = cmd
	return nil
}

func validateGate(id string, m map[string]any, step *Step) []string {
	var errs []string
	prompt, ok := asString(m["prompt"])
	if !ok || strings.TrimSpace(prompt) == "" {
		errs = append(errs, id+": human_gate requires prompt")
	} else {
		step.Prompt = prompt
	}
	if v := m["hint"]; v != nil {
		s, ok := asString(v)
		if !ok {
			errs = append(errs, id+": hint must be a string")
		} else {
			step.Hint = s
		}
	}
	if v, ok := m["allow_comment"].(bool); ok {
		step.AllowComment = v
	}
	raw, ok := m["choices"].([]any)
	if !ok || len(raw) == 0 {
		errs = append(errs, id+": human_gate requires choices")
		return errs
	}
	seen := map[string]bool{}
	for _, item := range raw {
		cm, ok := item.(map[string]any)
		if !ok {
			errs = append(errs, id+": choice must be a mapping")
			continue
		}
		cid, ok := asString(cm["id"])
		if !ok || cid == "" {
			errs = append(errs, id+": choice missing id")
			continue
		}
		if seen[cid] {
			errs = append(errs, id+": duplicate choice id "+cid)
		}
		seen[cid] = true
		label, ok := asString(cm["label"])
		if !ok || strings.TrimSpace(label) == "" {
			errs = append(errs, id+": choice "+cid+" requires label")
		}
		c := Choice{ID: cid, Label: label, Outcome: OutcomeContinue}
		if v := cm["outcome"]; v != nil {
			o, _ := asString(v)
			if !outcomes[o] {
				errs = append(errs, fmt.Sprintf("%s: choice %s has unknown outcome %q", id, cid, o))
			} else {
				c.Outcome = o
			}
		}
		if v := cm["comment"]; v != nil {
			s, _ := asString(v)
			if s != "required" {
				errs = append(errs, id+": choice "+cid+" comment must be 'required'")
			} else {
				c.Comment = s
			}
		}
		if v := cm["next"]; v != nil {
			if s, ok := asString(v); ok {
				c.Next = s
			}
		}
		step.Choices = append(step.Choices, c)
	}
	step.Judgment = JudgmentDynamic
	if v := m["judgment"]; v != nil {
		j, _ := asString(v)
		if j != JudgmentStatic && j != JudgmentDynamic {
			errs = append(errs, id+": judgment must be static or dynamic")
		} else {
			step.Judgment = j
		}
	}
	if v, exists := m["appraise"]; exists && v != nil {
		am, ok := v.(map[string]any)
		switch {
		case !ok:
			errs = append(errs, id+": appraise must be a mapping")
		case step.Judgment != JudgmentStatic:
			errs = append(errs, id+": appraise requires judgment: static")
		default:
			for key := range am {
				if key != "model" && key != "prompt" && key != "timeout_sec" {
					errs = append(errs, id+": unknown appraise field "+key)
				}
			}
			if mv, has := am["model"]; has {
				raw, modelErrs := parseModelPredicates(id, mv)
				errs = append(errs, modelErrs...)
				step.AppraiseModel = raw
			}
			if pv, has := am["prompt"]; has {
				p, ok := asString(pv)
				if !ok {
					errs = append(errs, id+": appraise.prompt must be a string")
				} else {
					step.AppraisePrompt = p
				}
			}
			if tv, has := am["timeout_sec"]; has {
				n, ok := asNumber(tv)
				if !ok || n <= 0 {
					errs = append(errs, id+": appraise.timeout_sec must be a positive number")
				} else {
					step.AppraiseTimeoutSec = &n
				}
			}
		}
	}
	if v, exists := m["precondition"]; exists && v != nil {
		p, perrs := parsePrecondition(id, v)
		errs = append(errs, perrs...)
		step.Precondition = p
		if strings.TrimSpace(step.Device) == "" {
			errs = append(errs, id+": precondition requires device (the screen it checks)")
		}
	}
	if step.Judgment == JudgmentStatic {
		if strings.TrimSpace(step.Device) == "" {
			errs = append(errs, id+": judgment static requires device (the screen a model appraises)")
		}
		if step.AppraiseModel == nil {
			step.AppraiseModel = json.RawMessage(defaultAppraiseModel)
		}
	}
	return errs
}

func requiresCycleErrors(steps []Step) []string {
	byID := map[string][]string{}
	for _, s := range steps {
		byID[s.ID] = append([]string{}, s.Requires...)
	}
	var errs []string
	visiting := map[string]bool{}
	seen := map[string]bool{}
	var walk func(id string, stack []string)
	walk = func(id string, stack []string) {
		if _, ok := byID[id]; !ok {
			return
		}
		if seen[id] {
			return
		}
		if visiting[id] {
			errs = append(errs, "requires cycle: "+strings.Join(append(stack, id), " -> "))
			return
		}
		visiting[id] = true
		for _, req := range byID[id] {
			walk(req, append(stack, id))
		}
		visiting[id] = false
		seen[id] = true
	}
	for _, s := range steps {
		walk(s.ID, nil)
	}
	return errs
}

// MergeParams overlays overrides on workflow defaults.
func MergeParams(wf *Workflow, overrides map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range wf.Params {
		out[k] = v
	}
	for k, v := range overrides {
		out[k] = v
	}
	return out
}

// CheckPlaceholders reports ${name} references the run cannot fill.
func CheckPlaceholders(wf *Workflow, params map[string]string) []string {
	var errs []string
	missing := map[string]bool{}
	collectMissing(wf.Steps, params, missing)
	collectMissing(wf.Cleanup, params, missing)
	collectMissing(wf.SuggestedCommands, params, missing)
	collectMissing(wf.Groups, params, missing)
	names := make([]string, 0, len(missing))
	for n := range missing {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		errs = append(errs, "missing param ${"+n+"}")
	}
	return errs
}

func collectMissing(v any, params map[string]string, missing map[string]bool) {
	switch t := v.(type) {
	case string:
		for _, m := range paramRE.FindAllStringSubmatch(t, -1) {
			if _, ok := params[m[1]]; !ok {
				missing[m[1]] = true
			}
		}
	case []string:
		for _, s := range t {
			collectMissing(s, params, missing)
		}
	case map[string]string:
		for _, s := range t {
			collectMissing(s, params, missing)
		}
	case []Step:
		for i := range t {
			collectMissing(t[i], params, missing)
		}
	case Step:
		collectMissing(t.Command, params, missing)
		collectMissing(t.Argv, params, missing)
		collectMissing(t.Script, params, missing)
		collectMissing(t.Prompt, params, missing)
		collectMissing(t.Accept, params, missing)
		collectMissing(t.Hint, params, missing)
		collectMissing(t.AppraisePrompt, params, missing)
		if t.Precondition != nil {
			collectMissing(t.Precondition.Screen, params, missing)
			collectMissing(t.Precondition.Mutex, params, missing)
		}
		collectMissing(t.Label, params, missing)
		collectMissing(t.Device, params, missing)
		collectMissing(t.Mutex, params, missing)
		collectMissing(t.Env, params, missing)
		collectMissing(t.Params, params, missing)
		for _, c := range t.Choices {
			collectMissing(c.Label, params, missing)
			collectMissing(c.ID, params, missing)
		}
	case []Group:
		for _, g := range t {
			collectMissing(g.Label, params, missing)
		}
	}
}

// Substitute fills ${name} in a copy of wf using params. Caller must have
// already checked placeholders.
func Substitute(wf *Workflow, params map[string]string) *Workflow {
	out := *wf
	out.Params = map[string]string{}
	for k, v := range wf.Params {
		out.Params[k] = subString(v, params)
	}
	out.SuggestedCommands = subStringList(wf.SuggestedCommands, params)
	out.Groups = make([]Group, len(wf.Groups))
	for i, g := range wf.Groups {
		g.Label = subString(g.Label, params)
		out.Groups[i] = g
	}
	subSteps := func(steps []Step) []Step {
		out := make([]Step, len(steps))
		for i, s := range steps {
			s.Label = subString(s.Label, params)
			s.Command = subString(s.Command, params)
			s.Argv = subStringList(s.Argv, params)
			s.Script = subString(s.Script, params)
			s.Prompt = subString(s.Prompt, params)
			s.Accept = subString(s.Accept, params)
			s.Hint = subString(s.Hint, params)
			s.AppraisePrompt = subString(s.AppraisePrompt, params)
			if s.Precondition != nil {
				p := *s.Precondition
				p.Screen = subString(p.Screen, params)
				p.Mutex = subString(p.Mutex, params)
				s.Precondition = &p
			}
			s.Device = subString(s.Device, params)
			s.Mutex = subString(s.Mutex, params)
			s.Env = subStringMap(s.Env, params)
			s.Params = subStringMap(s.Params, params)
			s.Choices = append([]Choice{}, s.Choices...)
			for j := range s.Choices {
				s.Choices[j].Label = subString(s.Choices[j].Label, params)
			}
			out[i] = s
		}
		return out
	}
	out.Steps = subSteps(wf.Steps)
	out.Cleanup = subSteps(wf.Cleanup)
	return &out
}

func subString(s string, params map[string]string) string {
	return paramRE.ReplaceAllStringFunc(s, func(m string) string {
		name := paramRE.FindStringSubmatch(m)[1]
		return params[name]
	})
}

func subStringList(in []string, params map[string]string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = subString(s, params)
	}
	return out
}

func subStringMap(in map[string]string, params map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = subString(v, params)
	}
	return out
}

func asString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

func asStringList(v any) ([]string, bool) {
	list, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, ok := asString(item)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func asNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case uint64:
		return float64(t), true
	case float64:
		return t, true
	default:
		return 0, false
	}
}

func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case uint64:
		return int(t), true
	case float64:
		if t == float64(int(t)) {
			return int(t), true
		}
		return 0, false
	default:
		return 0, false
	}
}

func stringify(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(t)
	}
}

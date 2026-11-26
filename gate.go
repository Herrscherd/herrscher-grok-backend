package grok

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	inspectEvent       = "pre_tool_use"
	inspectProject     = "project"
	inspectMatchAll    = "*"
	approveHookCommand = " approve hook"
	maxInspectOutput   = 8 << 20
)

// ErrGateNotLoaded reports a turn refused because the session asks for
// approvals and grok does not load the herrscher approval hook in its working
// directory, so every tool call of the turn would run unasked. Every
// [*GateError] matches it.
var ErrGateNotLoaded = errors.New("grok does not load the herrscher approval hook, the turn is refused")

// GateReason says why the herrscher approval hook was not found loaded.
type GateReason uint8

// The closed set of gate reasons.
const (
	GateInspectFailed GateReason = iota + 1
	GateInspectTooLarge
	GateInspectUnreadable
	GateHookMissing
	GateHookDisabled
	GateHookOtherBinary
)

func (r GateReason) String() string {
	switch r {
	case GateInspectFailed:
		return "grok inspect failed"
	case GateInspectTooLarge:
		return "grok inspect printed more than the gate reads"
	case GateInspectUnreadable:
		return "grok inspect printed a listing the gate cannot read"
	case GateHookMissing:
		return "the hook is not listed for this directory"
	case GateHookDisabled:
		return "the hook is listed but disabled"
	case GateHookOtherBinary:
		return "the hook calls another herrscher binary"
	}
	return "unknown reason"
}

func (r GateReason) remedy() string {
	switch r {
	case GateHookDisabled:
		return "enable the herrscher hook again with /hooks in grok"
	case GateHookOtherBinary, GateHookMissing:
		return "recreate the session so herrscher writes its hook again"
	case GateInspectFailed, GateInspectTooLarge, GateInspectUnreadable:
		return "check grok inspect --json in the worktree, then upgrade the grok backend if its format changed"
	}
	return ""
}

// GateError is a turn refused by the approval gate, with the Reason it was
// refused and, for an inspect that failed or could not be read, the Cause.
type GateError struct {
	Reason GateReason
	Cause  error
}

func (e *GateError) Error() string {
	message := ErrGateNotLoaded.Error() + ": " + e.Reason.String()
	if e.Cause != nil {
		message += " (" + e.Cause.Error() + ")"
	}
	return message + "; " + e.Reason.remedy()
}

// Is reports [ErrGateNotLoaded] as the error every gate refusal is.
func (*GateError) Is(target error) bool {
	return target == ErrGateNotLoaded
}

func (e *GateError) Unwrap() error {
	return e.Cause
}

type approvalGate struct {
	bin string
}

func approvalGateFrom(getenv func(string) string) (approvalGate, bool) {
	gate := contracts.ApprovalsFromEnv(getenv)
	return approvalGate{bin: gate.Bin}, gate.Gated
}

func (g approvalGate) hookCommand() string {
	return "'" + strings.ReplaceAll(g.bin, "'", `'\''`) + "'" + approveHookCommand
}

type inspectedHook struct {
	Event    string `json:"event"`
	Target   string `json:"target"`
	Matcher  string `json:"matcher"`
	Disabled bool   `json:"disabled"`
	Source   struct {
		Type string `json:"type"`
	} `json:"source"`
}

func (h inspectedHook) isProjectGate() bool {
	return h.Event == inspectEvent && h.Source.Type == inspectProject && h.Matcher == inspectMatchAll && strings.HasSuffix(h.Target, approveHookCommand)
}

func (g approvalGate) check(ctx context.Context, base []string, spec childSpec) error {
	output, err := runBoundedWithin(ctx, append(slices.Clone(base), "inspect", "--json"), spec.dir, spec.env, maxInspectOutput)
	if err != nil {
		return &GateError{Reason: GateInspectFailed, Cause: err}
	}
	if output.stdout.exceeded {
		return &GateError{Reason: GateInspectTooLarge, Cause: &OutputTooLargeError{Limit: maxInspectOutput}}
	}
	var inspected struct {
		Hooks []inspectedHook `json:"hooks"`
	}
	if err := json.Unmarshal(output.stdout.data.Bytes(), &inspected); err != nil {
		return &GateError{Reason: GateInspectUnreadable, Cause: err}
	}
	return g.verdict(inspected.Hooks)
}

func (g approvalGate) verdict(hooks []inspectedHook) error {
	reason := GateHookMissing
	want := g.hookCommand()
	for _, hook := range hooks {
		switch {
		case !hook.isProjectGate():
		case hook.Target != want:
			if reason == GateHookMissing {
				reason = GateHookOtherBinary
			}
		case hook.Disabled:
			reason = GateHookDisabled
		default:
			return nil
		}
	}
	return &GateError{Reason: reason}
}

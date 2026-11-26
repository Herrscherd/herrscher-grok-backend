package grok

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	inspectEvent       = "pre_tool_use"
	inspectProject     = "project"
	inspectMatchAll    = "*"
	approveHookCommand = " approve hook"
	approvalsBypass    = "bypass"
)

// ErrGateNotLoaded reports a turn refused because the session asks for
// approvals and grok does not load the herrscher approval hook in its working
// directory, so every tool call of the turn would run unasked.
var ErrGateNotLoaded = errors.New("grok does not load the herrscher approval hook, the turn is refused")

type approvalGate struct {
	bin string
}

func approvalGateFrom(getenv func(string) string) (approvalGate, bool) {
	mode := strings.TrimSpace(getenv(contracts.EnvApprovalsMode))
	bin := getenv(contracts.EnvApprovalsBin)
	if mode == "" || mode == approvalsBypass || bin == "" {
		return approvalGate{}, false
	}
	return approvalGate{bin: bin}, true
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

func (g approvalGate) check(ctx context.Context, base []string, spec childSpec) error {
	output, err := runBounded(ctx, append(slices.Clone(base), "inspect", "--json"), spec.dir, spec.env)
	if err != nil {
		return fmt.Errorf("%w: grok inspect: %w", ErrGateNotLoaded, err)
	}
	if output.stdout.exceeded {
		return fmt.Errorf("%w: %w", ErrGateNotLoaded, &OutputTooLargeError{Limit: boundedOutputMax})
	}
	var inspected struct {
		Hooks []inspectedHook `json:"hooks"`
	}
	if err := json.Unmarshal(output.stdout.data.Bytes(), &inspected); err != nil {
		return fmt.Errorf("%w: grok inspect: %w", ErrGateNotLoaded, err)
	}
	want := g.hookCommand()
	for _, hook := range inspected.Hooks {
		if hook.Event == inspectEvent && hook.Source.Type == inspectProject && hook.Target == want && hook.Matcher == inspectMatchAll && !hook.Disabled {
			return nil
		}
	}
	return ErrGateNotLoaded
}

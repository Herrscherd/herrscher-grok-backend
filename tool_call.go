package grok

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"unicode/utf8"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	maxOpenToolCalls = 256
	maxSeenToolCalls = 4096
	maxToolCallID    = 256
	maxToolName      = 256
	maxLineRunes     = 200

	statusCompleted = "completed"
	statusFailed    = "failed"
	rawOutputBash   = "Bash"
	reasonFailed    = "failed"
	boundMaxOpen    = "max_open"
	boundMaxSeen    = "max_seen"
)

func summaryKeys() []string {
	return []string{"command", "target_file", "file_path", "path", "target_directory", "pattern", "query", "url", "description"}
}

type toolCallTracker struct {
	open       map[string]string
	seen       map[string]struct{}
	warnedOpen bool
	warnedSeen bool
}

func (t *toolCallTracker) start(event *grokEvent, forward func(contracts.BackendEvent)) {
	started := contracts.BackendEvent{Kind: contracts.BackendKindTool}
	if isBoundedName(event.ToolName) {
		started.Tool = event.ToolName
		started.Detail = toolCallSummary(event.RawInput)
	}
	if started.Tool != "" && t.begin(event.ToolCallID, started.Tool) {
		started.ToolCall = &contracts.ToolCall{ID: event.ToolCallID, Tool: started.Tool, Detail: started.Detail, State: contracts.ToolCallStarted}
	}
	forward(started)
	if started.ToolCall != nil {
		t.update(event, forward)
	}
}

func (t *toolCallTracker) update(event *grokEvent, forward func(contracts.BackendEvent)) {
	if event.Status != statusCompleted && event.Status != statusFailed {
		return
	}
	tool, open := t.finish(event.ToolCallID)
	if !open {
		return
	}
	outcome := toolCallOutcome(event)
	forward(contracts.BackendEvent{Kind: contracts.BackendKindToolDone, ToolCall: &contracts.ToolCall{ID: event.ToolCallID, Tool: tool, State: outcome.state, Error: outcome.reason}})
}

func (t *toolCallTracker) begin(callID, tool string) bool {
	if callID == "" || len(callID) > maxToolCallID {
		return false
	}
	if _, isSeen := t.seen[callID]; isSeen {
		return false
	}
	if len(t.seen) >= maxSeenToolCalls {
		t.warnOnce(&t.warnedSeen, boundMaxSeen, maxSeenToolCalls)
		return false
	}
	if t.seen == nil {
		t.seen = make(map[string]struct{})
		t.open = make(map[string]string)
	}
	t.seen[callID] = struct{}{}
	if len(t.open) >= maxOpenToolCalls {
		t.warnOnce(&t.warnedOpen, boundMaxOpen, maxOpenToolCalls)
		return false
	}
	t.open[callID] = tool
	return true
}

func (t *toolCallTracker) finish(callID string) (string, bool) {
	tool, open := t.open[callID]
	if open {
		delete(t.open, callID)
	}
	return tool, open
}

func (*toolCallTracker) warnOnce(warned *bool, bound string, limit int) {
	if *warned {
		return
	}
	*warned = true
	slog.Warn("grok tool call bound reached, later calls are not followed", "bound", bound, "limit", limit)
}

func isBoundedName(name string) bool {
	return name != "" && len(name) <= maxToolName
}

func toolCallSummary(input map[string]json.RawMessage) string {
	for _, key := range summaryKeys() {
		var value string
		if json.Unmarshal(input[key], &value) != nil {
			continue
		}
		if line := firstLine(value); line != "" {
			return line
		}
	}
	return ""
}

type toolOutcome struct {
	state  contracts.ToolCallState
	reason string
}

func toolCallOutcome(event *grokEvent) toolOutcome {
	if event.Status == statusFailed {
		return toolOutcome{state: contracts.ToolCallFailed, reason: failureReason(event.Content)}
	}
	if reason, isFailed := shellFailure(event.RawOutput); isFailed {
		return toolOutcome{state: contracts.ToolCallFailed, reason: reason}
	}
	return toolOutcome{state: contracts.ToolCallSucceeded}
}

func failureReason(content []toolContent) string {
	for _, block := range content {
		if line := firstLine(block.Content.Text); line != "" {
			return line
		}
	}
	return reasonFailed
}

type shellOutput struct {
	Type     string      `json:"type"`
	ExitCode json.Number `json:"exit_code"`
	TimedOut bool        `json:"timed_out"`
}

func shellFailure(raw json.RawMessage) (string, bool) {
	var output shellOutput
	if json.Unmarshal(raw, &output) != nil || output.Type != rawOutputBash {
		return "", false
	}
	if output.TimedOut {
		return reasonTimeout, true
	}
	code, err := strconv.ParseInt(output.ExitCode.String(), 10, 32)
	if err != nil || code == 0 {
		return "", false
	}
	return "exit code " + strconv.FormatInt(code, 10), true
}

func firstLine(text string) string {
	for line := range strings.FieldsFuncSeq(text, isLineBreak) {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return truncateRunes(trimmed, maxLineRunes)
		}
	}
	return ""
}

func isLineBreak(char rune) bool {
	return char == '\n' || char == '\r'
}

func truncateRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit])
}

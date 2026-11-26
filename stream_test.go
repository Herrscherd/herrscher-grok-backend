package grok

import (
	"bufio"
	"fmt"
	"slices"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func readTurnEvents(t *testing.T, lines ...string) ([]contracts.BackendEvent, turnResult) {
	t.Helper()
	var events []contracts.BackendEvent
	turn, err := readTurn(bufio.NewReader(strings.NewReader(strings.Join(lines, "\n")+"\n")), func(event contracts.BackendEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	return events, turn
}

func finishedCalls(events []contracts.BackendEvent) []contracts.ToolCall {
	var calls []contracts.ToolCall
	done := eventsOfKind(events, contracts.BackendKindToolDone)
	for index := range done {
		calls = append(calls, *done[index].ToolCall)
	}
	return calls
}

const endLine = `{"type":"end","stopReason":"end_turn","sessionId":"s"}`

func startLine(callID, tool, input string) string {
	return fmt.Sprintf(`{"type":"tool_call","toolCallId":%q,"toolName":%q,"status":"pending","rawInput":%s}`, callID, tool, input)
}

func TestToolCallFailuresReadTheirReason(t *testing.T) {
	tests := []struct {
		name   string
		update string
		want   contracts.ToolCall
	}{
		{
			name:   "a failed call reads the first line of its content",
			update: `{"type":"tool_call_update","toolCallId":"c","status":"failed","content":[{"type":"content","content":{"type":"text","text":"\n  Error: missing.txt does not exist.\nNote: cwd"}}]}`,
			want:   contracts.ToolCall{ID: "c", Tool: "run_terminal_command", State: contracts.ToolCallFailed, Error: "Error: missing.txt does not exist."},
		},
		{
			name:   "a failed call without text names the failure",
			update: `{"type":"tool_call_update","toolCallId":"c","status":"failed","content":[]}`,
			want:   contracts.ToolCall{ID: "c", Tool: "run_terminal_command", State: contracts.ToolCallFailed, Error: "failed"},
		},
		{
			name:   "a shell that timed out failed",
			update: `{"type":"tool_call_update","toolCallId":"c","status":"completed","rawOutput":{"type":"Bash","exit_code":0,"timed_out":true}}`,
			want:   contracts.ToolCall{ID: "c", Tool: "run_terminal_command", State: contracts.ToolCallFailed, Error: "timeout"},
		},
		{
			name:   "an exit code that is not a whole int32 is not quoted",
			update: `{"type":"tool_call_update","toolCallId":"c","status":"completed","rawOutput":{"type":"Bash","exit_code":1.5}}`,
			want:   contracts.ToolCall{ID: "c", Tool: "run_terminal_command", State: contracts.ToolCallSucceeded},
		},
		{
			name:   "a completed shell that exited 0 succeeded",
			update: `{"type":"tool_call_update","toolCallId":"c","status":"completed","rawOutput":{"type":"Bash","exit_code":0}}`,
			want:   contracts.ToolCall{ID: "c", Tool: "run_terminal_command", State: contracts.ToolCallSucceeded},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events, _ := readTurnEvents(t, startLine("c", "run_terminal_command", `{"command":"true"}`), test.update, endLine)
			if calls := finishedCalls(events); !slices.Equal(calls, []contracts.ToolCall{test.want}) {
				t.Fatalf("finished calls = %+v, want %+v", calls, test.want)
			}
		})
	}
}

func TestCapturedFailuresFinishTheirCalls(t *testing.T) {
	tests := []struct {
		fixture string
		want    string
	}{
		{fixture: "failed_read.jsonl", want: "Error: /nonexistent/zzz.txt does not exist."},
		{fixture: "hook_denied.jsonl", want: "Hook denied: blocked by herrscher test"},
	}
	for _, test := range tests {
		t.Run(test.fixture, func(t *testing.T) {
			turn := respond(t, newStubBackend(t, Config{Cmd: replayStub(t, test.fixture, "0")}), "hi")
			if turn.err != nil {
				t.Fatal(turn.err)
			}
			calls := finishedCalls(turn.events)
			if len(calls) != 1 || calls[0].State != contracts.ToolCallFailed || calls[0].Error != test.want {
				t.Fatalf("finished calls = %+v, want one failure reading %q", calls, test.want)
			}
		})
	}
}

func TestAToolCallFinishesOnceAndOnlyWhenStarted(t *testing.T) {
	done := `{"type":"tool_call_update","toolCallId":"c","status":"completed"}`
	events, _ := readTurnEvents(t,
		`{"type":"tool_call_update","toolCallId":"unknown","status":"completed"}`,
		startLine("c", "read_file", `{"target_file":"a"}`),
		done,
		done,
		startLine("c", "read_file", `{"target_file":"b"}`),
		done,
		endLine,
	)
	if calls := finishedCalls(events); len(calls) != 1 {
		t.Fatalf("finished calls = %+v, want one", calls)
	}
	starts := eventsOfKind(events, contracts.BackendKindTool)
	if len(starts) != 2 || starts[1].ToolCall != nil || starts[1].Tool != "read_file" {
		t.Fatalf("starts = %+v, want the repeated id shown as a plain tool event", starts)
	}
}

func TestAToolCallThatStartsFinishedIsClosedAtOnce(t *testing.T) {
	events, _ := readTurnEvents(t, `{"type":"tool_call","toolCallId":"c","toolName":"grep","status":"completed","rawInput":{"pattern":"x"}}`, endLine)
	calls := finishedCalls(events)
	if len(calls) != 1 || calls[0].State != contracts.ToolCallSucceeded {
		t.Fatalf("finished calls = %+v, want the call closed", calls)
	}
}

func TestOpenToolCallsAreBounded(t *testing.T) {
	lines := make([]string, 0, maxOpenToolCalls+2)
	for index := range maxOpenToolCalls + 1 {
		lines = append(lines, startLine(fmt.Sprintf("c%d", index), "grep", `{}`))
	}
	lines = append(lines, endLine)
	events, _ := readTurnEvents(t, lines...)
	starts := eventsOfKind(events, contracts.BackendKindTool)
	if starts[maxOpenToolCalls-1].ToolCall == nil || starts[maxOpenToolCalls].ToolCall != nil {
		t.Fatal("the call past the open bound was followed, or one under it was not")
	}
}

func TestToolSummaryIsTheFirstLineOfItsKey(t *testing.T) {
	events, _ := readTurnEvents(t, startLine("c", "run_terminal_command", `{"description":"ignored","command":"\ncat <<EOF\nbody\nEOF"}`), endLine)
	if started := eventsOfKind(events, contracts.BackendKindTool); started[0].Detail != "cat <<EOF" {
		t.Fatalf("detail = %q, want the first line of the command", started[0].Detail)
	}
}

func TestPlanEntriesBecomeTodos(t *testing.T) {
	turn := respond(t, newStubBackend(t, Config{Cmd: replayStub(t, "todos.jsonl", "0")}), "hi")
	if turn.err != nil {
		t.Fatal(turn.err)
	}
	todos := eventsOfKind(turn.events, backendKindTodos)
	if len(todos) != 2 {
		t.Fatalf("todo events = %d, want 2", len(todos))
	}
	want := []contracts.TodoItem{
		{Text: "Create the three-item todo list", State: "completed"},
		{Text: "Mark the first item completed", State: "pending"},
		{Text: "Spawn a subagent that replies hi", State: "pending"},
	}
	if !slices.Equal(todos[1].Todos, want) {
		t.Fatalf("todos = %+v, want %+v", todos[1].Todos, want)
	}
}

func TestThoughtsAndTextsAreSeparateBlocks(t *testing.T) {
	events, turn := readTurnEvents(t,
		`{"type":"thought","data":"plan "}`,
		`{"type":"thought","data":"it"}`,
		`{"type":"text","data":"   "}`,
		`{"type":"thought","data":"more"}`,
		`{"type":"text","data":"answer"}`,
		endLine,
	)
	var kinds []string
	for _, event := range events {
		kinds = append(kinds, event.Kind+":"+event.Detail)
	}
	want := []string{"thinking:plan it", "thinking:more", "text:answer", "result:"}
	if !slices.Equal(kinds, want) {
		t.Fatalf("events = %q, want %q", kinds, want)
	}
	if turn.Text != "answer" {
		t.Fatalf("reply = %q, want answer", turn.Text)
	}
}

func TestUsageWithoutTokensIsNotForwarded(t *testing.T) {
	events, _ := readTurnEvents(t, `{"type":"usage","usage":{"input_tokens":0,"output_tokens":0}}`, endLine)
	if usages := eventsOfKind(events, backendKindUsage); len(usages) != 0 {
		t.Fatalf("usage events = %+v, want none", usages)
	}
}

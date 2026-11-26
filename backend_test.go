package grok

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func writeStubScript(t *testing.T, body string) string {
	t.Helper()
	stub := filepath.Join(t.TempDir(), "stub")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return stub
}

func replayStub(t *testing.T, fixture, exitCode string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	return writeStubScript(t, "cat > /dev/null\ncat '"+path+"'\nexit "+exitCode)
}

type recordedTurn struct {
	text   string
	err    error
	events []contracts.BackendEvent
}

func respond(t *testing.T, backend contracts.Backend, content string) recordedTurn {
	t.Helper()
	var turn recordedTurn
	turn.text, turn.err = backend.Respond(t.Context(), contracts.Prompt{Content: content}, func(event contracts.BackendEvent) {
		turn.events = append(turn.events, event)
	})
	return turn
}

func newStubBackend(t *testing.T, config Config) contracts.Backend {
	t.Helper()
	backend, err := NewBackend(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	return backend
}

func eventsOfKind(events []contracts.BackendEvent, kind string) []contracts.BackendEvent {
	var kept []contracts.BackendEvent
	for index := range events {
		if events[index].Kind == kind {
			kept = append(kept, events[index])
		}
	}
	return kept
}

func resumeToken(t *testing.T, backend contracts.Backend) string {
	t.Helper()
	aware, isAware := backend.(contracts.ResumeAware)
	if !isAware {
		t.Fatalf("backend %T does not resume", backend)
	}
	return aware.ResumeToken()
}

func TestRespondReplaysACapturedToolTurn(t *testing.T) {
	backend := newStubBackend(t, Config{Cmd: replayStub(t, "tools_turn.jsonl", "0")})
	turn := respond(t, backend, "read a.txt")
	if turn.err != nil {
		t.Fatal(turn.err)
	}
	wantText := "I'll read `a.txt` and run the first shell command.\n\nThe content of `a.txt` is:\n\n```\nhello grok\n```\n\n`ls -la && false` exited with code 1. `echo done` printed `done`."
	if turn.text != wantText {
		t.Fatalf("reply = %q, want %q", turn.text, wantText)
	}
	texts := eventsOfKind(turn.events, backendKindText)
	if len(texts) != 2 || !strings.HasPrefix(texts[1].Detail, textBlockSeparator) {
		t.Fatalf("text events = %+v, want two blocks, the second one separated", texts)
	}
	if texts[0].Detail+texts[1].Detail != turn.text {
		t.Fatal("the text events do not add up to the reply")
	}
	if thoughts := eventsOfKind(turn.events, backendKindThinking); len(thoughts) != 3 {
		t.Fatalf("thinking events = %d, want 3", len(thoughts))
	}
	assertCapturedToolCalls(t, turn.events)
	assertCapturedSpend(t, turn.events)
	if token := resumeToken(t, backend); token != "01a2174f-7cd0-7362-8d49-067a0220bd35" {
		t.Fatalf("resume token = %q, want the end event's session id", token)
	}
}

func assertCapturedToolCalls(t *testing.T, events []contracts.BackendEvent) {
	t.Helper()
	var calls []contracts.ToolCall
	for index := range events {
		event := &events[index]
		if event.ToolCall == nil {
			continue
		}
		if err := event.ValidateToolCall(); err != nil {
			t.Fatalf("event %+v breaks the tool call contract: %v", event, err)
		}
		calls = append(calls, *event.ToolCall)
	}
	want := []contracts.ToolCall{
		{ID: "call-04fad4da-13c2-40ff-b2e6-1c0a750ee3e7-0", Tool: "read_file", Detail: "/work/a.txt", State: contracts.ToolCallStarted},
		{ID: "call-04fad4da-13c2-40ff-b2e6-1c0a750ee3e7-1", Tool: "run_terminal_command", Detail: "ls -la && false", State: contracts.ToolCallStarted},
		{ID: "call-04fad4da-13c2-40ff-b2e6-1c0a750ee3e7-0", Tool: "read_file", State: contracts.ToolCallSucceeded},
		{ID: "call-04fad4da-13c2-40ff-b2e6-1c0a750ee3e7-1", Tool: "run_terminal_command", State: contracts.ToolCallFailed, Error: "exit code 1"},
		{ID: "call-86fdd2f8-5b63-440a-878d-857f0ba106ab-2", Tool: "run_terminal_command", Detail: "echo done", State: contracts.ToolCallStarted},
		{ID: "call-86fdd2f8-5b63-440a-878d-857f0ba106ab-2", Tool: "run_terminal_command", State: contracts.ToolCallSucceeded},
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("tool calls = %+v\nwant %+v", calls, want)
	}
}

func assertCapturedSpend(t *testing.T, events []contracts.BackendEvent) {
	t.Helper()
	if usages := eventsOfKind(events, backendKindUsage); len(usages) != 3 || usages[0].InTokens != 19536 || usages[0].CacheRead != 7040 {
		t.Fatalf("usage events = %+v, want the three captured responses", usages)
	}
	results := eventsOfKind(events, backendKindResult)
	if len(results) != 1 {
		t.Fatalf("result events = %d, want 1", len(results))
	}
	result := results[0]
	wantModels := []contracts.ModelUsage{{Model: "grok-4.7-build", In: 20147, Out: 572, CacheRead: 60416, Cost: 0.073934}}
	if result.IsError || result.Cost != 0.073934 || result.InTokens != 20147 || result.OutTokens != 572 || result.CacheRead != 60416 || !slices.Equal(result.Models, wantModels) {
		t.Fatalf("result = %+v, want the end event's spend", result)
	}
	if events[len(events)-1].Kind != backendKindResult {
		t.Fatal("the result is not the last event of the turn")
	}
}

func TestRespondSendsThePromptOnStdinWithTheTurnFlags(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record")
	stub := writeStubScript(t, `printf '%s\n' "$@" > '`+record+`.argv'
cat > '`+record+`.stdin'
env > '`+record+`.env'
printf '{"type":"end","stopReason":"end_turn","sessionId":"next"}\n'`)
	t.Setenv("GROK_FOLDER_TRUST", "1")
	backend := newStubBackend(t, Config{Cmd: stub, Model: "grok-4.7", Effort: "xhigh", ResumeID: "earlier", Env: map[string]string{"GROK_HOME": "/profile/home"}})
	turn := respond(t, backend, "secret prompt")
	if turn.err != nil {
		t.Fatal(turn.err)
	}
	argv := readLines(t, record+".argv")
	want := []string{"-p", "--output-format", "streaming-json", "--always-approve", "--no-auto-update", "--model", "grok-4.7", "--reasoning-effort", "xhigh", "--resume", "earlier"}
	if !slices.Equal(argv, want) {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
	if stdin := readFile(t, record+".stdin"); stdin != "secret prompt" {
		t.Fatalf("stdin = %q, want the prompt", stdin)
	}
	env := readLines(t, record+".env")
	if !slices.Contains(env, "GROK_FOLDER_TRUST=0") || slices.Contains(env, "GROK_FOLDER_TRUST=1") {
		t.Fatal("the child does not run with folder trust off alone, so the worktree's approval hook may not load")
	}
	if !slices.Contains(env, "GROK_HOME=/profile/home") {
		t.Fatal("the session env of the host does not reach the child")
	}
	if token := resumeToken(t, backend); token != "next" {
		t.Fatalf("resume token = %q, want next", token)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	return strings.Split(strings.TrimSuffix(readFile(t, path), "\n"), "\n")
}

func TestRespondStartsAFreshSessionWhenTheResumedOneIsGone(t *testing.T) {
	missing, err := filepath.Abs(filepath.Join("testdata", "resume_missing.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	stub := writeStubScript(t, `cat > /dev/null
for arg in "$@"; do
	if [ "$arg" = --resume ]; then cat '`+missing+`'; exit 1; fi
done
printf '{"type":"text","data":"fresh"}\n{"type":"end","stopReason":"end_turn","sessionId":"new-session"}\n'`)
	backend := newStubBackend(t, Config{Cmd: stub, ResumeID: "00000000-0000-0000-0000-000000000000"})
	turn := respond(t, backend, "hi")
	if turn.err != nil || turn.text != "fresh" {
		t.Fatalf("Respond = %q, %v; want the fresh turn's reply", turn.text, turn.err)
	}
	notices := eventsOfKind(turn.events, contracts.BackendKindNotice)
	if len(notices) != 1 || notices[0].Notice.Code != contracts.CodeResumeLost {
		t.Fatalf("notices = %+v, want one resume lost notice", notices)
	}
	if token := resumeToken(t, backend); token != "new-session" {
		t.Fatalf("resume token = %q, want the fresh session", token)
	}
}

func TestRespondKeepsTheSessionWhenAResumedTurnFailsForAnotherReason(t *testing.T) {
	stub := writeStubScript(t, `cat > /dev/null
printf '%s\n' '{"type":"error","message":"Not signed in. To authenticate without a browser, run grok login --device-code"}'
exit 1`)
	backend := newStubBackend(t, Config{Cmd: stub, ResumeID: "kept"})
	turn := respond(t, backend, "hi")
	failed, isFailed := errors.AsType[*TurnFailedError](turn.err)
	if !isFailed || !strings.HasPrefix(failed.Message, "Not signed in") {
		t.Fatalf("err = %v, want the error event as a failed turn", turn.err)
	}
	if len(eventsOfKind(turn.events, contracts.BackendKindNotice)) != 0 {
		t.Fatal("a turn refused for its login announced a lost session")
	}
	if token := resumeToken(t, backend); token != "kept" {
		t.Fatalf("resume token = %q, want it kept", token)
	}
}

func TestRespondReportsAStopReasonOtherThanEndTurn(t *testing.T) {
	stub := writeStubScript(t, `cat > /dev/null
printf '%s\n' '{"type":"text","data":"partial"}' '{"type":"end","stopReason":"hook_stopped","error":"stopped by policy","sessionId":"s"}'
exit 0`)
	turn := respond(t, newStubBackend(t, Config{Cmd: stub}), "hi")
	failed, isFailed := errors.AsType[*TurnFailedError](turn.err)
	if !isFailed || failed.StopReason != "hook_stopped" || failed.Message != "stopped by policy" {
		t.Fatalf("err = %v, want the stop reason and the hook's message", turn.err)
	}
	if turn.text != "partial" {
		t.Fatalf("reply = %q, want the partial text", turn.text)
	}
	results := eventsOfKind(turn.events, backendKindResult)
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("results = %+v, want one error result", results)
	}
}

func TestRespondReportsAStreamThatEndsWithoutItsEndEvent(t *testing.T) {
	stub := writeStubScript(t, `cat > /dev/null
printf '%s\n' '{"type":"text","data":"half"}' 'not json'`)
	turn := respond(t, newStubBackend(t, Config{Cmd: stub}), "hi")
	ended, isEnded := errors.AsType[*StreamEndedError](turn.err)
	if !isEnded || ended.Unparsable != 1 {
		t.Fatalf("err = %v, want a stream ended error counting one unparsable line", turn.err)
	}
	if turn.text != "half" {
		t.Fatalf("reply = %q, want the text read before the end", turn.text)
	}
}

func TestRespondRefusesAnOverlongLine(t *testing.T) {
	stub := writeStubScript(t, `cat > /dev/null
head -c 17000000 /dev/zero | tr '\0' 'x'
printf '\n'`)
	turn := respond(t, newStubBackend(t, Config{Cmd: stub}), "hi")
	if _, isTooLong := errors.AsType[*LineTooLongError](turn.err); !isTooLong {
		t.Fatalf("err = %v, want a line too long error", turn.err)
	}
}

func TestRespondRefusesAReplyPastItsCap(t *testing.T) {
	stub := writeStubScript(t, `cat > /dev/null
chunk=$(head -c 1048576 /dev/zero | tr '\0' 'x')
i=0
while [ $i -lt 17 ]; do printf '{"type":"text","data":"%s"}\n' "$chunk"; i=$((i+1)); done
printf '{"type":"end","stopReason":"end_turn","sessionId":"s"}\n'`)
	turn := respond(t, newStubBackend(t, Config{Cmd: stub}), "hi")
	if _, isTooLong := errors.AsType[*ReplyTooLongError](turn.err); !isTooLong {
		t.Fatalf("err = %v, want a reply too long error", turn.err)
	}
	if turn.text != "" {
		t.Fatal("an overlong reply was returned")
	}
}

func TestTurnArgvOmitsTheFlagsLeftEmpty(t *testing.T) {
	argv := turnArgv(baseCommand("grok --debug"), turnFlags{})
	want := []string{"grok", "--debug", "-p", "--output-format", "streaming-json", "--always-approve", "--no-auto-update"}
	if !slices.Equal(argv, want) {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
}

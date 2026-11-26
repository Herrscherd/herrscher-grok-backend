package grok

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const gatedBin = "/opt/it's/herrscher"

type gatedFake struct {
	stub   string
	marker string
}

func gatedStub(t *testing.T, hooks string) gatedFake {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "turn-ran")
	listing := filepath.Join(dir, "inspect.json")
	if err := os.WriteFile(listing, []byte(`{"hooks":`+hooks+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stub := writeStubScript(t, `if [ "$1" = inspect ]; then cat '`+listing+`'; exit 0; fi
cat > /dev/null
touch '`+marker+`'
printf '{"type":"end","stopReason":"end_turn","sessionId":"s"}\n'`)
	return gatedFake{stub: stub, marker: marker}
}

func gateEnv(t *testing.T, mode string) {
	t.Helper()
	t.Setenv(contracts.EnvApprovalsMode, mode)
	t.Setenv(contracts.EnvApprovalsSession, "gated")
	t.Setenv(contracts.EnvApprovalsBin, gatedBin)
}

func projectHook(t *testing.T, target string, disabled bool) string {
	t.Helper()
	hook := map[string]any{"event": "pre_tool_use", "target": target, "matcher": "*", "disabled": disabled, "source": map[string]string{"type": "project"}}
	data, err := json.Marshal([]any{hook})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const gatedCommand = `'/opt/it'\''s/herrscher' approve hook`

func TestAGatedTurnRunsWhenGrokLoadsTheHook(t *testing.T) {
	gateEnv(t, "ask")
	fake := gatedStub(t, projectHook(t, gatedCommand, false))
	if turn := respond(t, newStubBackend(t, Config{Cmd: fake.stub}), "hi"); turn.err != nil {
		t.Fatalf("err = %v, want the turn to run", turn.err)
	}
	if !fileExists(fake.marker) {
		t.Fatal("the turn did not run")
	}
}

func TestAGatedTurnIsRefusedWhenTheHookIsNotLoaded(t *testing.T) {
	tests := map[string]struct {
		hooks  string
		reason GateReason
	}{
		"no hook":       {hooks: `[]`, reason: GateHookMissing},
		"disabled hook": {hooks: projectHook(t, gatedCommand, true), reason: GateHookDisabled},
		"another bin":   {hooks: projectHook(t, "'/usr/bin/herrscher' approve hook", false), reason: GateHookOtherBinary},
		"unreadable":    {hooks: `not json`, reason: GateInspectUnreadable},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			gateEnv(t, "strict")
			fake := gatedStub(t, test.hooks)
			turn := respond(t, newStubBackend(t, Config{Cmd: fake.stub}), "hi")
			refused, isRefused := errors.AsType[*GateError](turn.err)
			if !isRefused || refused.Reason != test.reason || !errors.Is(turn.err, ErrGateNotLoaded) {
				t.Fatalf("err = %v, want a gate refusal for %s", turn.err, test.reason)
			}
			if fileExists(fake.marker) {
				t.Fatal("the turn ran without its approval hook")
			}
		})
	}
}

func TestTheGateReadsAnInspectListingPastTheProbeBound(t *testing.T) {
	gateEnv(t, "ask")
	filler := `{"event":"session_start","target":"` + strings.Repeat("x", boundedOutputMax) + `","source":{"type":"user"}}`
	hooks := projectHook(t, gatedCommand, false)
	fake := gatedStub(t, "["+filler+","+hooks[1:])
	if turn := respond(t, newStubBackend(t, Config{Cmd: fake.stub}), "hi"); turn.err != nil || !fileExists(fake.marker) {
		t.Fatalf("err = %v, want a listing over %d bytes read whole", turn.err, boundedOutputMax)
	}
}

func TestABypassSessionIsNotGated(t *testing.T) {
	gateEnv(t, "bypass")
	fake := gatedStub(t, `[]`)
	if turn := respond(t, newStubBackend(t, Config{Cmd: fake.stub}), "hi"); turn.err != nil || !fileExists(fake.marker) {
		t.Fatalf("err = %v, want an ungated turn to run", turn.err)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

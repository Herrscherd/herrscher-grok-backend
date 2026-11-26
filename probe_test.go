package grok

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	signedInListing = "You are logged in with grok.com.\n\nDefault model: grok-4.7\n\nAvailable models:\n  * grok-4.7 (default)\n  - grok-4.7-build-fast\n  - grok-4.7\n  - not a model!\n"
	apiKeyListing   = "You are using XAI_API_KEY.\n\nDefault model: grok-4.6\n\nAvailable models:\n  * grok-4.6 (default)\n"
	signedOutList   = "You are not authenticated.\n\nDefault model: grok-4.6\n\nAvailable models:\n  * grok-4.6 (default)\n"
	answeredTurn    = `{"text":"OK","stopReason":"end_turn","sessionId":"s"}`
)

func fakeGrok(t *testing.T, listing, turn string) string {
	t.Helper()
	return writeStubScript(t, `if [ "$1" = models ]; then printf '%s' '`+listing+`'; exit 0; fi
`+turn)
}

func envWith(command string) func(string) string {
	return func(name string) string {
		if name == envCmd {
			return command
		}
		return ""
	}
}

func runProbe(t *testing.T, command string, deep bool) contracts.ProbeReport {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	return probe(ctx, contracts.ProbeAccess{Getenv: envWith(command), Deep: deep})
}

func TestProbeReadsTheLoginOfTheModelsListing(t *testing.T) {
	tests := []struct {
		name    string
		listing string
		state   contracts.HealthState
		code    contracts.Code
		remedy  bool
	}{
		{name: "signed in", listing: signedInListing, state: contracts.HealthOK, code: contracts.CodeHealthConnected},
		{name: "api key", listing: apiKeyListing, state: contracts.HealthOK, code: contracts.CodeHealthConnected},
		{name: "signed out", listing: signedOutList, state: contracts.HealthOff, code: contracts.CodeHealthNotLoggedIn, remedy: true},
		{name: "unreadable", listing: "something else\n", state: contracts.HealthDown, code: contracts.CodeHealthUnreachable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := runProbe(t, fakeGrok(t, test.listing, "exit 1"), false)
			if report.State != test.state || report.Detail.Code != test.code {
				t.Fatalf("report = %+v, want %s with %v", report, test.state, test.code)
			}
			if hasRemedy := report.Remedy.Code == contracts.CodeHealthRemedyRun; hasRemedy != test.remedy {
				t.Fatalf("remedy = %+v, want one: %v", report.Remedy, test.remedy)
			}
		})
	}
}

func TestProbeOfAMissingBinaryIsOff(t *testing.T) {
	report := runProbe(t, filepath.Join(t.TempDir(), "absent"), false)
	if report.State != contracts.HealthOff {
		t.Fatalf("report = %+v, want off", report)
	}
}

func TestDeepProbeSettlesTheLoginWithATurn(t *testing.T) {
	tests := []struct {
		name  string
		turn  string
		state contracts.HealthState
		code  contracts.Code
	}{
		{name: "answered", turn: "printf '%s' '" + answeredTurn + "'", state: contracts.HealthOK, code: contracts.CodeHealthConnected},
		{name: "refused key", turn: `echo 'Error: Not signed in.' >&2; printf '{"type":"error","message":"Not signed in."}'; exit 1`, state: contracts.HealthDown, code: contracts.CodeHealthCredentialsRefused},
		{name: "rate limited", turn: `printf '{"type":"error","message":"rate limit exceeded"}'; exit 1`, state: contracts.HealthDegraded, code: contracts.CodeHealthRateLimited},
		{name: "unanswered", turn: `printf '{"text":"","stopReason":"max_tokens"}'`, state: contracts.HealthDown, code: contracts.CodeHealthUnreachable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := runProbe(t, fakeGrok(t, apiKeyListing, test.turn), true)
			if report.State != test.state || report.Detail.Code != test.code {
				t.Fatalf("report = %+v, want %s with %v", report, test.state, test.code)
			}
		})
	}
}

func TestDeepProbeAsksForASingleTurnWithoutTools(t *testing.T) {
	record := filepath.Join(t.TempDir(), "argv")
	stub := fakeGrok(t, signedInListing, `printf '%s\n' "$@" > '`+record+`'
printf '%s' '`+answeredTurn+`'`)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	config := contracts.PluginConfig{Settings: map[string]string{settingCmd: stub, settingModel: "grok-4.6"}}
	if report := probe(ctx, contracts.ProbeAccess{Config: config, Deep: true}); report.State != contracts.HealthOK {
		t.Fatalf("report = %+v, want ok", report)
	}
	argv := readLines(t, record)
	if slices.Contains(argv, "--always-approve") || !slices.Contains(argv, "--max-turns") || !slices.Contains(argv, "grok-4.6") {
		t.Fatalf("argv = %q, want a one turn probe on the model setting that approves no tool", argv)
	}
}

func TestDiscoverListsTheModelsOfASignedInAccount(t *testing.T) {
	specs, err := discover(t.Context(), contracts.DiscoveryAccess{Getenv: envWith(fakeGrok(t, signedInListing, "exit 1"))})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, spec := range specs {
		ids = append(ids, spec.ID)
	}
	if !slices.Equal(ids, []string{"grok-4.7", "grok-4.7-build-fast"}) {
		t.Fatalf("ids = %q, want the two listed models once each", ids)
	}
	if _, err := contracts.MergeDiscovered(contracts.VendorGrok, Models(), specs); err != nil {
		t.Fatalf("discovered models do not merge into the catalogue: %v", err)
	}
}

func TestDiscoverReturnsNothingForASignedOutAccountOrAGatewayOnlyHost(t *testing.T) {
	signedOut, err := discover(t.Context(), contracts.DiscoveryAccess{Getenv: envWith(fakeGrok(t, signedOutList, "exit 1"))})
	if err != nil || signedOut != nil {
		t.Fatalf("signed out = %v, %v; want nothing", signedOut, err)
	}
	gatewayOnly, err := discover(t.Context(), contracts.DiscoveryAccess{Policy: contracts.PolicyGatewayOnly, Getenv: envWith("/nonexistent")})
	if err != nil || gatewayOnly != nil {
		t.Fatalf("gateway only = %v, %v; want nothing without running the CLI", gatewayOnly, err)
	}
}

func TestAccountEnvTakesTheProfileVariablesFromTheAccount(t *testing.T) {
	t.Setenv(contracts.EnvGrokHome, "/host/home")
	t.Setenv(xaiKeyVariable, "host-key")
	env := accountEnv(func(name string) string {
		if name == contracts.EnvGrokHome {
			return "/profile/home"
		}
		return ""
	})
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "GROK_HOME=/profile/home") || strings.Contains(joined, "/host/home") || strings.Contains(joined, "host-key") {
		t.Fatalf("env does not follow the account: %q", env)
	}
}

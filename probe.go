package grok

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"slices"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	deepProbePrompt        = "Reply with the single word OK."
	notSignedInText        = "not signed in"
	unauthorizedText       = "unauthorized"
	rateLimitText          = "rate limit"
	reasonTimeout          = "timeout"
	reasonCanceled         = "canceled"
	reasonCommandNotFound  = "command not found"
	reasonCommandFailed    = "command failed"
	reasonOutputTooLarge   = "output too large"
	reasonUnreadableStatus = "unreadable status"
	reasonTurnFailed       = "turn failed"
	reasonDirMissing       = "working directory missing"
)

type probeTarget struct {
	base  []string
	env   []string
	model string
	dir   string
}

func probe(ctx context.Context, access contracts.ProbeAccess) contracts.ProbeReport {
	target := probeTargetOf(access)
	started := time.Now()
	report := target.statusReport(ctx)
	if access.Deep && report.State == contracts.HealthOK {
		report = target.deepReport(ctx, report)
	}
	report.Latency = time.Since(started)
	return report
}

func probeTargetOf(access contracts.ProbeAccess) probeTarget {
	getenv := access.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	command := access.Config.Get(settingCmd)
	if command == "" {
		command = getenv(envCmd)
	}
	return probeTarget{base: baseCommand(command), env: accountEnv(getenv), model: access.Config.Get(settingModel), dir: access.Config.Get(settingDir)}
}

func (t probeTarget) statusReport(ctx context.Context) contracts.ProbeReport {
	output, err := runBounded(ctx, append(slices.Clone(t.base), "models"), "", t.env)
	if err != nil {
		return failureReport(ctx, err)
	}
	if output.stdout.exceeded {
		return unreachableReport(contracts.HealthDown, reasonOutputTooLarge)
	}
	switch parseLoginState(output.stdout.data.String()) {
	case loginSession, loginAPIKey:
		return connectedReport()
	case loginNone:
		return contracts.ProbeReport{State: contracts.HealthOff, Detail: contracts.Notice{Code: contracts.CodeHealthNotLoggedIn}, Remedy: t.loginRemedy()}
	case loginUnknown:
	}
	return unreachableReport(contracts.HealthDown, reasonUnreadableStatus)
}

func connectedReport() contracts.ProbeReport {
	return contracts.ProbeReport{State: contracts.HealthOK, Detail: contracts.Notice{Code: contracts.CodeHealthConnected}}
}

type deepAnswer struct {
	Type       string `json:"type"`
	StopReason string `json:"stopReason"`
}

func (t probeTarget) deepReport(ctx context.Context, signedIn contracts.ProbeReport) contracts.ProbeReport {
	if t.dir != "" {
		if _, err := os.Stat(t.dir); err != nil {
			return unreachableReport(contracts.HealthDown, reasonDirMissing)
		}
	}
	argv := append(slices.Clone(t.base), "-p", deepProbePrompt, "--output-format", "json", "--no-auto-update", "--max-turns", "1")
	if t.model != "" {
		argv = append(argv, "--model", t.model)
	}
	output, err := runBounded(ctx, argv, t.dir, t.env)
	if err == nil && isAnsweredTurn(output) {
		return signedIn
	}
	switch {
	case output.mentions(notSignedInText), output.mentions(unauthorizedText):
		return contracts.ProbeReport{State: contracts.HealthDown, Detail: contracts.Notice{Code: contracts.CodeHealthCredentialsRefused}, Remedy: t.loginRemedy()}
	case output.mentions(rateLimitText):
		return contracts.ProbeReport{State: contracts.HealthDegraded, Detail: contracts.Notice{Code: contracts.CodeHealthRateLimited}}
	case err != nil:
		return failureReport(ctx, err)
	case output.stdout.exceeded:
		return unreachableReport(contracts.HealthDown, reasonOutputTooLarge)
	}
	return unreachableReport(contracts.HealthDown, reasonTurnFailed)
}

func isAnsweredTurn(output *boundedOutput) bool {
	var answer deepAnswer
	if output.stdout.exceeded || json.Unmarshal(output.stdout.data.Bytes(), &answer) != nil {
		return false
	}
	return answer.Type != eventError && answer.StopReason == stopEndTurn
}

func failureReport(ctx context.Context, err error) contracts.ProbeReport {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return unreachableReport(contracts.HealthDegraded, reasonTimeout)
	case ctx.Err() != nil:
		return unreachableReport(contracts.HealthDegraded, reasonCanceled)
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return unreachableReport(contracts.HealthOff, reasonCommandNotFound)
	}
	if exited, isExit := errors.AsType[*exec.ExitError](err); isExit {
		return unreachableReport(contracts.HealthDown, exited.String())
	}
	return unreachableReport(contracts.HealthDown, reasonCommandFailed)
}

func unreachableReport(state contracts.HealthState, reason string) contracts.ProbeReport {
	return contracts.ProbeReport{
		State:  state,
		Detail: contracts.Notice{Code: contracts.CodeHealthUnreachable, Args: []contracts.Arg{contracts.Text("Reason", reason)}},
	}
}

func (t probeTarget) loginRemedy() contracts.Notice {
	return contracts.Notice{Code: contracts.CodeHealthRemedyRun, Args: []contracts.Arg{contracts.Text("Command", t.base[0]+" login")}}
}

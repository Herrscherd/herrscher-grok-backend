// Package grok runs the Grok Build CLI as a herrscher backend.
package grok

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// Config configures a Grok Build backend.
type Config struct {
	Cmd      string
	Model    string
	Effort   string
	Dir      string
	Verbose  bool
	ResumeID string
}

const (
	defaultCmd       = "grok"
	streamFormat     = "streaming-json"
	maxStreamLine    = 16 << 20
	envFolderTrust   = "GROK_FOLDER_TRUST"
	folderTrustOff   = "0"
	errorExitMessage = "grok turn failed"
)

// NewBackend builds a configured Grok Build backend. Config.Cmd is split on
// spaces, and an empty Cmd runs grok. It does not start Grok.
func NewBackend(_ context.Context, config Config) (contracts.Backend, error) {
	return &streamResponder{
		base:        baseCommand(config.Cmd),
		model:       config.Model,
		effort:      config.Effort,
		dir:         config.Dir,
		stderr:      diagnosticsOutput(config),
		drain:       drainPipe,
		setDeadline: setReadDeadline,
		session:     config.ResumeID,
	}, nil
}

func diagnosticsOutput(config Config) io.Writer {
	if config.Verbose {
		return os.Stderr
	}
	return nil
}

type streamResponder struct {
	base        []string
	model       string
	effort      string
	dir         string
	stderr      io.Writer
	drain       func(io.Reader) error
	setDeadline stdoutDeadline
	turns       sync.Mutex
	mu          sync.Mutex
	session     string
}

var _ contracts.ResumeAware = (*streamResponder)(nil)

func (r *streamResponder) Respond(ctx context.Context, prompt contracts.Prompt, onEvent func(contracts.BackendEvent)) (string, error) {
	r.turns.Lock()
	defer r.turns.Unlock()
	resume := r.ResumeToken()
	turn, err := r.runTurn(ctx, prompt, resume, onEvent)
	if resume != "" && turn.isResumeRefused() {
		r.dropSession(resume)
		emit(onEvent, contracts.BackendEvent{Kind: contracts.BackendKindNotice, Notice: &contracts.Notice{Code: contracts.CodeResumeLost}})
		turn, err = r.runTurn(ctx, prompt, "", onEvent)
	}
	r.setSession(turn.SessionID)
	return turn.Text, err
}

func (r *streamResponder) runTurn(ctx context.Context, prompt contracts.Prompt, resume string, onEvent func(contracts.BackendEvent)) (turnResult, error) {
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	argv := turnArgv(r.base, turnFlags{model: r.model, effort: r.effort, resume: resume})
	proc, err := startTurn(runCtx, argv, r.dir, turnInput(prompt), r.stderr, r.setDeadline)
	if err != nil {
		return turnResult{}, fmt.Errorf("grok start: %w", err)
	}
	turn, readErr := readOutput(proc, stop, func(out io.Reader) (turnResult, error) {
		return readTurn(bufio.NewReader(out), onEvent)
	})
	drainErr := r.drainOutput(proc, stop)
	waitErr := errors.Join(proc.wait(), proc.closeStdout())
	return turn, streamOutcome(turn, readErr, drainErr, waitErr)
}

func (r *streamResponder) drainOutput(proc *turnProcess, stop context.CancelFunc) error {
	_, err := readOutput(proc, stop, func(pipe io.Reader) (struct{}, error) {
		return struct{}{}, r.drain(pipe)
	})
	return ignoreOwnCut(err)
}

func streamOutcome(turn turnResult, readErr, drainErr, waitErr error) error {
	if readErr == nil {
		readErr = drainErr
	}
	switch {
	case readErr != nil && waitErr != nil:
		return fmt.Errorf("grok stream: %w (exit: %w)", readErr, waitErr)
	case readErr != nil:
		return fmt.Errorf("grok stream: %w", readErr)
	case turn.Failure != nil:
		return turn.Failure
	case waitErr != nil:
		return fmt.Errorf("grok turn: %w", waitErr)
	}
	return nil
}

func (r *streamResponder) ResumeToken() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.session
}

func (r *streamResponder) setSession(sessionID string) {
	if sessionID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.session = sessionID
}

func (r *streamResponder) dropSession(refused string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session == refused {
		r.session = ""
	}
}

func (r *streamResponder) Close() error { return nil }

// TurnFailedError reports a turn that Grok ended without success: an error
// event, or an end event whose stop reason is not end_turn. StopReason is empty
// for an error event, and Message is the text Grok gave, empty when it gave none.
type TurnFailedError struct {
	StopReason string
	Message    string
}

func (e *TurnFailedError) Error() string {
	switch {
	case e.StopReason != "" && e.Message != "":
		return errorExitMessage + " (" + e.StopReason + "): " + e.Message
	case e.StopReason != "":
		return errorExitMessage + " (" + e.StopReason + ")"
	}
	return errorExitMessage + ": " + e.Message
}

func turnInput(p contracts.Prompt) string {
	return withContext(p.Context, withAttachments(p.Content, p.Attachments))
}

func turnCommand(ctx context.Context, argv []string, dir string, stderr io.Writer) *exec.Cmd {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	configureChild(cmd)
	cmd.Dir = dir
	cmd.Stderr = stderr
	cmd.Env = childEnv(os.Environ())
	return cmd
}

func childEnv(parent []string) []string {
	return append(slices.Clone(parent), envFolderTrust+"="+folderTrustOff)
}

func baseCommand(cmd string) []string {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return []string{defaultCmd}
	}
	return fields
}

type turnFlags struct {
	model  string
	effort string
	resume string
}

func turnArgv(base []string, flags turnFlags) []string {
	argv := slices.Clone(base)
	argv = append(argv, "-p", "--output-format", streamFormat, "--always-approve", "--no-auto-update")
	if flags.model != "" {
		argv = append(argv, "--model", flags.model)
	}
	if flags.effort != "" {
		argv = append(argv, "--reasoning-effort", flags.effort)
	}
	if flags.resume != "" {
		argv = append(argv, "--resume", flags.resume)
	}
	return argv
}

func emit(onEvent func(contracts.BackendEvent), event contracts.BackendEvent) {
	if onEvent != nil {
		onEvent(event)
	}
}

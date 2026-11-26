//go:build !unix

package grok

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
)

func configureChild(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		return killGroup(cmd.Process)
	}
}

func killGroup(process *os.Process) error {
	treeErr := exec.CommandContext(context.Background(), "taskkill", "/T", "/F", "/PID", strconv.Itoa(process.Pid)).Run()
	if treeErr == nil {
		return nil
	}
	return killAfterTreeFailure(process, treeErr)
}

func killAfterTreeFailure(process *os.Process, treeErr error) error {
	slog.Warn("taskkill failed, killing the grok process only", "error", treeErr)
	err := process.Kill()
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

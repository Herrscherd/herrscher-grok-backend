package grok

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// ErrInvalidSetting reports a setting whose value the factory cannot read.
var ErrInvalidSetting = errors.New("invalid grok setting")

const (
	settingCmd    = "cmd"
	settingModel  = "model"
	settingEffort = "effort"
	settingDir    = "dir"
	settingResume = "resume"
	envCmd        = "GROK_CMD"
)

func init() {
	contracts.Register(contracts.Plugin{
		Manifest: contracts.Manifest{
			Kind:     contracts.VendorGrok,
			Category: contracts.CategoryBackend,
			Status:   contracts.StatusLive,
			Config: []contracts.Setting{
				{Key: settingCmd, Env: envCmd, Help: "base command to run Grok Build", Default: defaultCmd},
				{Key: settingModel, Env: "GROK_MODEL", Help: "model override"},
				{Key: settingEffort, Env: "GROK_EFFORT", Help: "reasoning effort override: low, medium, high or xhigh"},
				{Key: settingDir, Env: "GROK_DIR", Help: "working directory"},
				{Key: settingResume, Help: "session id to resume, fed by the host at construction, never set by an operator"},
			},
			Models:       Models(),
			Capabilities: contracts.Capabilities{Gate: contracts.GrainTool},
		},
		Discover: discover,
		Probe:    probe,
		Backend: func(ctx context.Context, cfg contracts.PluginConfig) (contracts.Backend, error) {
			effort, err := effortSetting(cfg)
			if err != nil {
				return nil, err
			}
			return NewBackend(ctx, Config{
				Cmd:      cfg.Get(settingCmd),
				Model:    cfg.Get(settingModel),
				Effort:   effort,
				Dir:      cfg.Get(settingDir),
				ResumeID: cfg.Get(settingResume),
			})
		},
	})
}

func effortSetting(cfg contracts.PluginConfig) (string, error) {
	effort := strings.TrimSpace(cfg.Get(settingEffort))
	if effort == "" || slices.Contains(Efforts(), effort) {
		return effort, nil
	}
	return "", fmt.Errorf("%w: effort=%q, want one of %s", ErrInvalidSetting, effort, strings.Join(Efforts(), ", "))
}

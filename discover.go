package grok

import (
	"context"
	"fmt"
	"slices"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// OutputTooLargeError reports a CLI listing or status longer than Limit bytes.
type OutputTooLargeError struct {
	Limit int
}

func (e *OutputTooLargeError) Error() string {
	return fmt.Sprintf("grok output is longer than %d bytes", e.Limit)
}

func discover(ctx context.Context, access contracts.DiscoveryAccess) ([]contracts.ModelSpec, error) {
	if !access.Policy.Allows(contracts.RouteNative) {
		return nil, nil
	}
	getenv := access.Getenv
	command := ""
	if getenv != nil {
		command = strings.TrimSpace(getenv(envCmd))
	}
	output, runErr := runBounded(ctx, append(slices.Clone(baseCommand(command)), "models"), "", accountEnv(getenv))
	if runErr != nil {
		return nil, fmt.Errorf("grok models: %w", runErr)
	}
	if output.stdout.exceeded {
		return nil, &OutputTooLargeError{Limit: boundedOutputMax}
	}
	listing := output.stdout.data.String()
	if parseLoginState(listing) == loginNone {
		return nil, nil
	}
	return parseModelsOutput(listing), nil
}

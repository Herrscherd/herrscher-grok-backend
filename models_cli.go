package grok

import (
	"bytes"
	"context"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	boundedWaitDelay  = 2 * time.Second
	boundedOutputMax  = 256 << 10
	modelsHeading     = "available models:"
	signedInPrefix    = "you are logged in"
	apiKeyPrefix      = "you are using xai_api_key"
	notSignedInPrefix = "you are not authenticated"
	xaiKeyVariable    = "XAI_API_KEY"
	envAPIBaseURL     = "GROK_XAI_API_BASE_URL"
)

var modelIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type loginState uint8

const (
	loginUnknown loginState = iota
	loginSession
	loginAPIKey
	loginNone
)

type cappedBuffer struct {
	data     bytes.Buffer
	limit    int
	exceeded bool
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	accepted := min(len(data), max(b.limit-b.data.Len(), 0))
	if accepted < len(data) {
		b.exceeded = true
	}
	if _, err := b.data.Write(data[:accepted]); err != nil {
		return 0, err
	}
	return len(data), nil
}

type boundedOutput struct {
	stdout cappedBuffer
	stderr cappedBuffer
}

func runBounded(ctx context.Context, argv []string, dir string, env []string) (*boundedOutput, error) {
	return runBoundedWithin(ctx, argv, dir, env, boundedOutputMax)
}

func runBoundedWithin(ctx context.Context, argv []string, dir string, env []string, limit int) (*boundedOutput, error) {
	output := &boundedOutput{stdout: cappedBuffer{limit: limit}, stderr: cappedBuffer{limit: boundedOutputMax}}
	cmd := turnCommand(ctx, childSpec{argv: argv, dir: dir, env: env, stderr: &output.stderr})
	cmd.Stdout = &output.stdout
	cmd.WaitDelay = boundedWaitDelay
	return output, cmd.Run()
}

func (o *boundedOutput) mentions(lowerText string) bool {
	return strings.Contains(strings.ToLower(o.stdout.data.String()), lowerText) ||
		strings.Contains(strings.ToLower(o.stderr.data.String()), lowerText)
}

func accountEnv(getenv func(string) string) []string {
	names := []string{contracts.EnvGrokHome, xaiKeyVariable, envAPIBaseURL}
	env := slices.DeleteFunc(os.Environ(), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		return slices.Contains(names, name)
	})
	if getenv == nil {
		getenv = os.Getenv
	}
	for _, name := range names {
		if value := getenv(name); value != "" {
			env = append(env, name+"="+value)
		}
	}
	return env
}

func parseLoginState(output string) loginState {
	for line := range strings.Lines(stripANSI(output)) {
		text := strings.ToLower(strings.TrimSpace(line))
		switch {
		case text == "":
			continue
		case strings.HasPrefix(text, signedInPrefix):
			return loginSession
		case strings.HasPrefix(text, apiKeyPrefix):
			return loginAPIKey
		case strings.HasPrefix(text, notSignedInPrefix):
			return loginNone
		}
		return loginUnknown
	}
	return loginUnknown
}

func parseModelsOutput(output string) []contracts.ModelSpec {
	var specs []contracts.ModelSpec
	seen := make(map[string]bool)
	listing := false
	for line := range strings.Lines(stripANSI(output)) {
		text := strings.TrimSpace(line)
		if strings.EqualFold(text, modelsHeading) {
			listing = true
			continue
		}
		modelID, ok := listedModel(text)
		if !listing || !ok || seen[modelID] {
			continue
		}
		seen[modelID] = true
		specs = append(specs, contracts.ModelSpec{ID: modelID, Label: modelID, Arg: modelID, Route: contracts.RouteNative})
	}
	return specs
}

func listedModel(line string) (string, bool) {
	rest, marked := strings.CutPrefix(line, "* ")
	if !marked {
		rest, marked = strings.CutPrefix(line, "- ")
	}
	if !marked {
		return "", false
	}
	modelID, marker, hasMarker := strings.Cut(strings.TrimSpace(rest), " ")
	if hasMarker && !isParenthesised(strings.TrimSpace(marker)) {
		return "", false
	}
	if !modelIDPattern.MatchString(modelID) {
		return "", false
	}
	return modelID, true
}

func isParenthesised(text string) bool {
	return strings.HasPrefix(text, "(") && strings.HasSuffix(text, ")")
}

func stripANSI(text string) string {
	kept := make([]rune, 0, len(text))
	inEscape := false
	for _, char := range text {
		switch {
		case char == '\x1b':
			inEscape = true
		case inEscape && ((char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z')):
			inEscape = false
		case !inEscape:
			kept = append(kept, char)
		}
	}
	return string(kept)
}

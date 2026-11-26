package grok

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	eventText     = "text"
	eventThought  = "thought"
	eventToolCall = "tool_call"
	eventToolEdit = "tool_call_update"
	eventPlan     = "plan"
	eventUsage    = "usage"
	eventEnd      = "end"
	eventError    = "error"

	backendKindText     = "text"
	backendKindThinking = "thinking"
	backendKindTodos    = "todos"
	backendKindUsage    = "usage"
	backendKindResult   = "result"

	stopEndTurn        = "end_turn"
	resumeMissingText  = "no saved copy of session"
	textBlockSeparator = "\n\n"
	maxReplyBytes      = maxStreamLine
	maxThoughtBytes    = 1 << 20
)

type grokEvent struct {
	Type         string                     `json:"type"`
	Data         string                     `json:"data"`
	Message      string                     `json:"message"`
	ToolCallID   string                     `json:"toolCallId"`
	ToolName     string                     `json:"toolName"`
	Status       string                     `json:"status"`
	RawInput     map[string]json.RawMessage `json:"rawInput"`
	RawOutput    json.RawMessage            `json:"rawOutput"`
	Content      []toolContent              `json:"content"`
	Entries      []planEntry                `json:"entries"`
	Usage        tokenUsage                 `json:"usage"`
	StopReason   string                     `json:"stopReason"`
	SessionID    string                     `json:"sessionId"`
	Error        json.RawMessage            `json:"error"`
	TotalCostUSD float64                    `json:"total_cost_usd"`
	ModelUsage   map[string]modelUsage      `json:"modelUsage"`
}

type tokenUsage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
}

func (u tokenUsage) isEmpty() bool {
	return u == tokenUsage{}
}

type modelUsage struct {
	InputTokens         int     `json:"inputTokens"`
	OutputTokens        int     `json:"outputTokens"`
	CacheReadTokens     int     `json:"cacheReadInputTokens"`
	CacheCreationTokens int     `json:"cacheCreationInputTokens"`
	CostUSD             float64 `json:"costUSD"`
}

type planEntry struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

type toolContent struct {
	Content struct {
		Text string `json:"text"`
	} `json:"content"`
}

// LineTooLongError reports a line of the stream longer than the reader accepts.
type LineTooLongError struct {
	Limit int
}

func (e *LineTooLongError) Error() string {
	return fmt.Sprintf("grok stream line is longer than %d bytes", e.Limit)
}

// ReplyTooLongError reports a turn whose reply text grows past Limit bytes. The
// turn is stopped there and its text is not returned.
type ReplyTooLongError struct {
	Limit int
}

func (e *ReplyTooLongError) Error() string {
	return fmt.Sprintf("grok reply is longer than %d bytes", e.Limit)
}

// StreamEndedError reports that the stream ended before its end or error event.
// Unparsable counts the non-empty lines that were not JSON, and Cause is the
// read error that ended the stream.
type StreamEndedError struct {
	Unparsable int
	Cause      error
}

func (e *StreamEndedError) Error() string {
	return fmt.Sprintf("grok stream ended after %d unparsable line(s): %v", e.Unparsable, e.Cause)
}

func (e *StreamEndedError) Unwrap() error {
	return e.Cause
}

type turnResult struct {
	Text      string
	SessionID string
	Failure   *TurnFailedError
	Forwarded bool
}

func (t turnResult) isResumeRefused() bool {
	if t.Failure == nil || t.Failure.StopReason != "" || t.Forwarded {
		return false
	}
	return strings.Contains(strings.ToLower(t.Failure.Message), resumeMissingText)
}

type turnState struct {
	onEvent    func(contracts.BackendEvent)
	forwarded  bool
	unparsable int
	text       []byte
	thought    []byte
	reply      []byte
	tools      toolCallTracker
}

func (s *turnState) forward(event contracts.BackendEvent) {
	s.forwarded = true
	emit(s.onEvent, event)
}

func readTurn(reader *bufio.Reader, onEvent func(contracts.BackendEvent)) (turnResult, error) {
	state := &turnState{onEvent: onEvent}
	for {
		line, err := readLine(reader)
		if _, isTooLong := errors.AsType[*LineTooLongError](err); isTooLong {
			return state.partial(), err
		}
		outcome, consumeErr := state.consume(line)
		switch {
		case consumeErr != nil:
			return turnResult{Forwarded: state.forwarded}, consumeErr
		case outcome.isDone:
			return outcome.turn, nil
		case err != nil:
			state.flushBlocks()
			return state.partial(), &StreamEndedError{Unparsable: state.unparsable, Cause: err}
		}
	}
}

func readLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(line)+len(chunk) > maxStreamLine {
			return nil, &LineTooLongError{Limit: maxStreamLine}
		}
		line = append(line, chunk...)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}

type lineOutcome struct {
	turn   turnResult
	isDone bool
}

func (s *turnState) consume(line []byte) (lineOutcome, error) {
	if len(bytes.TrimSpace(line)) == 0 {
		return lineOutcome{}, nil
	}
	event, isEvent := decodeEvent(line)
	if !isEvent {
		s.unparsable++
		return lineOutcome{}, nil
	}
	return s.apply(&event)
}

func decodeEvent(line []byte) (grokEvent, bool) {
	var event grokEvent
	isEvent := json.Unmarshal(line, &event) == nil
	return event, isEvent
}

func (s *turnState) apply(event *grokEvent) (lineOutcome, error) {
	switch event.Type {
	case eventText:
		s.flushThought()
		return lineOutcome{}, s.appendText(event.Data)
	case eventThought:
		err := s.flushText()
		s.appendThought(event.Data)
		return lineOutcome{}, err
	}
	if err := s.flushText(); err != nil {
		return lineOutcome{}, err
	}
	s.flushThought()
	switch event.Type {
	case eventToolCall:
		s.tools.start(event, s.forward)
	case eventToolEdit:
		s.tools.update(event, s.forward)
	case eventPlan:
		s.forward(contracts.BackendEvent{Kind: backendKindTodos, Todos: todoItems(event.Entries)})
	case eventUsage:
		s.emitUsage(event.Usage)
	case eventEnd:
		return lineOutcome{turn: s.finish(event), isDone: true}, nil
	case eventError:
		return lineOutcome{turn: s.fail(event), isDone: true}, nil
	}
	return lineOutcome{}, nil
}

func (s *turnState) appendText(chunk string) error {
	if len(s.reply)+len(textBlockSeparator)+len(s.text)+len(chunk) > maxReplyBytes {
		return &ReplyTooLongError{Limit: maxReplyBytes}
	}
	s.text = append(s.text, chunk...)
	return nil
}

func (s *turnState) flushText() error {
	block := string(s.text)
	s.text = s.text[:0]
	if strings.TrimSpace(block) == "" {
		return nil
	}
	if len(s.reply) > 0 {
		block = textBlockSeparator + block
	}
	if len(s.reply)+len(block) > maxReplyBytes {
		return &ReplyTooLongError{Limit: maxReplyBytes}
	}
	s.reply = append(s.reply, block...)
	s.forward(contracts.BackendEvent{Kind: backendKindText, Detail: block})
	return nil
}

func (s *turnState) appendThought(chunk string) {
	s.thought = append(s.thought, chunk...)
	if len(s.thought) >= maxThoughtBytes {
		s.flushThought()
	}
}

func (s *turnState) flushThought() {
	block := string(s.thought)
	s.thought = s.thought[:0]
	if strings.TrimSpace(block) != "" {
		s.forward(contracts.BackendEvent{Kind: backendKindThinking, Detail: block})
	}
}

func (s *turnState) flushBlocks() {
	if s.flushText() != nil {
		s.text = s.text[:0]
	}
	s.flushThought()
}

func (s *turnState) emitUsage(usage tokenUsage) {
	if usage.isEmpty() {
		return
	}
	s.forward(contracts.BackendEvent{
		Kind:        backendKindUsage,
		InTokens:    usage.InputTokens,
		OutTokens:   usage.OutputTokens,
		CacheRead:   usage.CacheReadTokens,
		CacheCreate: usage.CacheCreationTokens,
	})
}

func (s *turnState) finish(event *grokEvent) turnResult {
	turn := s.partial()
	turn.SessionID = event.SessionID
	isError := event.StopReason != stopEndTurn
	if isError {
		turn.Failure = &TurnFailedError{StopReason: cmp.Or(event.StopReason, "unknown"), Message: errorText(event.Error)}
	}
	s.forward(contracts.BackendEvent{
		Kind:        backendKindResult,
		IsError:     isError,
		Cost:        event.TotalCostUSD,
		InTokens:    event.Usage.InputTokens,
		OutTokens:   event.Usage.OutputTokens,
		CacheRead:   event.Usage.CacheReadTokens,
		CacheCreate: event.Usage.CacheCreationTokens,
		Models:      turnModels(event.ModelUsage),
	})
	turn.Forwarded = s.forwarded
	return turn
}

func (s *turnState) fail(event *grokEvent) turnResult {
	turn := s.partial()
	turn.SessionID = event.SessionID
	turn.Failure = &TurnFailedError{Message: event.Message}
	return turn
}

func (s *turnState) partial() turnResult {
	return turnResult{Text: string(s.reply), Forwarded: s.forwarded}
}

func errorText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return ""
	}
	return text
}

func todoItems(entries []planEntry) []contracts.TodoItem {
	items := make([]contracts.TodoItem, 0, len(entries))
	for _, entry := range entries {
		items = append(items, contracts.TodoItem{Text: entry.Content, State: entry.Status})
	}
	return items
}

func turnModels(usage map[string]modelUsage) []contracts.ModelUsage {
	if len(usage) == 0 {
		return nil
	}
	models := make([]contracts.ModelUsage, 0, len(usage))
	for name, model := range usage {
		models = append(models, contracts.ModelUsage{
			Model:       name,
			In:          model.InputTokens,
			Out:         model.OutputTokens,
			CacheRead:   model.CacheReadTokens,
			CacheCreate: model.CacheCreationTokens,
			Cost:        model.CostUSD,
		})
	}
	slices.SortFunc(models, func(left, right contracts.ModelUsage) int {
		return strings.Compare(left.Model, right.Model)
	})
	return models
}

func drainPipe(pipe io.Reader) error {
	_, err := io.Copy(io.Discard, pipe)
	return err
}

package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeProvider replays a scripted sequence of answers and records the
// conversation it was given on each call.
type fakeProvider struct {
	answers []Response
	errs    []error
	calls   [][]Message
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Stream(_ context.Context, req Request, sink Sink) (Response, error) {
	index := len(f.calls)
	f.calls = append(f.calls, req.Messages)

	if index < len(f.errs) && f.errs[index] != nil {
		return Response{}, f.errs[index]
	}
	response := f.answers[min(index, len(f.answers)-1)]
	if response.Reasoning != "" {
		sink.Emit(Delta{Reasoning: response.Reasoning})
	}
	if response.Content != "" {
		sink.Emit(Delta{Content: response.Content})
	}
	return response, nil
}

func TestStreamWithRepairRecoversAnEmptyAnswer(t *testing.T) {
	provider := &fakeProvider{answers: []Response{
		{Reasoning: "the answer is 42, and I will now stop"},
		{Content: `{"value":42}`},
	}}

	var restarts int
	var content strings.Builder
	response, err := StreamWithRepair(context.Background(), provider, Request{
		Messages: []Message{User("what is the value?")},
		Format:   FormatJSON,
	}, nil, 3, func(delta Delta) {
		if delta.Restart {
			restarts++
			content.Reset()
		}
		content.WriteString(delta.Content)
	})
	if err != nil {
		t.Fatalf("StreamWithRepair returned error: %v", err)
	}

	if response.Content != `{"value":42}` || response.Attempts != 2 {
		t.Fatalf("content=%q attempts=%d", response.Content, response.Attempts)
	}
	// The caller must be told to discard the first, empty attempt.
	if restarts != 1 || content.String() != `{"value":42}` {
		t.Fatalf("restarts=%d streamed=%q", restarts, content.String())
	}
	// The retry tells the model it answered inside its reasoning.
	retry := provider.calls[1]
	if !strings.Contains(retry[len(retry)-1].Content, "not your reasoning") {
		t.Fatalf("retry instruction = %q", retry[len(retry)-1].Content)
	}
}

func TestStreamWithRepairSalvagesFencedJSONWithoutRetrying(t *testing.T) {
	provider := &fakeProvider{answers: []Response{
		{Content: "Here you go:\n```json\n{\"topics\":[\"Pork\"]}\n```"},
	}}

	response, err := StreamWithRepair(context.Background(), provider, Request{
		Messages: []Message{User("topics")},
		Format:   FormatJSON,
	}, nil, 3, nil)
	if err != nil {
		t.Fatalf("StreamWithRepair returned error: %v", err)
	}
	if response.Content != `{"topics":["Pork"]}` {
		t.Fatalf("content = %q, want the salvaged JSON", response.Content)
	}
	if response.Attempts != 1 {
		t.Fatalf("attempts = %d, want the fence repaired locally", response.Attempts)
	}
}

func TestStreamWithRepairFeedsValidationFailureBackToTheModel(t *testing.T) {
	provider := &fakeProvider{answers: []Response{
		{Content: `{"instructions":[]}`},
		{Content: `{"instructions":["tighten the lede"]}`},
	}}

	validate := func(content string) error {
		if strings.Contains(content, `"instructions":[]`) {
			return errors.New("the plan must contain at least one instruction")
		}
		return nil
	}

	response, err := StreamWithRepair(context.Background(), provider, Request{
		Messages: []Message{User("plan")},
		Format:   FormatJSON,
	}, validate, 3, nil)
	if err != nil {
		t.Fatalf("StreamWithRepair returned error: %v", err)
	}
	if response.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", response.Attempts)
	}

	retry := provider.calls[1]
	if retry[len(retry)-2].Role != RoleAssistant {
		t.Fatalf("rejected answer was not replayed to the model: %+v", retry)
	}
	if !strings.Contains(retry[len(retry)-1].Content, "at least one instruction") {
		t.Fatalf("retry did not name the violation: %q", retry[len(retry)-1].Content)
	}
	// The caller's message slice must not be mutated by the retry.
	if len(provider.calls[0]) != 1 {
		t.Fatalf("first call was mutated: %+v", provider.calls[0])
	}
}

func TestStreamWithRepairGivesUpAfterAttempts(t *testing.T) {
	provider := &fakeProvider{answers: []Response{{Content: "not json at all"}}}

	_, err := StreamWithRepair(context.Background(), provider, Request{
		Messages: []Message{User("plan")},
		Format:   FormatJSON,
	}, nil, 2, nil)
	if err == nil || !strings.Contains(err.Error(), "after 2 attempts") {
		t.Fatalf("error = %v, want exhaustion after 2 attempts", err)
	}
	if len(provider.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(provider.calls))
	}
}

func TestStreamWithRepairDoesNotRetryTransportFailures(t *testing.T) {
	provider := &fakeProvider{
		answers: []Response{{Content: "unused"}},
		errs:    []error{errors.New("connection refused")},
	}

	_, err := StreamWithRepair(context.Background(), provider, Request{
		Messages: []Message{User("plan")},
	}, nil, 3, nil)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("error = %v", err)
	}
	// Re-asking cannot fix a dead connection, and doing so burns the budget.
	if len(provider.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(provider.calls))
	}
}

func TestStreamWithRepairAccumulatesUsageAcrossAttempts(t *testing.T) {
	provider := &fakeProvider{answers: []Response{
		{Content: "nope", Usage: Usage{PromptTokens: 10, ResponseTokens: 5}},
		{Content: `{}`, Usage: Usage{PromptTokens: 20, ResponseTokens: 3}},
	}}

	response, err := StreamWithRepair(context.Background(), provider, Request{
		Messages: []Message{User("plan")},
		Format:   FormatJSON,
	}, nil, 3, nil)
	if err != nil {
		t.Fatalf("StreamWithRepair returned error: %v", err)
	}
	if response.Usage.Total() != 38 {
		t.Fatalf("usage = %+v, want every attempt billed", response.Usage)
	}
}

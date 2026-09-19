package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
)

// DefaultAttempts is the number of provider calls one logical request may make.
const DefaultAttempts = 3

// Validator reports whether a model answer is acceptable. The returned error
// is fed back to the model as the repair instruction, so it should describe
// the violation in terms the model can act on.
type Validator func(content string) error

// ErrEmptyAnswer is returned when a call produced no user-visible content.
//
// This is the characteristic reasoning-model failure: the model finishes its
// work inside the reasoning trace and emits an empty or truncated answer.
var ErrEmptyAnswer = errors.New("model produced no answer outside its reasoning trace")

// StreamWithRepair performs req, validates the answer, and re-asks the model
// with the violation appended when validation fails.
//
// Each retry emits Delta{Restart: true} first, so a caller streaming to a UI
// knows to discard what it has shown. attempts <= 0 uses DefaultAttempts.
func StreamWithRepair(ctx context.Context, provider Provider, req Request, validate Validator, attempts int, sink Sink) (Response, error) {
	if provider == nil {
		return Response{}, errors.New("llm: provider is not configured")
	}
	if attempts <= 0 {
		attempts = DefaultAttempts
	}

	var total Usage
	messages := req.Messages
	var lastErr error

	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			sink.Emit(Delta{Restart: true})
		}

		call := req
		call.Messages = messages
		response, err := provider.Stream(ctx, call, sink)
		total.Add(response.Usage)

		// A transport or context failure is not repairable by re-asking.
		if err != nil {
			response.Usage = total
			response.Attempts = attempt
			return response, err
		}

		content := strings.TrimSpace(response.Content)
		if req.Format == FormatJSON || len(req.Schema) > 0 {
			if salvaged, ok := extractJSON(content); ok {
				content = salvaged
			}
		}

		lastErr = checkAnswer(content, req, validate)
		if lastErr == nil {
			response.Content = content
			response.Usage = total
			response.Attempts = attempt
			return response, nil
		}

		log.Printf("[llm] %s attempt %d/%d rejected: %v", provider.Name(), attempt, attempts, lastErr)
		if attempt < attempts {
			messages = appendRepairTurn(req.Messages, response.Content, lastErr)
		}
	}

	return Response{Usage: total, Attempts: attempts},
		fmt.Errorf("llm: answer still invalid after %d attempts: %w", attempts, lastErr)
}

func checkAnswer(content string, req Request, validate Validator) error {
	if content == "" {
		return ErrEmptyAnswer
	}
	if (req.Format == FormatJSON || len(req.Schema) > 0) && !json.Valid([]byte(content)) {
		return errors.New("answer is not valid JSON")
	}
	if validate != nil {
		return validate(content)
	}
	return nil
}

// appendRepairTurn builds the follow-up conversation: the rejected answer,
// then an instruction naming the violation.
func appendRepairTurn(base []Message, rejected string, reason error) []Message {
	instruction := fmt.Sprintf(
		"That response was rejected: %v. Reply again with the corrected response only, and nothing else.",
		reason,
	)
	if errors.Is(reason, ErrEmptyAnswer) {
		instruction = "You did not provide an answer. Reply with the answer itself, not your reasoning about it."
	}

	// Copy so retries never alias the caller's slice.
	turn := make([]Message, 0, len(base)+2)
	turn = append(turn, base...)
	if strings.TrimSpace(rejected) != "" {
		turn = append(turn, Message{Role: RoleAssistant, Content: rejected})
	}
	return append(turn, User(instruction))
}

// extractJSON salvages a JSON value wrapped in code fences or commentary,
// which is cheaper than spending a retry on a formatting slip.
func extractJSON(content string) (string, bool) {
	if json.Valid([]byte(content)) {
		return content, false
	}

	start := strings.IndexAny(content, "{[")
	if start < 0 {
		return content, false
	}
	open := rune(content[start])
	close := '}'
	if open == '[' {
		close = ']'
	}
	end := strings.LastIndexFunc(content, func(r rune) bool { return r == close })
	if end <= start {
		return content, false
	}

	candidate := strings.TrimSpace(content[start : end+1])
	if !json.Valid([]byte(candidate)) {
		return content, false
	}
	return candidate, true
}

// Package llm is the provider boundary for every model call in the
// application. Nothing above this package knows whether generation happens on
// a local Ollama instance or through a hosted OpenAI-compatible API.
//
// The boundary exists because reasoning models emit two distinct streams: the
// reasoning trace and the user-visible answer. Concatenating them puts
// "<think>..." into article prose and into json.Unmarshal, so Delta keeps them
// apart all the way to the caller.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Role identifies the author of a Message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one turn of a chat conversation.
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

// System and User build the two message kinds the agents need today.
func System(content string) Message { return Message{Role: RoleSystem, Content: content} }
func User(content string) Message   { return Message{Role: RoleUser, Content: content} }

// ThinkLevel requests a reasoning trace. Providers that cannot honour the
// request degrade to ThinkOff rather than failing the call, so the same
// pipeline runs against non-reasoning models such as llama3.1.
type ThinkLevel string

const (
	ThinkOff    ThinkLevel = ""
	ThinkOn     ThinkLevel = "on"
	ThinkLow    ThinkLevel = "low"
	ThinkMedium ThinkLevel = "medium"
	ThinkHigh   ThinkLevel = "high"
)

// Enabled reports whether a reasoning trace was requested.
func (t ThinkLevel) Enabled() bool { return t != ThinkOff }

// Effort maps a level onto the low/medium/high vocabulary shared by gpt-oss
// and the hosted reasoning APIs. ThinkOn carries no explicit budget.
func (t ThinkLevel) Effort() string {
	switch t {
	case ThinkLow, ThinkMedium, ThinkHigh:
		return string(t)
	default:
		return ""
	}
}

// ParseThinkLevel reads a configured value, defaulting to ThinkOff.
func ParseThinkLevel(value string) (ThinkLevel, error) {
	switch level := ThinkLevel(strings.ToLower(strings.TrimSpace(value))); level {
	case ThinkOff, ThinkOn, ThinkLow, ThinkMedium, ThinkHigh:
		return level, nil
	case "off", "none", "false", "no":
		return ThinkOff, nil
	case "true", "yes":
		return ThinkOn, nil
	default:
		return ThinkOff, fmt.Errorf("unknown think level %q", value)
	}
}

// Format constrains the shape of the model's answer.
type Format string

const (
	FormatText Format = ""
	FormatJSON Format = "json"
)

// Request is one model call.
type Request struct {
	Model    string
	Messages []Message
	Think    ThinkLevel
	Format   Format
	// Schema is an optional JSON Schema describing the answer. Providers that
	// support schema-constrained decoding use it; the rest fall back to Format.
	Schema      json.RawMessage
	Temperature float64
	MaxTokens   int
}

// Delta is one increment of a streamed response.
//
// Content and Reasoning are never mixed. Restart marks a repair retry: every
// Content and Reasoning token delivered before it must be discarded, because
// the model is answering again from the beginning.
type Delta struct {
	Content   string
	Reasoning string
	Restart   bool
}

// Sink receives stream increments. A nil Sink is valid and discards them.
type Sink func(Delta)

// Emit delivers one increment. Providers must call this rather than invoking
// the func value, so that a nil Sink stays safe.
func (s Sink) Emit(delta Delta) {
	if s != nil {
		s(delta)
	}
}

// Usage reports token consumption, so a job can be held to a budget.
type Usage struct {
	PromptTokens   int `json:"prompt_tokens"`
	ResponseTokens int `json:"response_tokens"`
}

// Total returns the tokens billed for the call.
func (u Usage) Total() int { return u.PromptTokens + u.ResponseTokens }

// Add accumulates usage across the retries of one logical call.
func (u *Usage) Add(other Usage) {
	u.PromptTokens += other.PromptTokens
	u.ResponseTokens += other.ResponseTokens
}

// Response is a completed model call.
type Response struct {
	Content   string
	Reasoning string
	Usage     Usage
	// Attempts is the number of provider calls made, counting repair retries.
	Attempts int
}

// Provider is the one interface every model backend implements.
type Provider interface {
	// Stream performs a single call, delivering increments to sink as they
	// arrive. Implementations must not emit Restart; repair owns that signal.
	Stream(ctx context.Context, req Request, sink Sink) (Response, error)

	// Name identifies the backend for logs and health checks.
	Name() string
}

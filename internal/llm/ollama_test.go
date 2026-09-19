package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func testResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d test status", status),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// collect records the two streams separately, which is what callers rely on.
func collect(content, reasoning *strings.Builder) Sink {
	return func(delta Delta) {
		content.WriteString(delta.Content)
		reasoning.WriteString(delta.Reasoning)
	}
}

func TestOllamaKeepsReasoningOutOfContent(t *testing.T) {
	var requests []string
	provider := NewOllama("http://ollama.test", &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				return nil, err
			}
			requests = append(requests, string(body))
			if !strings.HasSuffix(request.URL.Path, "/api/chat") {
				t.Errorf("path = %q, want /api/chat", request.URL.Path)
			}
			return testResponse(http.StatusOK,
				`{"message":{"thinking":"let me "},"done":false}`+"\n"+
					`{"message":{"thinking":"weigh it"},"done":false}`+"\n"+
					`{"message":{"content":"Bacon "},"done":false}`+"\n"+
					`{"message":{"content":"is cured pork."},"done":true,"prompt_eval_count":12,"eval_count":34}`+"\n"), nil
		}),
	})

	var content, reasoning strings.Builder
	response, err := provider.Stream(context.Background(), Request{
		Model:    "gpt-oss:20b",
		Messages: []Message{User("Bacon")},
		Think:    ThinkHigh,
	}, collect(&content, &reasoning))
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}

	if response.Content != "Bacon is cured pork." {
		t.Errorf("content = %q", response.Content)
	}
	if response.Reasoning != "let me weigh it" {
		t.Errorf("reasoning = %q", response.Reasoning)
	}
	if content.String() != response.Content || reasoning.String() != response.Reasoning {
		t.Errorf("streamed content=%q reasoning=%q", content.String(), reasoning.String())
	}
	if response.Usage.PromptTokens != 12 || response.Usage.ResponseTokens != 34 {
		t.Errorf("usage = %+v", response.Usage)
	}
	if !strings.Contains(requests[0], `"think":"high"`) {
		t.Errorf("request did not carry the think level: %s", requests[0])
	}
}

func TestOllamaRetriesWithoutThinkingWhenUnsupported(t *testing.T) {
	var payloads []string
	provider := NewOllama("http://ollama.test", &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(request.Body)
			payloads = append(payloads, string(body))
			if strings.Contains(string(body), `"think"`) {
				return testResponse(http.StatusBadRequest, `{"error":"llama3.1 does not support thinking"}`), nil
			}
			return testResponse(http.StatusOK, `{"message":{"content":"plain answer"},"done":true}`+"\n"), nil
		}),
	})

	req := Request{Model: "llama3.1", Messages: []Message{User("Bacon")}, Think: ThinkOn}
	response, err := provider.Stream(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	if response.Content != "plain answer" {
		t.Fatalf("content = %q", response.Content)
	}
	if len(payloads) != 2 {
		t.Fatalf("attempts = %d, want 2 (one rejected, one without think)", len(payloads))
	}

	// The unsupported model is remembered, so later calls skip the doomed attempt.
	if _, err := provider.Stream(context.Background(), req, nil); err != nil {
		t.Fatalf("second Stream returned error: %v", err)
	}
	if len(payloads) != 3 {
		t.Fatalf("attempts = %d, want 3: the second call should not retry", len(payloads))
	}
}

func TestOllamaReportsErrorBody(t *testing.T) {
	provider := NewOllama("http://ollama.test", &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return testResponse(http.StatusServiceUnavailable, `{"error":"model unavailable"}`), nil
		}),
	})
	_, err := provider.Stream(context.Background(), Request{Model: "text"}, nil)
	if err == nil || !strings.Contains(err.Error(), "model unavailable") {
		t.Fatalf("error = %v, want the provider's message", err)
	}
}

func TestOllamaHonorsContextCancellation(t *testing.T) {
	provider := NewOllama("http://ollama.test", &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return nil, request.Context().Err()
		}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.Stream(ctx, Request{Model: "text"}, nil); err == nil {
		t.Fatal("Stream returned nil after context cancellation")
	}
}

func TestNormalizeOllamaBaseURL(t *testing.T) {
	cases := map[string]string{
		"":                                    DefaultOllamaBaseURL,
		"http://localhost:11434":              "http://localhost:11434",
		"http://localhost:11434/":             "http://localhost:11434",
		"http://localhost:11434/api/generate": "http://localhost:11434",
		"http://localhost:11434/api/chat":     "http://localhost:11434",
	}
	for input, want := range cases {
		if got := normalizeOllamaBaseURL(input); got != want {
			t.Errorf("normalizeOllamaBaseURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestOllamaSendsJSONFormat(t *testing.T) {
	var payload string
	provider := NewOllama("http://ollama.test", &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(request.Body)
			payload = string(body)
			return testResponse(http.StatusOK, `{"message":{"content":"{}"},"done":true}`+"\n"), nil
		}),
	})
	if _, err := provider.Stream(context.Background(), Request{Model: "structured", Format: FormatJSON}, nil); err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	if !strings.Contains(payload, `"format":"json"`) {
		t.Fatalf("request did not request JSON format: %s", payload)
	}
}

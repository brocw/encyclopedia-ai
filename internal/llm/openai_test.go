package llm

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOpenAISplitsReasoningAndContent(t *testing.T) {
	var payload string
	var authorization string
	provider := NewOpenAI("", "test-key", &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(request.Body)
			payload = string(body)
			authorization = request.Header.Get("Authorization")
			return testResponse(http.StatusOK, strings.Join([]string{
				`: keep-alive`,
				`data: {"choices":[{"delta":{"reasoning":"weighing "}}]}`,
				`data: {"choices":[{"delta":{"reasoning_content":"the sources"}}]}`,
				`data: {"choices":[{"delta":{"content":"Bacon is cured pork."}}]}`,
				`data: {"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":9}}`,
				`data: [DONE]`,
				``,
			}, "\n")), nil
		}),
	})

	var content, reasoning strings.Builder
	response, err := provider.Stream(context.Background(), Request{
		Model:    "openai/gpt-oss-120b",
		Messages: []Message{User("Bacon")},
		Think:    ThinkMedium,
		Format:   FormatJSON,
	}, collect(&content, &reasoning))
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}

	if response.Content != "Bacon is cured pork." {
		t.Errorf("content = %q", response.Content)
	}
	// Both reasoning field spellings feed the same stream.
	if response.Reasoning != "weighing the sources" {
		t.Errorf("reasoning = %q", response.Reasoning)
	}
	if reasoning.String() != response.Reasoning || content.String() != response.Content {
		t.Errorf("streamed content=%q reasoning=%q", content.String(), reasoning.String())
	}
	if response.Usage.Total() != 16 {
		t.Errorf("usage = %+v", response.Usage)
	}
	if authorization != "Bearer test-key" {
		t.Errorf("authorization = %q", authorization)
	}
	if !strings.Contains(payload, `"reasoning":{"effort":"medium"}`) {
		t.Errorf("request did not carry the reasoning effort: %s", payload)
	}
	if !strings.Contains(payload, `"response_format":{"type":"json_object"}`) {
		t.Errorf("request did not carry the JSON response format: %s", payload)
	}
	if provider.BaseURL != DefaultOpenAIBaseURL {
		t.Errorf("base URL = %q, want the OpenRouter default", provider.BaseURL)
	}
}

func TestOpenAIReportsProviderError(t *testing.T) {
	provider := NewOpenAI("https://api.test/v1", "key", &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return testResponse(http.StatusTooManyRequests, `{"error":{"message":"rate limited"}}`), nil
		}),
	})
	_, err := provider.Stream(context.Background(), Request{Model: "m"}, nil)
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("error = %v, want the provider's message", err)
	}
}

func TestOpenAIReportsInStreamError(t *testing.T) {
	provider := NewOpenAI("https://api.test/v1", "key", &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return testResponse(http.StatusOK,
				`data: {"error":{"message":"upstream exploded"}}`+"\n"), nil
		}),
	})
	_, err := provider.Stream(context.Background(), Request{Model: "m"}, nil)
	if err == nil || !strings.Contains(err.Error(), "upstream exploded") {
		t.Fatalf("error = %v, want the in-stream error", err)
	}
}

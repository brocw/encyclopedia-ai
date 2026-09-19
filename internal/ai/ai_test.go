package ai

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

func TestClientStreamsResponseAndUsesJSONFormat(t *testing.T) {
	client := NewClient("http://ollama.test", "text", "structured", &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				return nil, err
			}
			payload := string(body)
			if !strings.Contains(payload, `"model":"structured"`) {
				t.Errorf("request did not use configured model: %s", payload)
			}
			if !strings.Contains(payload, `"format":"json"`) {
				t.Errorf("request did not request JSON format: %s", payload)
			}
			return testResponse(http.StatusOK, `{"response":"hello ","done":false}`+"\n"+
				`{"response":"world","done":true}`+"\n"), nil
		}),
	})
	var tokens []string
	response, err := client.callStreaming(context.Background(), "structured", "prompt", "json", func(token string) {
		tokens = append(tokens, token)
	})
	if err != nil {
		t.Fatalf("callStreaming returned error: %v", err)
	}
	if response != "hello world" || strings.Join(tokens, "") != response {
		t.Fatalf("response=%q tokens=%q", response, strings.Join(tokens, ""))
	}
}

func TestClientReturnsHTTPErrorBody(t *testing.T) {
	client := NewClient("http://ollama.test", "text", "structured", &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return testResponse(http.StatusServiceUnavailable, "model unavailable"), nil
		}),
	})
	_, err := client.callStreaming(context.Background(), "text", "prompt", "", nil)
	if err == nil || !strings.Contains(err.Error(), "model unavailable") {
		t.Fatalf("error = %v, want model error body", err)
	}
}

func TestClientReturnsMalformedStreamError(t *testing.T) {
	client := NewClient("http://ollama.test", "text", "structured", &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return testResponse(http.StatusOK, "not json\n"), nil
		}),
	})
	_, err := client.callStreaming(context.Background(), "text", "prompt", "", nil)
	if err == nil || !strings.Contains(err.Error(), "decode Ollama stream") {
		t.Fatalf("error = %v, want stream decoding error", err)
	}
}

func TestClientHonorsContextCancellation(t *testing.T) {
	client := NewClient("http://ollama.test", "text", "structured", &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return nil, request.Context().Err()
		}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.callStreaming(ctx, "text", "prompt", "", nil)
	if err == nil {
		t.Fatal("callStreaming returned nil after context cancellation")
	}
	if !strings.Contains(err.Error(), "context canceled") && !strings.Contains(fmt.Sprint(err), "canceled") {
		t.Fatalf("error = %v, want cancellation error", err)
	}
}

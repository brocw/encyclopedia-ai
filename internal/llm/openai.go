package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

// DefaultOpenAIBaseURL points at OpenRouter, the deployment target.
const DefaultOpenAIBaseURL = "https://openrouter.ai/api/v1"

// OpenAI calls any OpenAI-compatible /chat/completions endpoint: OpenRouter,
// vLLM, Together, Fireworks, Groq. One client covers the hosted options and
// leaves self-hosted vLLM open without another implementation.
type OpenAI struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client

	// Referer and Title are optional OpenRouter attribution headers.
	Referer string
	Title   string
}

// NewOpenAI builds a provider for an OpenAI-compatible endpoint.
func NewOpenAI(baseURL, apiKey string, httpClient *http.Client) *OpenAI {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultOpenAIBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &OpenAI{
		BaseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:     strings.TrimSpace(apiKey),
		HTTPClient: httpClient,
	}
}

func (o *OpenAI) Name() string { return "openai-compatible" }

type openAIReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Enabled bool   `json:"enabled,omitempty"`
}

type openAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIRequest struct {
	Model          string               `json:"model"`
	Messages       []Message            `json:"messages"`
	Stream         bool                 `json:"stream"`
	StreamOptions  *openAIStreamOptions `json:"stream_options,omitempty"`
	Reasoning      *openAIReasoning     `json:"reasoning,omitempty"`
	ResponseFormat json.RawMessage      `json:"response_format,omitempty"`
	Temperature    float64              `json:"temperature,omitempty"`
	MaxTokens      int                  `json:"max_tokens,omitempty"`
}

type openAIChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
			// OpenRouter reports the trace as "reasoning"; DeepSeek and vLLM
			// use "reasoning_content". Accept both.
			Reasoning        string `json:"reasoning"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Stream implements Provider.
func (o *OpenAI) Stream(ctx context.Context, req Request, sink Sink) (Response, error) {
	if o == nil || o.HTTPClient == nil {
		return Response{}, fmt.Errorf("llm: openai provider is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	payload := openAIRequest{
		Model:          req.Model,
		Messages:       req.Messages,
		Stream:         true,
		StreamOptions:  &openAIStreamOptions{IncludeUsage: true},
		ResponseFormat: openAIResponseFormat(req),
		Temperature:    req.Temperature,
		MaxTokens:      req.MaxTokens,
	}
	if req.Think.Enabled() {
		if effort := req.Think.Effort(); effort != "" {
			payload.Reasoning = &openAIReasoning{Effort: effort}
		} else {
			payload.Reasoning = &openAIReasoning{Enabled: true}
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return Response{}, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if o.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	if o.Referer != "" {
		httpReq.Header.Set("HTTP-Referer", o.Referer)
	}
	if o.Title != "" {
		httpReq.Header.Set("X-Title", o.Title)
	}

	log.Printf("[llm] openai-compatible model=%s think=%v", req.Model, req.Think.Enabled())
	httpResp, err := o.HTTPClient.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("send request: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		return Response{}, openAIStatusError(httpResp)
	}

	return o.readStream(httpResp.Body, sink)
}

func (o *OpenAI) readStream(body io.Reader, sink Sink) (Response, error) {
	var content, reasoning strings.Builder
	var usage Usage

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), maxStreamLineSize)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Blank separators and ": keep-alive" comments carry no payload.
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		data, found := strings.CutPrefix(line, "data:")
		if !found {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}

		var chunk openAIChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return partial(&content, &reasoning, usage), fmt.Errorf("decode stream: %w", err)
		}
		if chunk.Error != nil {
			return partial(&content, &reasoning, usage), fmt.Errorf("provider error: %s", chunk.Error.Message)
		}
		if chunk.Usage != nil {
			usage = Usage{PromptTokens: chunk.Usage.PromptTokens, ResponseTokens: chunk.Usage.CompletionTokens}
		}

		for _, choice := range chunk.Choices {
			trace := choice.Delta.Reasoning
			if trace == "" {
				trace = choice.Delta.ReasoningContent
			}
			if trace != "" {
				reasoning.WriteString(trace)
				sink.Emit(Delta{Reasoning: trace})
			}
			if choice.Delta.Content != "" {
				content.WriteString(choice.Delta.Content)
				sink.Emit(Delta{Content: choice.Delta.Content})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return partial(&content, &reasoning, usage), fmt.Errorf("read stream: %w", err)
	}

	return partial(&content, &reasoning, usage), nil
}

func openAIResponseFormat(req Request) json.RawMessage {
	if len(req.Schema) > 0 {
		format, err := json.Marshal(map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "response",
				"strict": true,
				"schema": req.Schema,
			},
		})
		if err == nil {
			return format
		}
	}
	if req.Format == FormatJSON {
		return json.RawMessage(`{"type":"json_object"}`)
	}
	return nil
}

func openAIStatusError(resp *http.Response) error {
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if readErr != nil {
		return fmt.Errorf("provider returned HTTP %s (read error: %w)", resp.Status, readErr)
	}

	message := strings.TrimSpace(string(body))
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Error.Message != "" {
		message = payload.Error.Message
	}
	if message == "" {
		message = resp.Status
	}
	return fmt.Errorf("provider returned HTTP %s: %s", resp.Status, message)
}

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

const (
	// DefaultOllamaBaseURL is the address of a local Ollama instance.
	DefaultOllamaBaseURL = "http://localhost:11434"
	maxStreamLineSize    = 4 * 1024 * 1024
)

// Ollama calls a local Ollama instance over /api/chat.
//
// /api/chat is used rather than the older /api/generate because it reports the
// reasoning trace in its own message.thinking field. /api/generate folds the
// trace into the answer, which is what puts "<think>" into article prose.
type Ollama struct {
	BaseURL    string
	HTTPClient *http.Client

	// thinkUnsupported records models that rejected a reasoning request, so
	// the retry without it happens only once per model.
	thinkUnsupported map[string]bool
}

// NewOllama builds a provider for the instance at baseURL.
func NewOllama(baseURL string, httpClient *http.Client) *Ollama {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Ollama{
		BaseURL:          normalizeOllamaBaseURL(baseURL),
		HTTPClient:       httpClient,
		thinkUnsupported: make(map[string]bool),
	}
}

func (o *Ollama) Name() string { return "ollama" }

// normalizeOllamaBaseURL accepts a bare host or a full endpoint path, so an
// OLLAMA_API_URL left over from the /api/generate configuration still works.
func normalizeOllamaBaseURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return DefaultOllamaBaseURL
	}
	value = strings.TrimRight(value, "/")
	for _, suffix := range []string{"/api/chat", "/api/generate"} {
		if trimmed, found := strings.CutSuffix(value, suffix); found {
			return trimmed
		}
	}
	return value
}

type ollamaOptions struct {
	Temperature float64 `json:"temperature,omitempty"`
	NumPredict  int     `json:"num_predict,omitempty"`
	NumCtx      int     `json:"num_ctx,omitempty"`
}

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []Message       `json:"messages"`
	Stream   bool            `json:"stream"`
	Think    any             `json:"think,omitempty"`
	Format   json.RawMessage `json:"format,omitempty"`
	Options  *ollamaOptions  `json:"options,omitempty"`
}

type ollamaChatChunk struct {
	Message struct {
		Content  string `json:"content"`
		Thinking string `json:"thinking"`
	} `json:"message"`
	Done            bool   `json:"done"`
	Error           string `json:"error,omitempty"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
}

// Stream implements Provider.
func (o *Ollama) Stream(ctx context.Context, req Request, sink Sink) (Response, error) {
	if o == nil || o.HTTPClient == nil {
		return Response{}, fmt.Errorf("llm: ollama provider is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	think := req.Think
	if o.thinkUnsupported[req.Model] {
		think = ThinkOff
	}

	response, err := o.stream(ctx, req, think, sink)
	// A model without reasoning support rejects the request outright. Record
	// that and answer without a trace, so one pipeline runs against both
	// reasoning and non-reasoning models.
	if err != nil && think.Enabled() && isThinkUnsupported(err) {
		log.Printf("[llm] model %q does not support thinking; continuing without a reasoning trace", req.Model)
		o.thinkUnsupported[req.Model] = true
		return o.stream(ctx, req, ThinkOff, sink)
	}
	return response, err
}

func (o *Ollama) stream(ctx context.Context, req Request, think ThinkLevel, sink Sink) (Response, error) {
	payload := ollamaChatRequest{
		Model:    req.Model,
		Messages: req.Messages,
		Stream:   true,
		Format:   ollamaFormat(req),
	}
	if think.Enabled() {
		if effort := think.Effort(); effort != "" {
			payload.Think = effort
		} else {
			payload.Think = true
		}
	}
	if req.Temperature > 0 || req.MaxTokens > 0 || req.NumCtx > 0 {
		payload.Options = &ollamaOptions{
			Temperature: req.Temperature,
			NumPredict:  req.MaxTokens,
			NumCtx:      req.NumCtx,
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return Response{}, fmt.Errorf("marshal Ollama request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("create Ollama request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	log.Printf("[llm] ollama model=%s think=%v", req.Model, think.Enabled())
	httpResp, err := o.HTTPClient.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("send Ollama request: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		return Response{}, ollamaStatusError(httpResp)
	}

	var content, reasoning strings.Builder
	var usage Usage

	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(make([]byte, 64*1024), maxStreamLineSize)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var chunk ollamaChatChunk
		if err := json.Unmarshal(line, &chunk); err != nil {
			return partial(&content, &reasoning, usage), fmt.Errorf("decode Ollama stream: %w", err)
		}
		if chunk.Error != "" {
			return partial(&content, &reasoning, usage), fmt.Errorf("Ollama error: %s", chunk.Error)
		}

		if chunk.Message.Thinking != "" {
			reasoning.WriteString(chunk.Message.Thinking)
			sink.Emit(Delta{Reasoning: chunk.Message.Thinking})
		}
		if chunk.Message.Content != "" {
			content.WriteString(chunk.Message.Content)
			sink.Emit(Delta{Content: chunk.Message.Content})
		}
		if chunk.Done {
			usage = Usage{PromptTokens: chunk.PromptEvalCount, ResponseTokens: chunk.EvalCount}
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return partial(&content, &reasoning, usage), fmt.Errorf("read Ollama stream: %w", err)
	}

	return partial(&content, &reasoning, usage), nil
}

// ollamaFormat renders the answer constraint. Ollama accepts either the string
// "json" or a JSON Schema object.
func ollamaFormat(req Request) json.RawMessage {
	if len(req.Schema) > 0 {
		return req.Schema
	}
	if req.Format == FormatJSON {
		return json.RawMessage(`"json"`)
	}
	return nil
}

func ollamaStatusError(resp *http.Response) error {
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if readErr != nil {
		return fmt.Errorf("Ollama returned HTTP %s (read error: %w)", resp.Status, readErr)
	}

	message := strings.TrimSpace(string(body))
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Error != "" {
		message = payload.Error
	}
	if message == "" {
		message = resp.Status
	}
	return fmt.Errorf("Ollama returned HTTP %s: %s", resp.Status, message)
}

// isThinkUnsupported matches the rejection Ollama sends for a model with no
// reasoning support.
func isThinkUnsupported(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "does not support thinking") ||
		strings.Contains(message, "thinking is not supported") ||
		strings.Contains(message, `"think"`)
}

func partial(content, reasoning *strings.Builder, usage Usage) Response {
	return Response{Content: content.String(), Reasoning: reasoning.String(), Usage: usage, Attempts: 1}
}

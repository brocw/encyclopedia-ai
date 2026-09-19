package ai

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAPIURL          = "http://localhost:11434/api/generate"
	defaultTextModel       = "llama3.1"
	defaultStructuredModel = "mistral"
	defaultTimeout         = 10 * time.Minute
	maxStreamLineSize      = 4 * 1024 * 1024
)

// Client is the Ollama-backed implementation of the article-generation agents.
// Its fields are exported so applications and tests can configure the client
// without relying on package globals.
type Client struct {
	APIURL          string
	TextModel       string
	StructuredModel string
	HTTPClient      *http.Client
}

// NewClient creates an Ollama client. Empty values use the application defaults.
func NewClient(apiURL, textModel, structuredModel string, httpClient *http.Client) *Client {
	if apiURL == "" {
		apiURL = defaultAPIURL
	}
	if textModel == "" {
		textModel = defaultTextModel
	}
	if structuredModel == "" {
		structuredModel = defaultStructuredModel
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{
		APIURL:          apiURL,
		TextModel:       textModel,
		StructuredModel: structuredModel,
		HTTPClient:      httpClient,
	}
}

// NewClientFromEnv reads optional Ollama configuration from the environment.
// OLLAMA_TIMEOUT_SECONDS controls the total duration of one request.
func NewClientFromEnv() *Client {
	timeout := defaultTimeout
	if seconds, err := strconv.Atoi(os.Getenv("OLLAMA_TIMEOUT_SECONDS")); err == nil && seconds > 0 {
		timeout = time.Duration(seconds) * time.Second
	}
	return NewClient(
		os.Getenv("OLLAMA_API_URL"),
		os.Getenv("OLLAMA_TEXT_MODEL"),
		os.Getenv("OLLAMA_STRUCTURED_MODEL"),
		&http.Client{Timeout: timeout},
	)
}

// DefaultClient returns the process-wide production client.
func DefaultClient() *Client {
	return defaultClient
}

var defaultClient = NewClientFromEnv()

//go:embed generate_prompt.txt
var generatePrompt string

//go:embed revise_prompt.txt
var revisePrompt string

//go:embed categorize_prompt.txt
var categorizePrompt string

//go:embed references_prompt.txt
var referencesPrompt string

//go:embed infobox_prompt.txt
var infoboxPrompt string

//go:embed seealso_prompt.txt
var seealsoPrompt string

//go:embed evaluate_prompt.txt
var evaluatePrompt string

//go:embed revision_plan_prompt.txt
var revisionPlanPrompt string

type ollamaRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
	Format string `json:"format,omitempty"`
}

type ollamaResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
	Error    string `json:"error,omitempty"`
}

// callStreaming is the one streaming implementation used by all agents.
// Ollama emits newline-delimited JSON, one object per line.
func (c *Client) callStreaming(ctx context.Context, model, prompt, format string, onToken func(string)) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || c.HTTPClient == nil {
		return "", fmt.Errorf("ollama client is not configured")
	}

	reqData := ollamaRequest{Model: model, Prompt: prompt, Stream: true, Format: format}
	jsonData, err := json.Marshal(reqData)
	if err != nil {
		return "", fmt.Errorf("marshal Ollama request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.APIURL, bytes.NewReader(jsonData))
	if err != nil {
		return "", fmt.Errorf("create Ollama request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	log.Printf("[ollama-stream] Starting request to model '%s'...", model)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("send Ollama request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
		if readErr != nil {
			return "", fmt.Errorf("Ollama returned HTTP %s (read error: %w)", resp.Status, readErr)
		}
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		return "", fmt.Errorf("Ollama returned HTTP %s: %s", resp.Status, message)
	}

	var full strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), maxStreamLineSize)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var chunk ollamaResponse
		if err := json.Unmarshal(line, &chunk); err != nil {
			return full.String(), fmt.Errorf("decode Ollama stream: %w", err)
		}
		if chunk.Error != "" {
			return full.String(), fmt.Errorf("Ollama error: %s", chunk.Error)
		}
		if chunk.Response != "" {
			full.WriteString(chunk.Response)
			if onToken != nil {
				onToken(chunk.Response)
			}
		}
		if chunk.Done {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return full.String(), fmt.Errorf("read Ollama stream: %w", err)
	}

	response := full.String()
	log.Printf("[ollama-stream] Model '%s' finished. Response length: %d chars.", model, len(response))
	return response, nil
}

func (c *Client) GenerateArticle(ctx context.Context, topic string, onToken func(string)) (string, error) {
	prompt := fmt.Sprintf(generatePrompt, topic)
	return c.callStreaming(ctx, c.TextModel, prompt, "", onToken)
}

func (c *Client) ReviseArticle(ctx context.Context, topic, article, revisionPlan string, onToken func(string)) (string, error) {
	prompt := fmt.Sprintf(revisePrompt, topic, article, revisionPlan)
	return c.callStreaming(ctx, c.TextModel, prompt, "", onToken)
}

func (c *Client) CategorizeArticle(ctx context.Context, article string, onToken func(string)) (string, error) {
	prompt := fmt.Sprintf(categorizePrompt, article)
	return c.callStreaming(ctx, c.StructuredModel, prompt, "json", onToken)
}

func (c *Client) References(ctx context.Context, article string, onToken func(string)) (string, error) {
	prompt := fmt.Sprintf(referencesPrompt, article)
	return c.callStreaming(ctx, c.StructuredModel, prompt, "json", onToken)
}

func (c *Client) Infobox(ctx context.Context, topic, article string, onToken func(string)) (string, error) {
	prompt := fmt.Sprintf(infoboxPrompt, topic, article)
	return c.callStreaming(ctx, c.StructuredModel, prompt, "json", onToken)
}

func (c *Client) SeeAlso(ctx context.Context, article string, onToken func(string)) (string, error) {
	prompt := fmt.Sprintf(seealsoPrompt, article)
	return c.callStreaming(ctx, c.StructuredModel, prompt, "json", onToken)
}

func (c *Client) EvaluateArticle(ctx context.Context, article string, onToken func(string)) (string, error) {
	prompt := fmt.Sprintf(evaluatePrompt, article)
	return c.callStreaming(ctx, c.StructuredModel, prompt, "json", onToken)
}

func (c *Client) PlanRevision(ctx context.Context, article, evaluation string, onToken func(string)) (string, error) {
	prompt := fmt.Sprintf(revisionPlanPrompt, article, evaluation)
	return c.callStreaming(ctx, c.StructuredModel, prompt, "json", onToken)
}

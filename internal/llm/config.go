package llm

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// ProviderOllama runs inference locally. ProviderOpenAI calls any
	// OpenAI-compatible endpoint, OpenRouter by default.
	ProviderOllama = "ollama"
	ProviderOpenAI = "openai"

	defaultTimeout = 10 * time.Minute
)

// Config selects and configures the provider. It is read from the environment
// once at start-up and validated before the server accepts traffic.
type Config struct {
	Provider string

	// TextModel writes prose. StructuredModel produces JSON.
	TextModel       string
	StructuredModel string

	// Think is the reasoning budget requested of both models. Providers fall
	// back to no trace when the model cannot produce one.
	Think ThinkLevel

	// Attempts bounds the repair retries of one logical call.
	Attempts int

	// ContextTokens is the context window. Zero keeps the provider default.
	// Ollama's default of 4096 truncates a reasoning model's answer away, so
	// a reasoning run should raise it.
	ContextTokens int

	Timeout time.Duration

	// BaseURL is the Ollama host, or the OpenAI-compatible endpoint.
	BaseURL string
	APIKey  string
	Referer string
	Title   string
}

func env(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func envDefault(fallback string, names ...string) string {
	if value := env(names...); value != "" {
		return value
	}
	return fallback
}

// ConfigFromEnv reads the provider configuration. The OLLAMA_* names are
// honoured so an existing local setup keeps working.
func ConfigFromEnv() (Config, error) {
	provider := strings.ToLower(envDefault(ProviderOllama, "LLM_PROVIDER"))

	timeout := defaultTimeout
	if raw := env("LLM_TIMEOUT_SECONDS", "OLLAMA_TIMEOUT_SECONDS"); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds <= 0 {
			return Config{}, fmt.Errorf("llm: timeout seconds must be a positive integer, got %q", raw)
		}
		timeout = time.Duration(seconds) * time.Second
	}

	attempts := DefaultAttempts
	if raw := env("LLM_ATTEMPTS"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return Config{}, fmt.Errorf("llm: attempts must be a positive integer, got %q", raw)
		}
		attempts = parsed
	}

	contextTokens := 0
	if raw := env("LLM_CONTEXT_TOKENS", "OLLAMA_NUM_CTX"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return Config{}, fmt.Errorf("llm: context tokens must be a positive integer, got %q", raw)
		}
		contextTokens = parsed
	}

	think, err := ParseThinkLevel(env("LLM_THINK"))
	if err != nil {
		return Config{}, fmt.Errorf("llm: %w", err)
	}

	config := Config{
		Provider:      provider,
		Think:         think,
		Attempts:      attempts,
		ContextTokens: contextTokens,
		Timeout:       timeout,
		APIKey:        env("LLM_API_KEY", "OPENROUTER_API_KEY"),
		Referer:       env("LLM_REFERER"),
		Title:         env("LLM_TITLE"),
	}

	switch provider {
	case ProviderOllama:
		config.BaseURL = envDefault(DefaultOllamaBaseURL, "OLLAMA_HOST", "OLLAMA_API_URL", "LLM_BASE_URL")
		config.TextModel = envDefault("llama3.1", "LLM_TEXT_MODEL", "OLLAMA_TEXT_MODEL")
		config.StructuredModel = envDefault("mistral", "LLM_STRUCTURED_MODEL", "OLLAMA_STRUCTURED_MODEL")
	case ProviderOpenAI:
		config.BaseURL = envDefault(DefaultOpenAIBaseURL, "LLM_BASE_URL")
		// Verify model identifiers against the provider's catalogue; these are
		// a starting point, not a guarantee of availability.
		config.TextModel = envDefault("openai/gpt-oss-120b", "LLM_TEXT_MODEL")
		config.StructuredModel = envDefault("openai/gpt-oss-120b", "LLM_STRUCTURED_MODEL")
	default:
		return Config{}, fmt.Errorf("llm: unknown provider %q (want %q or %q)", provider, ProviderOllama, ProviderOpenAI)
	}

	if err := config.validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (c Config) validate() error {
	if c.Provider == ProviderOpenAI && c.APIKey == "" {
		return fmt.Errorf("llm: provider %q requires LLM_API_KEY (or OPENROUTER_API_KEY)", c.Provider)
	}
	if c.TextModel == "" || c.StructuredModel == "" {
		return fmt.Errorf("llm: both a text model and a structured model must be configured")
	}
	return nil
}

// NewProvider builds the configured provider.
func (c Config) NewProvider() (Provider, error) {
	httpClient := &http.Client{Timeout: c.Timeout}

	switch c.Provider {
	case ProviderOllama:
		return NewOllama(c.BaseURL, httpClient), nil
	case ProviderOpenAI:
		provider := NewOpenAI(c.BaseURL, c.APIKey, httpClient)
		provider.Referer = c.Referer
		provider.Title = c.Title
		return provider, nil
	default:
		return nil, fmt.Errorf("llm: unknown provider %q", c.Provider)
	}
}

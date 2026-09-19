package llm

import (
	"strings"
	"testing"
	"time"
)

func TestConfigFromEnvDefaultsToLocalOllama(t *testing.T) {
	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv returned error: %v", err)
	}
	if config.Provider != ProviderOllama || config.BaseURL != DefaultOllamaBaseURL {
		t.Fatalf("provider=%q baseURL=%q", config.Provider, config.BaseURL)
	}
	if config.TextModel != "llama3.1" || config.StructuredModel != "mistral" {
		t.Fatalf("models = %q / %q", config.TextModel, config.StructuredModel)
	}
	if config.Think != ThinkOff {
		t.Fatalf("think = %q, want off by default", config.Think)
	}
}

// The legacy OLLAMA_* names must keep working, including an API URL that still
// points at the /api/generate endpoint.
func TestConfigFromEnvHonorsLegacyOllamaNames(t *testing.T) {
	t.Setenv("OLLAMA_API_URL", "http://gpu.local:11434/api/generate")
	t.Setenv("OLLAMA_TEXT_MODEL", "gpt-oss:20b")
	t.Setenv("OLLAMA_STRUCTURED_MODEL", "qwen3:30b-a3b")
	t.Setenv("OLLAMA_TIMEOUT_SECONDS", "900")

	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv returned error: %v", err)
	}
	if config.TextModel != "gpt-oss:20b" || config.StructuredModel != "qwen3:30b-a3b" {
		t.Fatalf("models = %q / %q", config.TextModel, config.StructuredModel)
	}
	if config.Timeout != 900*time.Second {
		t.Fatalf("timeout = %v", config.Timeout)
	}

	provider, err := config.NewProvider()
	if err != nil {
		t.Fatalf("NewProvider returned error: %v", err)
	}
	if ollama, ok := provider.(*Ollama); !ok || ollama.BaseURL != "http://gpu.local:11434" {
		t.Fatalf("provider = %#v, want the endpoint path trimmed", provider)
	}
}

func TestConfigFromEnvRequiresAKeyForHostedProviders(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "openai")
	if _, err := ConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "LLM_API_KEY") {
		t.Fatalf("error = %v, want a missing-key failure", err)
	}
}

func TestConfigFromEnvBuildsHostedProvider(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "openai")
	t.Setenv("OPENROUTER_API_KEY", "sk-test")
	t.Setenv("LLM_THINK", "high")

	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv returned error: %v", err)
	}
	if config.BaseURL != DefaultOpenAIBaseURL || config.Think != ThinkHigh {
		t.Fatalf("baseURL=%q think=%q", config.BaseURL, config.Think)
	}
	provider, err := config.NewProvider()
	if err != nil {
		t.Fatalf("NewProvider returned error: %v", err)
	}
	if _, ok := provider.(*OpenAI); !ok {
		t.Fatalf("provider = %#v, want an OpenAI-compatible client", provider)
	}
}

func TestConfigFromEnvRejectsBadValues(t *testing.T) {
	t.Run("provider", func(t *testing.T) {
		t.Setenv("LLM_PROVIDER", "anthropic")
		if _, err := ConfigFromEnv(); err == nil {
			t.Fatal("ConfigFromEnv accepted an unknown provider")
		}
	})
	t.Run("think", func(t *testing.T) {
		t.Setenv("LLM_THINK", "maybe")
		if _, err := ConfigFromEnv(); err == nil {
			t.Fatal("ConfigFromEnv accepted an unknown think level")
		}
	})
	t.Run("timeout", func(t *testing.T) {
		t.Setenv("LLM_TIMEOUT_SECONDS", "0")
		if _, err := ConfigFromEnv(); err == nil {
			t.Fatal("ConfigFromEnv accepted a non-positive timeout")
		}
	})
}

func TestParseThinkLevel(t *testing.T) {
	cases := map[string]ThinkLevel{
		"":       ThinkOff,
		"off":    ThinkOff,
		"true":   ThinkOn,
		"on":     ThinkOn,
		"HIGH":   ThinkHigh,
		" low  ": ThinkLow,
	}
	for input, want := range cases {
		got, err := ParseThinkLevel(input)
		if err != nil || got != want {
			t.Errorf("ParseThinkLevel(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
}

func TestConfigFromEnvReadsTheContextWindow(t *testing.T) {
	t.Setenv("LLM_CONTEXT_TOKENS", "16384")
	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv returned error: %v", err)
	}
	if config.ContextTokens != 16384 {
		t.Fatalf("context tokens = %d", config.ContextTokens)
	}
}

func TestConfigFromEnvRejectsABadContextWindow(t *testing.T) {
	t.Setenv("LLM_CONTEXT_TOKENS", "-1")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("ConfigFromEnv accepted a negative context window")
	}
}

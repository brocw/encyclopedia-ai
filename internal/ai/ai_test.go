package ai

import (
	"context"
	"strings"
	"testing"

	"encyclopedia-ai/internal/llm"
)

// recordingProvider captures the requests each agent builds.
type recordingProvider struct {
	requests []llm.Request
	content  string
	reason   string
}

func (p *recordingProvider) Name() string { return "recording" }

func (p *recordingProvider) Stream(_ context.Context, req llm.Request, sink llm.Sink) (llm.Response, error) {
	p.requests = append(p.requests, req)
	content := p.content
	if content == "" {
		content = `{"ok":true}`
	}
	if p.reason != "" {
		sink.Emit(llm.Delta{Reasoning: p.reason})
	}
	sink.Emit(llm.Delta{Content: content})
	return llm.Response{Content: content, Reasoning: p.reason}, nil
}

func testClient(provider llm.Provider) *Client {
	return New(provider, llm.Config{
		TextModel:       "text-model",
		StructuredModel: "structured-model",
		Think:           llm.ThinkHigh,
		Attempts:        2,
	})
}

func TestAgentsUseTheModelRoleForTheirTask(t *testing.T) {
	provider := &recordingProvider{content: "prose"}
	client := testClient(provider)
	ctx := context.Background()

	if _, err := client.GenerateArticle(ctx, "Bacon", nil); err != nil {
		t.Fatalf("GenerateArticle returned error: %v", err)
	}
	if _, err := client.ReviseArticle(ctx, "Bacon", "draft", "plan", nil); err != nil {
		t.Fatalf("ReviseArticle returned error: %v", err)
	}
	for _, request := range provider.requests {
		if request.Model != "text-model" {
			t.Errorf("prose agent used model %q", request.Model)
		}
		if request.Format == llm.FormatJSON {
			t.Error("prose agent asked for JSON")
		}
		if request.Messages[0].Role != llm.RoleSystem {
			t.Error("prose agent did not send a system turn")
		}
	}

	provider.requests = nil
	provider.content = `{"topics":[]}`
	if _, err := client.SeeAlso(ctx, "article", nil); err != nil {
		t.Fatalf("SeeAlso returned error: %v", err)
	}
	if _, err := client.EvaluateArticle(ctx, "article", nil); err != nil {
		t.Fatalf("EvaluateArticle returned error: %v", err)
	}
	for _, request := range provider.requests {
		if request.Model != "structured-model" {
			t.Errorf("structured agent used model %q", request.Model)
		}
		if request.Format != llm.FormatJSON {
			t.Errorf("structured agent did not request JSON")
		}
	}
}

func TestAgentsCarryTheConfiguredThinkLevel(t *testing.T) {
	provider := &recordingProvider{content: "prose"}
	if _, err := testClient(provider).GenerateArticle(context.Background(), "Bacon", nil); err != nil {
		t.Fatalf("GenerateArticle returned error: %v", err)
	}
	if provider.requests[0].Think != llm.ThinkHigh {
		t.Fatalf("think = %q, want the configured level", provider.requests[0].Think)
	}
}

// The reasoning trace must never reach the returned article text.
func TestReasoningIsExcludedFromTheAnswer(t *testing.T) {
	provider := &recordingProvider{
		content: "Bacon is cured pork.",
		reason:  "First I should decide the scope of the article.",
	}

	var reasoning strings.Builder
	article, err := testClient(provider).GenerateArticle(context.Background(), "Bacon", func(delta llm.Delta) {
		reasoning.WriteString(delta.Reasoning)
	})
	if err != nil {
		t.Fatalf("GenerateArticle returned error: %v", err)
	}
	if article != "Bacon is cured pork." {
		t.Fatalf("article = %q, want the answer without its reasoning", article)
	}
	if reasoning.String() != provider.reason {
		t.Fatalf("reasoning = %q, want it delivered on its own channel", reasoning.String())
	}
}

func TestPromptsCarryTheAgentArguments(t *testing.T) {
	provider := &recordingProvider{content: "prose"}
	client := testClient(provider)

	if _, err := client.ReviseArticle(context.Background(), "Bacon", "the draft", "the plan", nil); err != nil {
		t.Fatalf("ReviseArticle returned error: %v", err)
	}
	prompt := provider.requests[0].Messages[1].Content
	for _, want := range []string{"Bacon", "the draft", "the plan"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("revision prompt is missing %q: %s", want, prompt)
		}
	}
	if strings.Contains(prompt, "%!") {
		t.Errorf("revision prompt has a formatting error: %s", prompt)
	}
}

func TestClientWithoutAProviderFailsClearly(t *testing.T) {
	_, err := New(nil, llm.Config{TextModel: "m"}).GenerateArticle(context.Background(), "Bacon", nil)
	if err == nil || !strings.Contains(err.Error(), "provider is not configured") {
		t.Fatalf("error = %v", err)
	}
}

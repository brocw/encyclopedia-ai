// Package ai holds the article agents. Each agent owns a prompt and a model
// role; every model call goes through the llm.Provider boundary, so the same
// agents run against local Ollama and against a hosted OpenAI-compatible API.
package ai

import (
	"cmp"
	"context"
	_ "embed"
	"fmt"

	"encyclopedia-ai/internal/llm"
	"encyclopedia-ai/internal/plan"
)

//go:embed intake_prompt.txt
var intakePrompt string

//go:embed outline_prompt.txt
var outlinePrompt string

//go:embed lead_prompt.txt
var leadPrompt string

//go:embed section_prompt.txt
var sectionPrompt string

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

// The system turns establish the role once, so the per-agent prompt files stay
// focused on the task and its output format.
const (
	proseSystemPrompt = "You are an encyclopedia editor. You write neutral, " +
		"factual, well-structured reference prose. Follow the requested output " +
		"format exactly and emit nothing besides the article itself."

	structuredSystemPrompt = "You are an encyclopedia editor's assistant producing " +
		"structured data. Reply with a single JSON object matching the requested " +
		"schema, and nothing else: no prose, no explanation, no code fences."
)

// Client runs the agents against a configured provider.
type Client struct {
	Provider llm.Provider
	Config   llm.Config
}

// New builds a client over an existing provider.
func New(provider llm.Provider, config llm.Config) *Client {
	return &Client{Provider: provider, Config: config}
}

// NewFromEnv reads the provider configuration and builds the client. It
// returns an error rather than falling back to a default, so a misconfigured
// deployment fails at start-up instead of on the first request.
func NewFromEnv() (*Client, error) {
	config, err := llm.ConfigFromEnv()
	if err != nil {
		return nil, err
	}
	provider, err := config.NewProvider()
	if err != nil {
		return nil, err
	}
	return New(provider, config), nil
}

// Describe reports the active configuration for start-up logging.
func (c *Client) Describe() string {
	if c == nil || c.Provider == nil {
		return "no provider configured"
	}
	return fmt.Sprintf("provider=%s text=%s structured=%s think=%s",
		c.Provider.Name(), c.Config.TextModel, c.Config.StructuredModel,
		cmp.Or(string(c.Config.Think), "off"))
}

// prose runs a text-model call and returns the answer without its reasoning trace.
func (c *Client) prose(ctx context.Context, prompt string, sink llm.Sink) (string, error) {
	return c.call(ctx, llm.Request{
		Model:    c.Config.TextModel,
		Messages: []llm.Message{llm.System(proseSystemPrompt), llm.User(prompt)},
		Think:    c.Config.Think,
		NumCtx:   c.Config.ContextTokens,
	}, sink)
}

// structured runs a JSON-mode call on the structured model.
//
// Repair here is deliberately syntactic: it recovers empty answers, code
// fences, and truncated JSON. Semantic validation of each agent's shape stays
// in the orchestrator, which owns the meaning of those documents.
func (c *Client) structured(ctx context.Context, prompt string, sink llm.Sink) (string, error) {
	return c.call(ctx, llm.Request{
		Model:    c.Config.StructuredModel,
		Messages: []llm.Message{llm.System(structuredSystemPrompt), llm.User(prompt)},
		Think:    c.Config.Think,
		Format:   llm.FormatJSON,
		NumCtx:   c.Config.ContextTokens,
	}, sink)
}

func (c *Client) call(ctx context.Context, req llm.Request, sink llm.Sink) (string, error) {
	if c == nil || c.Provider == nil {
		return "", fmt.Errorf("ai: provider is not configured")
	}
	response, err := llm.StreamWithRepair(ctx, c.Provider, req, nil, c.Config.Attempts, sink)
	return response.Content, err
}

// Intake turns a reader's topic into a brief: a title, a subject sentence, and
// the boundary of the article. Nothing is written until this has run.
func (c *Client) Intake(ctx context.Context, topic string, sink llm.Sink) (string, error) {
	return c.structured(ctx, fmt.Sprintf(intakePrompt, topic), sink)
}

// Outline plans the article's sections from the brief.
func (c *Client) Outline(ctx context.Context, brief string, sink llm.Sink) (string, error) {
	return c.structured(ctx, fmt.Sprintf(outlinePrompt, brief), sink)
}

// DraftLead writes the untitled opening. It is written first even though it
// summarizes sections that do not exist yet, because the outline already says
// what they will contain.
func (c *Client) DraftLead(ctx context.Context, brief, outline string, sink llm.Sink) (string, error) {
	return c.prose(ctx, fmt.Sprintf(leadPrompt, brief, outline), sink)
}

// DraftSection writes one section against its assignment. draft is the article
// so far, which is what keeps sections from repeating one another.
func (c *Client) DraftSection(ctx context.Context, brief, outline string, section plan.Section, draft string, sink llm.Sink) (string, error) {
	return c.prose(ctx, fmt.Sprintf(sectionPrompt, brief, outline, section.Prompt(), draft), sink)
}

func (c *Client) ReviseArticle(ctx context.Context, brief, article, revisionPlan string, sink llm.Sink) (string, error) {
	return c.prose(ctx, fmt.Sprintf(revisePrompt, brief, article, revisionPlan), sink)
}

func (c *Client) CategorizeArticle(ctx context.Context, article string, sink llm.Sink) (string, error) {
	return c.structured(ctx, fmt.Sprintf(categorizePrompt, article), sink)
}

func (c *Client) References(ctx context.Context, article string, sink llm.Sink) (string, error) {
	return c.structured(ctx, fmt.Sprintf(referencesPrompt, article), sink)
}

func (c *Client) Infobox(ctx context.Context, topic, article string, sink llm.Sink) (string, error) {
	return c.structured(ctx, fmt.Sprintf(infoboxPrompt, topic, article), sink)
}

func (c *Client) SeeAlso(ctx context.Context, article string, sink llm.Sink) (string, error) {
	return c.structured(ctx, fmt.Sprintf(seealsoPrompt, article), sink)
}

func (c *Client) EvaluateArticle(ctx context.Context, brief, article string, sink llm.Sink) (string, error) {
	return c.structured(ctx, fmt.Sprintf(evaluatePrompt, brief, article), sink)
}

func (c *Client) PlanRevision(ctx context.Context, article, evaluation string, sink llm.Sink) (string, error) {
	return c.structured(ctx, fmt.Sprintf(revisionPlanPrompt, article, evaluation), sink)
}

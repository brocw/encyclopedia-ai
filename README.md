# encyclopedia-ai

![A screenshot of an AI-generated article about Bacon](./screenshot.png)

An experimental encyclopedia generator that uses AI agents to draft, evaluate, revise, and enhance Wikipedia-style articles.

Model calls go through a provider boundary, so the same pipeline runs against a
local Ollama instance or any OpenAI-compatible API such as OpenRouter. Reasoning
models are supported: a model's thinking is streamed on its own channel and
never mixed into the article text.

## Run it

Install:

- Go 1.24 or newer
- Ollama
- `curl`
- The `llama3.1` and `mistral` Ollama models, or whichever models you configure

The convenience script starts Ollama, waits for it to become available, pulls the models, and starts the server:

```bash
./start.sh
```

To run Ollama separately:

```bash
go run ./cmd/server
```

The application listens on `http://localhost:8080` by default. Configuration is available through environment variables:

| Variable | Default | Purpose |
| --- | --- | --- |
| `ENCYCLOPEDIA_ADDR` | `:8080` | HTTP listen address |
| `LLM_PROVIDER` | `ollama` | `ollama` or `openai` |
| `LLM_TEXT_MODEL` | `llama3.1` | Prose generation and revision model |
| `LLM_STRUCTURED_MODEL` | `mistral` | Evaluation, planning, and metadata model |
| `LLM_THINK` | `off` | Reasoning budget: `off`, `on`, `low`, `medium`, `high` |
| `LLM_ATTEMPTS` | `3` | Provider calls allowed per agent call, including repairs |
| `LLM_CONTEXT_TOKENS` | provider default | Context window. Raise this for reasoning models |
| `LLM_TIMEOUT_SECONDS` | `600` | Maximum duration of one model request |
| `LLM_BASE_URL` | provider default | Ollama host, or OpenAI-compatible endpoint |
| `LLM_API_KEY` | — | Required when `LLM_PROVIDER=openai`; `OPENROUTER_API_KEY` also works |
| `ROCR_VISIBLE_DEVICES` | `1` | GPU selection used by `start.sh` |
| `HIP_VISIBLE_DEVICES` | `1` | GPU selection used by `start.sh` |

The previous `OLLAMA_API_URL`, `OLLAMA_TEXT_MODEL`, `OLLAMA_STRUCTURED_MODEL`,
and `OLLAMA_TIMEOUT_SECONDS` names still work. An `OLLAMA_API_URL` that points
at `/api/generate` is accepted and trimmed to the host.

Configuration is resolved once at start-up, so a bad value stops the server
rather than failing on the first request.

### Running against a reasoning model

Locally, pull a model that supports thinking and ask for a trace:

```bash
LLM_TEXT_MODEL=gpt-oss:20b \
LLM_STRUCTURED_MODEL=gpt-oss:20b \
LLM_THINK=high \
LLM_CONTEXT_TOKENS=16384 \
./start.sh
```

**Set `LLM_CONTEXT_TOKENS` when using a reasoning model.** Ollama's default
context window is 4096 tokens, and a single trace can exceed it on its own: the
answer is then truncated away and the agent returns nothing. Measured on
`gpt-oss:20b` at `LLM_THINK=high`, one metadata agent produced 36,000
characters of reasoning and no answer until the window was raised. A larger
window costs VRAM, so on a 16 GB card expect to trade context against model
size.

Against OpenRouter:

```bash
export LLM_PROVIDER=openai
export LLM_API_KEY=sk-...
export LLM_TEXT_MODEL=openai/gpt-oss-120b
export LLM_STRUCTURED_MODEL=openai/gpt-oss-120b
export LLM_THINK=high
go run ./cmd/server
```

Model identifiers should be checked against the provider's catalogue. A model
that cannot produce a reasoning trace is detected on its first call and used
without one, so `llama3.1` and `mistral` keep working with `LLM_THINK` set.

## Generation flow

`POST /api/start` accepts:

```json
{"topic":"Bacon","max_rounds":3}
```

The server streams Server-Sent Events in this order:

1. Article, evaluation, and revision-plan token events
2. `round_complete` events containing the evaluated draft and scores
3. Optional `converged` event
4. Metadata token events
5. `article_done` and a structured `done` event containing the final `ArticleState`

Every stream carries three events. For a stream named `article`:

| Event | Meaning |
| --- | --- |
| `article_token` | A token of the answer |
| `article_reasoning` | A token of the model's reasoning trace |
| `article_restart` | Discard this stream's tokens; a repair retry is answering again |

`max_rounds` is the maximum number of evaluated rounds, not the number of unverified revisions. A final round is never revised without being evaluated.

The final state includes `status`, `termination_reason`, `converged`, round history, metadata, and any warnings. Loop failures emit an `error` event with the partial state; metadata failures produce a completed article with warnings.

## Development

Run the checks locally:

```bash
GOCACHE=/tmp/encyclopedia-ai-go-cache go test -race ./...
GOCACHE=/tmp/encyclopedia-ai-go-cache go vet ./...
test -z "$(gofmt -l .)"
node --check web/static/script.js
```

The provider boundary is injectable, so the `llm`, `ai`, orchestration, and HTTP tests run without Ollama or a network. The browser supports canceling generation, reviewing round drafts, applying a local edit, and following related topics.

## Roadmap

`docs/DEPLOYMENT.md` records the deployment plan and the milestone ladder
towards a research-backed pipeline: outlining, retrieval, grounded drafting,
and claim verification.

## Limitations

Generated prose and metadata are not independently verified. In particular, the current references agent produces candidate references rather than retrieved or validated citations. Source retrieval and evidence application are intentionally separate future work.

This is a stateless prototype: generated articles and local edits are not persisted.

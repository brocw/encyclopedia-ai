# encyclopedia-ai

![A screenshot of an AI-generated article about Bacon](./screenshot.png)

An experimental encyclopedia generator that uses Ollama-backed agents to draft, evaluate, revise, and enhance Wikipedia-style articles.

## Run it

Install:

- Go 1.24 or newer
- Ollama
- `curl`
- The `llama3.1` and `mistral` Ollama models

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
| `OLLAMA_API_URL` | `http://localhost:11434/api/generate` | Ollama generation endpoint |
| `OLLAMA_TEXT_MODEL` | `llama3.1` | Prose generation and revision model |
| `OLLAMA_STRUCTURED_MODEL` | `mistral` | Evaluation, planning, and metadata model |
| `OLLAMA_TIMEOUT_SECONDS` | `600` | Maximum duration of one Ollama request |
| `ROCR_VISIBLE_DEVICES` | `1` | GPU selection used by `start.sh` |
| `HIP_VISIBLE_DEVICES` | `1` | GPU selection used by `start.sh` |

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

The AI boundary is injectable, so orchestration and HTTP tests do not require a running Ollama instance. The browser supports canceling generation, reviewing round drafts, applying a local edit, and following related topics.

## Limitations

Generated prose and metadata are not independently verified. In particular, the current references agent produces candidate references rather than retrieved or validated citations. Source retrieval and evidence application are intentionally separate future work.

This is a stateless prototype: generated articles and local edits are not persisted.

# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Encyclopedia-AI is an AI-powered encyclopedia generator that produces Wikipedia-style articles using a cybernetic feedback loop. Users submit a topic, and specialized agents generate, evaluate, revise, and enhance articles with structured metadata (categories, references, infobox, related topics). The system autonomously refines articles until quality converges or a maximum round count is reached.

## Running the Application

```bash
./start.sh
```

This starts Ollama, pulls required models (`llama3.1` and `mistral`), and runs the Go server on `localhost:8080`. To run the server alone (if Ollama is already running):

```bash
go run ./cmd/server
```

Tests, `go vet`, formatting checks, and GitHub Actions CI are configured. Tests use injected AI fakes and do not require Ollama.

## Tech Stack

- **Backend**: Go 1.24.5 using only the standard library (no external dependencies)
- **Frontend**: Vanilla HTML/CSS/JS with `marked.js` via CDN for Markdown rendering
- **LLM**: Ollama API at `localhost:11434` — `llama3.1` for text generation, `mistral` for structured JSON outputs

## Architecture

### Cybernetic Feedback Loop

The system follows a Generate → [Evaluate → Compare → Plan → Revise]* → Metadata pipeline:

1. `POST /api/start` with `{topic, max_rounds}` triggers `RunArticleLoop`
2. **Actuator**: Generate initial article via `llama3.1`
3. **Sensor**: `EvaluateArticleStreaming` (mistral, JSON) scores the article on 5 dimensions and lists critical issues
4. **Comparator**: `hasConverged` checks if overall score >= 8.0 with no critical issues; `isStagnant` detects score plateaus between rounds
5. **Controller**: `PlanRevisionStreaming` (mistral, JSON) produces targeted revision instructions from the evaluation
6. **Actuator**: `ReviseArticleStreaming` (llama3.1) applies the revision plan
7. Loop repeats until convergence, stagnation, or max rounds
8. **Metadata agents** (references, infobox, see-also, categories) run once in parallel on the final article

All responses stream tokens via SSE. The frontend shows a round timeline with per-round quality scores and a convergence badge.

### Backend Structure (`internal/`)

- **`ai/`**: Configurable, context-aware Ollama client with one shared newline-delimited JSON streaming implementation. `OLLAMA_API_URL`, model names, and request timeout are environment-configurable.
- **`orchestrator/`**: Coordinates the cybernetic loop through the injectable `Agent` interface. `ArticleState` records status, termination reason, warnings, and `Rounds` history. A final evaluated round is never revised without another evaluation; failed loop phases return partial state and an error.
- **`handlers/`**: `Handler` owns the injected agent and exposes `POST /api/start`. `safeSender` serializes concurrent SSE writes and cancels the request when the client disconnects. The final `done` event contains structured state rather than a double-encoded JSON string.

### Frontend (`web/static/`)

Single-page app with Wikipedia-inspired styling. `script.js` manages article state client-side, handles SSE streaming, sanitizes rendered Markdown, displays a round timeline with color-coded quality scores, shows explicit termination and warning states, supports canceling generation, and provides local article/draft review editing.

### Key Patterns

- **Two-model strategy**: `llama3.1` for prose generation/revision; `mistral` with Ollama's `format: "json"` for structured data (evaluation, revision plans, metadata)
- **Token-level streaming**: Each AI call takes a callback invoked per token, enabling real-time SSE pushes via `http.Flusher`
- **Cancellation propagation**: Browser aborts and disconnected SSE clients cancel the request context passed through the handler, orchestrator, and Ollama client.
- **Autonomous convergence**: The loop self-terminates based on quality scores — no manual intervention required
- **Parallel metadata agents**: 4 metadata agents run as goroutines with WaitGroup synchronization after the loop completes
- **Stateless server**: No database or persistence — `ArticleState` (including full round history) is returned to the client. Browser edits are local only.

### Current limitations

References are currently model-generated candidates and are not retrieved or verified. Do not present them as proof of a claim. Evidence retrieval and applying verified sources are planned separately.

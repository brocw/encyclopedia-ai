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

There are no tests, linting, or CI configured.

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

- **`ai/`**: Ollama API client with streaming support. Contains embedded prompt templates (`//go:embed`). Core streaming functions: `CallOllamaStreaming` (free text), `CallOllamaStreamingJSON` (JSON-constrained). Nine agent functions map to two models.
- **`orchestrator/`**: Manages `ArticleState` with `Rounds` history and coordinates the cybernetic loop. Key types: `Evaluation` (5 scores + critical issues), `Round` (article snapshot + evaluation + revision plan). Metadata agents run concurrently via `sync.WaitGroup` + channels.
- **`handlers/`**: Single `POST /api/start` endpoint. `safeSender` provides thread-safe SSE writes. SSE events: `article_token`, `evaluation_token`, `revision_plan_token`, `round_complete`, `converged`, `article_done`, `done`.

### Frontend (`web/static/`)

Single-page app with Wikipedia-inspired styling. `script.js` manages article state client-side, handles SSE streaming, renders JSON metadata into HTML components, displays a round timeline with color-coded quality scores (green/yellow/red), and shows a convergence badge.

### Key Patterns

- **Two-model strategy**: `llama3.1` for prose generation/revision; `mistral` with Ollama's `format: "json"` for structured data (evaluation, revision plans, metadata)
- **Token-level streaming**: Each AI call takes a callback invoked per token, enabling real-time SSE pushes via `http.Flusher`
- **Autonomous convergence**: The loop self-terminates based on quality scores — no manual intervention required
- **Parallel metadata agents**: 4 metadata agents run as goroutines with WaitGroup synchronization after the loop completes
- **Stateless server**: No database or persistence — `ArticleState` (including full round history) is returned to the client

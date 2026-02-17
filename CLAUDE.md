# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Encyclopedia-AI is an AI-powered encyclopedia generator that produces Wikipedia-style articles using multiple LLM agents. Users submit a topic, and specialized agents generate, fact-check, and enhance articles with structured metadata (categories, references, infobox, related topics). The server is stateless — article state lives on the client and is sent back for revision cycles.

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
- **LLM**: Ollama API at `localhost:11434` — `llama3.1` for text generation/fact-checking, `mistral` for structured JSON outputs

## Architecture

### Request Flow

1. `POST /api/start` with topic → Orchestrator generates article via `llama3.1`, then runs 5 post-article agents in parallel
2. `POST /api/continue` with full `ArticleState` → Orchestrator revises article using previous fact-check, then re-runs all 5 agents
3. All responses stream tokens via SSE (Server-Sent Events)

### Backend Structure (`internal/`)

- **`ai/`**: Ollama API client with streaming support. Contains embedded prompt templates (`//go:embed`). Three core streaming functions: `CallOllamaStreaming` (free text), `CallOllamaStreamingJSON` (JSON-constrained), and a non-streaming `callOllama`. Seven agent functions map to the two models.
- **`orchestrator/`**: Manages `ArticleState` and coordinates agent execution. Post-article agents (fact-check, references, infobox, see-also, categories) run concurrently via `sync.WaitGroup` + channels.
- **`handlers/`**: HTTP endpoints and SSE streaming. `safeSender` provides thread-safe concurrent writes to the SSE stream.

### Frontend (`web/static/`)

Single-page app with Wikipedia-inspired styling. `script.js` manages article state client-side, handles SSE streaming, renders JSON metadata into HTML components, and auto-generates table of contents from headings.

### Key Patterns

- **Two-model strategy**: `llama3.1` for prose generation/revision/fact-checking; `mistral` with Ollama's `format: "json"` for structured data extraction
- **Token-level streaming**: Each AI call takes a callback invoked per token, enabling real-time SSE pushes via `http.Flusher`
- **Parallel agents**: 5 post-article agents run as goroutines with WaitGroup synchronization and channel-based result collection
- **Stateless server**: No database or persistence — `ArticleState` round-trips through the client

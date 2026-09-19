# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Encyclopedia-AI is an AI-powered encyclopedia generator that produces Wikipedia-style articles using a cybernetic feedback loop. Users submit a topic, and specialized agents generate, evaluate, revise, and enhance articles with structured metadata (categories, references, infobox, related topics). The system autonomously refines articles until quality converges or a maximum round count is reached.

## Running the Application

```bash
./start.sh
```

This starts Ollama, pulls the configured models (`llama3.1` and `mistral` by default), and runs the Go server on `localhost:8080`. To run the server alone (if Ollama is already running):

```bash
go run ./cmd/server
```

Tests, `go vet`, formatting checks, and GitHub Actions CI are configured. Tests use injected AI fakes and do not require Ollama.

## Tech Stack

- **Backend**: Go 1.25 standard library, plus one dependency: `modernc.org/sqlite`, a pure-Go driver chosen so `CGO_ENABLED=0` and the static single-binary build survive. The dependency budget is deliberately short — see `docs/DEPLOYMENT.md`.
- **Frontend**: Vanilla HTML/CSS/JS with `marked.js` via CDN for Markdown rendering
- **LLM**: any `llm.Provider`. Local Ollama (`/api/chat`) by default; OpenRouter or another OpenAI-compatible endpoint with `LLM_PROVIDER=openai`. Reasoning models are supported through `LLM_THINK`.

## Architecture

### Cybernetic Feedback Loop

The system follows an Intake → Outline → Draft → [Evaluate → Compare → Plan → Revise]* → Metadata pipeline:

1. `POST /api/articles` enqueues a job; a worker runs `RunArticleLoop`
2. **Framing**: `Intake` (structured model) turns the topic into a `plan.Brief` — title, subject sentence, scope, exclusions, rejected readings. Nothing is written until this exists.
3. **Planning**: `Outline` (structured model) turns the brief into a `plan.Outline` — sections with a purpose, a word target, and the `key_questions` each must answer
4. **Actuator**: `DraftLead` then `DraftSection` per section (text model). Each call sees the brief, the whole outline, and the draft so far, so sections neither overlap nor drift out of scope. The loop writes the headings itself.
5. **Sensor**: `EvaluateArticle` (structured model, JSON) scores the article on 5 dimensions against the brief and lists critical issues
6. **Comparator**: `hasConverged` checks if overall score >= 8.0 with no critical issues; `isStagnant` detects score plateaus between rounds
7. **Controller**: `PlanRevision` (structured model, JSON) produces targeted revision instructions from the evaluation
8. **Actuator**: `ReviseArticle` (text model) rewrites the article whole against the brief and the revision plan
9. Loop repeats until convergence, stagnation, or max rounds
10. **Metadata agents** (references, infobox, see-also, categories) run once in parallel on the final article

All responses stream tokens via SSE. The frontend shows the article plan, a round timeline with per-round quality scores, a convergence badge, and the live reasoning trace when a reasoning model is configured. Progress is driven by `phase` events the pipeline emits, not inferred from which stream is producing tokens.

The loop still grades itself: `factual_accuracy` is scored by a model with no access to evidence. Hardening that gate is M4.

### Backend Structure (`internal/`)

- **`llm/`**: The provider boundary. `Provider.Stream` delivers `Delta{Content, Reasoning, Restart}`, keeping a reasoning trace out of article text and out of the JSON parsers. Implementations: `Ollama` (`/api/chat`, with a remembered fallback for models that reject `think`) and `OpenAI` (any OpenAI-compatible `/chat/completions`, reading both `reasoning` and `reasoning_content`). `StreamWithRepair` re-asks the model when an answer is empty or unparseable, and salvages fenced JSON locally. `ConfigFromEnv` is validated at start-up.
- **`plan/`**: The two documents that come before the prose — `Brief` and `Outline`. They sit in their own package because both sides of the agent boundary read their fields, and because retrieval will hang evidence off `Outline.Questions()`. Parsing repairs what it can (unnamed or duplicated sections dropped, word targets clamped, an over-long outline truncated) and errors only on a document too broken to write from.
- **`ai/`**: The agents. Each owns an embedded prompt and a model role (prose or structured) and calls through `llm`. Repair here is syntactic only; semantic validation of each agent's document stays in the orchestrator.
- **`orchestrator/`**: Coordinates the pipeline through the injectable `Agent` interface, whose methods stream to an `llm.Sink`. `draft.go` holds the pre-prose half: framing, planning, and section-by-section drafting through `articleWriter`. `ArticleState` records the brief, the outline, status, termination reason, warnings, and `Rounds` history. A final evaluated round is never revised without another evaluation; failed loop phases return partial state and an error.
- **`jobs/`**: generation as a background job. `Runner` drains a queue of `Job`s with a worker pool; `Recorder` coalesces token deltas into batched events before they reach the log; `Broker` wakes live subscribers; `Store` persists jobs and their append-only event logs, with `MemoryStore` as the default implementation. Event sequence numbers are contiguous from 1, which is what makes a stream resumable.
- **`handlers/`**: the job API — enqueue, read, stream, cancel. The SSE handler replays a job's log from the client's last sequence number and then follows it live, so a dropped connection resumes without a gap.

### Frontend (`web/static/`)

Single-page app with Wikipedia-inspired styling. `script.js` manages article state client-side, handles SSE streaming, sanitizes rendered Markdown, displays a round timeline with color-coded quality scores, streams the reasoning trace into a collapsible "Editor's notes" panel as text (never markup), shows explicit termination and warning states, supports canceling generation, and provides local article/draft review editing.

### Key Patterns

- **The plan is the specification**: everything that writes or judges the article is shown the brief. `completeness` means "covers what the brief put in scope"; off-topic means "outside it". An agent asked to judge an article against nothing will judge it against nothing.
- **The server owns the article text**: `articleWriter` assembles the article and mirrors it to the `article` stream. Its copy is authoritative, so whenever the stream could drift from it — a repair retry, or a section whose answer needed cleaning — it emits `Restart` and repaints. A client never has to reconstruct what the server already knows.
- **Repair what is repairable, fail on what is not**: `plan.ParseOutline` drops a nameless section and clamps an absurd word target, because an article is still writable. It errors on a one-section outline, because there is nothing to write.

- **Two-model strategy**: a text model for prose drafting and revision; a structured model in JSON mode for intake, outlining, evaluation, revision plans, and metadata. Both are configurable and may be the same model.
- **Provider independence**: nothing above `internal/llm` knows which backend is in use. Keep provider-specific behaviour inside that package.
- **Context window**: a reasoning trace competes with the answer for the context window. Ollama's 4096-token default truncates the answer away entirely, so `LLM_CONTEXT_TOKENS` must be raised whenever `LLM_THINK` is on. When an answer comes back empty, `StreamWithRepair` also steps the reasoning budget down for the retry, since re-asking at the same budget repeats the failure.
- **Separated reasoning**: answer tokens and reasoning tokens travel on different channels end to end — `llm.Delta`, then `<stream>_token` and `<stream>_reasoning` SSE events, then the collapsible "Editor's notes" panel. A reasoning trace must never reach article text or a JSON parser.
- **Token-level streaming**: Each AI call takes an `llm.Sink` invoked per increment, enabling real-time SSE pushes via `http.Flusher`. Call `Sink.Emit`, never the func value, so a nil sink stays safe.
- **Cancellation propagation**: Browser aborts and disconnected SSE clients cancel the request context passed through the handler, orchestrator, and provider.
- **Autonomous convergence**: The loop self-terminates based on quality scores — no manual intervention required
- **Parallel metadata agents**: 4 metadata agents run as goroutines with WaitGroup synchronization after the loop completes
- **Work off the request path**: a request enqueues; a worker generates. Nothing long-running happens inside an HTTP handler, because a reasoning run outlives any reasonable request timeout.
- **The log is the contract**: clients reconstruct all state by replaying a job's events. Live and resuming subscribers follow the identical path, so there is no separate catch-up code to keep correct.
- **Token coalescing**: deltas are batched by size or interval before being logged. A run that emitted 11,087 deltas becomes a few dozen events.
- **One store contract, two backends**: `MemoryStore` and `SQLiteStore` are exercised by the same conformance suite in `store_test.go`. Add a backend by adding a case there. `Claim` must stay atomic — it is what stops two workers taking one job.

### Planned work

`docs/DEPLOYMENT.md` holds the deployment plan and the milestone ladder. M0
(the provider boundary), M1 (jobs and a resumable log), and M2 (intake,
outline, section drafting) are done. M3–M5 add retrieval, grounded drafting,
and verification, at which point the references agent is deleted rather than
improved — `Outline.Questions()` is where retrieval attaches.

### Current limitations

References are currently model-generated candidates and are not retrieved or verified. Do not present them as proof of a claim. Evidence retrieval and applying verified sources are planned separately.

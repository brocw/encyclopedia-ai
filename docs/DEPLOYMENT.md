# Deployment plan

Status: **planned, not started.** Execute after M5 (evals), so the cost and
latency of a reasoning-model article are known before the door opens.

This document assumes the milestone ladder in the refactor plan:

| # | Milestone | State |
| --- | --- | --- |
| M0 | Provider boundary, `/api/chat`, thinking split from content, repair | done |
| M1 | Jobs, persistence, resumable SSE | done (SQLite; Postgres remains for deploy) |
| M2 | Intake, outline, section drafting | done |
| M3 | Retrieval, evidence store, real references | next |
| M4 | Verification agents, hard convergence gates | planned |
| M5 | Evals, cost controls | planned |
| M6 | Containerize and deploy — **this document** | planned |

## Inference: two providers, one interface

The decision is to keep self-hosted inference working permanently rather than
treat it as a development crutch.

- **Local / development** — Ollama against the RX 9070 XT (gfx1201, 16 GB).
  That ceiling puts `gpt-oss:20b` and `qwen3:30b-a3b` (quantized, MoE) in
  reach and rules out 120B-class models. Enough to exercise every phase of the
  pipeline end to end, which is the point.
- **Deployed** — OpenRouter, through the OpenAI-compatible provider. Elastic,
  no GPU to babysit, and model choice becomes a config value.

Both satisfy `llm.Provider`, so the switch is `LLM_PROVIDER=openai` plus a key.
Nothing above `internal/llm` knows which is in use. Keep it that way: no
provider-specific behaviour may leak into `agents`, `pipeline`, or `handlers`.

Self-hosted vLLM on rented GPUs stays open as a third implementation of the
same interface if OpenRouter's per-token cost ever exceeds a dedicated box.

## 1. Build

- Multi-stage Dockerfile: `golang:1.25` build, `CGO_ENABLED=0`, final stage
  `gcr.io/distroless/static`.
- **Move `web/static` into `embed.FS`.** `cmd/server/main.go` currently does
  `http.FileServer(http.Dir("./web/static"))`, a working-directory dependency
  that breaks in a container. One embedded binary is the deployable.
- `docker-compose.yml` for local parity: `app`, `worker`, `postgres`,
  `searxng`, `ollama`.

## 2. Configuration

One typed config struct, populated from the environment, validated at boot,
fail-fast on anything missing. No key ever baked into an image.

| Variable | Purpose |
| --- | --- |
| `LLM_PROVIDER` | `ollama` or `openai` |
| `LLM_API_KEY` | OpenRouter key; required when provider is `openai` |
| `LLM_BASE_URL` | Defaults to OpenRouter |
| `LLM_TEXT_MODEL` / `LLM_STRUCTURED_MODEL` | Model selection per role |
| `LLM_THINK` | `off`, `on`, `low`, `medium`, `high` |
| `DATABASE_URL` | Postgres |
| `ENCYCLOPEDIA_ADDR` | Listen address |

## M2, and what it leaves for M3

One-shot generation is gone. A topic becomes a `plan.Brief`, the brief becomes
a `plan.Outline`, and prose is written one section at a time against both. The
brief then travels with the article: the evaluator scores `completeness`
against a stated scope rather than against nothing.

The hook for M3 is `Outline.Questions()` — every key question the sections
committed to answering, in order. Those are the retrieval queries. Until they
are answered from retrieved text, `open_questions` is the honest record of
what the model knew it was unsure about, and the references agent still
fabricates.

## 3. Persistence (the outstanding half of M1)

`jobs.Store` is the seam: `MemoryStore` and `SQLiteStore` implement it, and a
Postgres backend implements the same six methods and joins the conformance
suite in `store_test.go`. `Claim` must stay atomic — that is what stops two
workers taking the same job.


Postgres: `articles`, `jobs`, `events`, `sources`, `claims`, `rounds`.

The `events` table is the spine — an append-only log per job. SSE becomes a
subscriber that replays from an offset, which is what makes reconnects,
shareable URLs, and multiple viewers work.

The stdlib-only rule is relaxed here by decision. The dependency budget is
short and explicit: a Postgres driver, a JSON Schema validator, OpenTelemetry.
Everything else stays stdlib. M0 needed no dependencies at all.

## 4. Runtime

Fly.io, or Hetzner with Caddy in front. App and worker run as separate
processes from the same image; scale workers independently, since generation
is the slow half and HTTP is nearly free.

Health: `/healthz` (process up), `/readyz` (database and provider reachable).

## 5. Before opening the door

| Concern | Action |
| --- | --- |
| Cost | Per-job token ceiling that aborts; cost logged per article; per-IP quota |
| Abuse | Rate limit `POST /api/articles`; topic filter |
| **Prompt injection** | Retrieved pages are untrusted. Fence passages, strip HTML, never let fetched text reach a system prompt. Arrives with M3 — design for it then, not after. |
| Observability | `log/slog` structured, request IDs, OTel span per pipeline step |
| Licensing | Wikipedia retrieval is CC BY-SA; attribution obligations attach |
| Honesty | Keep the "AI-generated, not independently verified" disclaimer; surface citation coverage in the UI |

## 6. CI/CD

Extend the existing workflow: `gofmt`, `vet`, `go test -race`, then
`golangci-lint`, the M5 eval suite, image build and push, deploy on tag.

## 7. Open questions

- Anonymous generation, or accounts? Decides whether quotas key on IP or user.
- Retention: are generated articles public and permanent, or session-scoped?
  Changes the caching story and the licensing surface.
- Does the deployed instance keep an Ollama fallback, or is it OpenRouter only?

# llm-gateway — Implementation Plan

> **Status: APPROVED + REVISED-1** (autoplan D4 = option A, 2026-05-13; Plan Revision 1 added same day from Task 0 finding). Approved with 42 auto-decided fixes + Task 0 Wizard-of-Oz validation. 4 user challenges (UC-1/3/4/5) explicitly declined — archived in `TODOS.md`.
>
> **🆕 Plan Revision 1 (post-Task-0, 2026-05-13):** P3 expanded from OpenAI-only inbound to **dual-protocol inbound** (OpenAI Chat Completions + Anthropic Messages). See §Plan Revision 1 at the end of this file. Where the older sections below (§1 Premises, §2 Architecture, §4 Tasks) contradict the revision, the revision wins.
>
> Working plan file. Reviewed by `/plan-ceo-review`, `/plan-eng-review`, `/plan-devex-review` via the `/autoplan` chain. Design doc lineage: `~/.gstack/projects/llm-gateway/panda-main-design-20260513-040833.md`. Test plan artifact: `~/.gstack/projects/llm-gateway/panda-main-test-plan-20260513-040833.md`.
>
> **Implementer note:** The 42 auto-decided fixes are listed in the per-phase "Auto-Decided Findings Summary" tables (CEO §end, Eng §end, DX §end), each with a Task # column. When working a Task, scan all three Summary tables for that Task # before starting — those are required modifications to the §2/§4 baselines above.

**Goal:** Build an AI-native LLM proxy that exposes an OpenAI-compatible `/v1/chat/completions` endpoint for GLM + DeepSeek, with all operations (provider config, routing, usage queries) performed via MCP — no web UI, no admin CLI subcommands.

**Architecture (one-line):** Single static Go binary serving HTTP (OpenAI-compat) + MCP (stdio); SQLite embedded; pluggable Provider interface; goreleaser → GitHub Releases.

**Tech Stack:**
- Go 1.22 (toolchain pinned in `go.mod`)
- `modernc.org/sqlite` (pure-Go SQLite, no CGO)
- `github.com/mark3labs/mcp-go` (pending Open Question #4 — may switch to `metoro-io/mcp-golang` after 1h spike)
- stdlib `net/http`, `database/sql`, `encoding/json`
- Build: `goreleaser` + GitHub Actions

---

## §1. Confirmed Premises (from D2)

P1. Single-user / small-team scope. Not SaaS. No RBAC, no multi-tenancy, no billing.
P2. MCP is the only ops surface. No web UI; only `start`/`stop`/`init`/`version` as CLI subcommands.
P3. OpenAI Chat Completions is the inbound lingua franca.
P4. MVP providers: GLM (Zhipu) + DeepSeek.
P5. Self-hosted single artifact (Go static binary), SQLite embedded, no Postgres/Redis.
P6. "AI-native" = the agent operating the gateway is also a consumer of the LLMs flowing through it. Control plane ≡ data plane's caller.

---

## §2. Architecture

### Component map

```
                    ┌───────────────────────────────────────────┐
                    │            llm-gateway (1 binary)         │
                    │                                           │
   coding agent ───►│ HTTP server   ┌──────────────┐            │
   (Cline/Cursor)   │ /v1/chat/...  │   Router     │            │
   OpenAI-compat    │  (port 7421)  │  (model→     │            │
                    │   ── SSE ───► │   provider)  │            │
                    │               └──────┬───────┘            │
                    │                      │                    │
                    │                      ▼                    │
                    │             ┌──────────────┐              │  ┌──────────┐
                    │             │  Provider    │──────────────┼─►│  GLM     │
                    │             │  interface   │              │  └──────────┘
                    │             └──────┬───────┘              │  ┌──────────┐
                    │                    └──────────────────────┼─►│ DeepSeek │
                    │                                           │  └──────────┘
   coding agent ───►│ MCP server                                │
   (Cline as ops)   │  (stdio)      ┌──────────────┐            │
                    │               │  Tool set:   │            │
                    │               │  list_*      │            │
                    │               │  add_*       │            │
                    │               │  set_*       │            │
                    │               │  get_usage   │            │
                    │               │  tail_logs   │            │
                    │               └──────┬───────┘            │
                    │                      ▼                    │
                    │             ┌──────────────┐              │
                    │             │   Storage    │              │
                    │             │  (SQLite)    │              │
                    │             └──────────────┘              │
                    └───────────────────────────────────────────┘
```

### File structure (target end-state for v0.1.0)

```
cmd/
  llm-gateway/
    main.go             # entrypoint, flag parsing, dispatch to subcommands
    start.go            # `llm-gateway start` — boots HTTP + MCP servers
    init.go             # `llm-gateway init` — bootstraps SQLite + asks for GATEWAY_TOKEN
internal/
  http/
    server.go           # net/http mux, middleware chain
    chat_completions.go # /v1/chat/completions handler + SSE bridge
    auth.go             # bearer token middleware
  router/
    router.go           # model → provider resolution; alias lookup
  provider/
    provider.go         # interface { ChatComplete, ChatCompleteStream, Name() }
    glm.go              # Zhipu adapter
    deepseek.go         # DeepSeek adapter
    common.go           # shared HTTP client, retry policy, errors
  mcp/
    server.go           # MCP stdio server wrapping mcp-go
    tools_provider.go   # list_providers, add_provider, remove_provider
    tools_alias.go      # set_model_alias, list_model_aliases, remove_model_alias
    tools_observability.go  # get_usage, tail_logs, get_request
  store/
    sqlite.go           # connection + migrations
    schema.sql          # canonical schema
    providers.go        # CRUD on providers table
    aliases.go          # CRUD on model_aliases table
    logs.go             # request log writes + queries
docs/
  plan.md               # this file
  README.md             # user-facing
  ARCHITECTURE.md       # generated post-v0.1.0
.github/
  workflows/
    test.yml
    release.yml
.goreleaser.yaml
go.mod
go.sum
README.md
```

### Storage schema (SQLite)

```sql
CREATE TABLE providers (
  name        TEXT PRIMARY KEY,         -- e.g. "glm-prod", "deepseek-personal"
  kind        TEXT NOT NULL,            -- "glm" | "deepseek"
  base_url    TEXT NOT NULL,
  api_key     TEXT NOT NULL,            -- encrypted-at-rest (v0.2); plaintext v0.1
  is_default  INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL          -- unix ms
);

CREATE TABLE model_aliases (
  alias         TEXT PRIMARY KEY,       -- e.g. "fast", "smart", "glm-4-flash"
  provider_name TEXT NOT NULL REFERENCES providers(name) ON DELETE CASCADE,
  upstream_model TEXT NOT NULL,         -- e.g. "glm-4-flash", "deepseek-chat"
  created_at    INTEGER NOT NULL
);

CREATE TABLE request_logs (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  ts              INTEGER NOT NULL,    -- unix ms
  client_model    TEXT NOT NULL,       -- model name the caller asked for
  resolved_model  TEXT NOT NULL,       -- upstream model after alias resolution
  provider_name   TEXT NOT NULL,
  prompt_tokens   INTEGER,
  completion_tokens INTEGER,
  total_tokens    INTEGER,
  latency_ms      INTEGER,
  status          TEXT NOT NULL,        -- "ok" | "upstream_error" | "timeout" | "abort"
  error_msg       TEXT,                 -- nullable
  prompt_excerpt  TEXT                  -- first 200 chars; full prompt only if LOG_FULL_PROMPT=1
);

CREATE INDEX idx_logs_ts ON request_logs(ts DESC);
CREATE INDEX idx_logs_provider ON request_logs(provider_name);
```

### MCP tool set (v0.1.0)

| Tool                  | Purpose                                            |
|-----------------------|----------------------------------------------------|
| `list_providers`      | Return all registered providers with kind/base_url |
| `add_provider`        | Register a new provider (name, kind, base_url, api_key) |
| `remove_provider`     | Delete a provider (cascades to aliases)            |
| `set_default_provider`| Mark a provider as default fallback                |
| `list_model_aliases`  | Return all alias → (provider, upstream_model) maps |
| `set_model_alias`     | Create or update a model alias                     |
| `remove_model_alias`  | Delete an alias                                    |
| `get_usage`           | Token usage aggregated by provider/model over a window (default 24h) |
| `tail_logs`           | Most recent N request_logs entries (default 20)    |
| `get_request`         | Full detail of a request_log entry by id           |

Plus 1 prompt: `gateway://how-to` returning a markdown cheatsheet for an agent to bootstrap usage.

### Auth model

- Inbound HTTP: single bearer token from env var `LLM_GATEWAY_TOKEN`. Request header `Authorization: Bearer <token>`.
- MCP: stdio transport — process boundary is the auth boundary (whoever can fork-exec the binary can manage it). v0 acceptable for single-user.
- Upstream: each provider has its own `api_key` stored in SQLite.

---

## §3. Version Roadmap

### v0.1.0 — MVP (target: 8 工程日)

**Capabilities:**
- HTTP `/v1/chat/completions` (POST), both blocking and streaming (SSE)
- 2 providers: GLM + DeepSeek
- SQLite embedded, schema above
- MCP stdio server with the 10 tools listed above
- Bearer token inbound auth
- goreleaser → GitHub Releases (linux/amd64, linux/arm64, darwin/arm64, darwin/amd64)

**Out of scope (defer):**
- API key encryption at rest (plaintext in SQLite, file-permission-protected)
- Model alias rich semantics (just simple `alias → (provider, model)` lookup; no fallback chains)
- Multi-provider sharding / load balancing
- Request retry on upstream failure
- HTTP/SSE MCP transport (stdio only)
- Cost tracking (just token counts, no $ multiplication)

### v0.2.0 — Polish

- API key encryption at rest (XChaCha20-Poly1305 with key derived from a master env var)
- HTTP/SSE MCP transport (so multiple agents can connect to one running gateway)
- Cost tracking (per-provider $ rates baked in, derived from token counts)
- `set_routing_rule` MCP tool (e.g. "send any model containing 'reasoning' to GLM-Plus")
- Docker image (scratch-based, ~10MB)

### v0.3.0 — Operability

- Prometheus `/metrics` endpoint (opt-in via env)
- Health check `/healthz`
- Graceful shutdown with in-flight request draining
- Log rotation (request_logs table) with configurable retention
- More providers: Qwen, Moonshot, Doubao

### v1.0.0 — Stability lock

- API stability guarantee on MCP tool schemas
- Migration system for SQLite schema changes
- Comprehensive test coverage (provider adapters mocked at HTTP layer)
- README + architecture docs + cookbook of agent prompts

---

## §4. Implementation Tasks (TDD order)

Each task: write failing test → minimal impl → green → commit.

### Task 1: HTTP skeleton + hello-world `/v1/chat/completions`
**Files:** `cmd/llm-gateway/main.go`, `cmd/llm-gateway/start.go`, `internal/http/server.go`, `internal/http/chat_completions.go`

- [ ] Test: POST to `/v1/chat/completions` with valid OpenAI-shaped JSON returns 200 with stub response.
- [ ] Test: POST with malformed JSON returns 400.
- [ ] Test: POST without `Authorization` header returns 401.
- [ ] Impl: minimal handler that echoes back a stub completion (no upstream yet).
- [ ] Commit: `feat: http skeleton with /v1/chat/completions and bearer auth`

### Task 2: Provider interface + DeepSeek adapter
**Files:** `internal/provider/provider.go`, `internal/provider/deepseek.go`, `internal/provider/common.go`

- [ ] Test: `ChatComplete` against a mock HTTP server returns parsed `ChatCompletionResponse`.
- [ ] Test: `ChatCompleteStream` returns a channel of chunks; channel closes on `[DONE]`.
- [ ] Test: upstream 401 surfaces as `provider.ErrUpstreamAuth`.
- [ ] Test: upstream timeout (context deadline) surfaces as `provider.ErrTimeout`.
- [ ] Impl: `Provider` interface, DeepSeek client using `net/http`.
- [ ] Commit: `feat: provider interface + DeepSeek adapter`

### Task 3: GLM adapter
**Files:** `internal/provider/glm.go`

- [ ] Test: `ChatComplete` against mock GLM server returns parsed response (note: GLM uses different field names for some metadata).
- [ ] Test: Streaming behaves like DeepSeek (both speak SSE with OpenAI delta format).
- [ ] Impl: GLM provider. Verify auth scheme — bearer or JWT — against current Zhipu docs (Open Question #7).
- [ ] Commit: `feat: GLM provider adapter`

### Task 4: SQLite store + providers/aliases CRUD
**Files:** `internal/store/sqlite.go`, `internal/store/schema.sql`, `internal/store/providers.go`, `internal/store/aliases.go`

- [ ] Test: schema migration is idempotent (run twice, no error).
- [ ] Test: provider CRUD round-trip.
- [ ] Test: alias CRUD; deleting a provider cascades to aliases.
- [ ] Impl: `modernc.org/sqlite`, schema.sql embedded via `//go:embed`.
- [ ] Commit: `feat: sqlite store with providers and aliases`

### Task 5: Router (model → provider resolution)
**Files:** `internal/router/router.go`

- [ ] Test: model name matches an alias → routes to alias's `(provider, upstream_model)`.
- [ ] Test: model name doesn't match alias but matches a provider's known model → routes there.
- [ ] Test: no match + default provider exists → routes to default.
- [ ] Test: no match + no default → returns `ErrNoRoute`.
- [ ] Impl: `Resolve(clientModel string) (Provider, upstreamModel string, error)`.
- [ ] Commit: `feat: router with alias + default resolution`

### Task 6: Wire HTTP handler to router + provider + log writer
**Files:** `internal/http/chat_completions.go` (update), `internal/store/logs.go`

- [ ] Test: e2e blocking — POST chat completion → resolved provider returns stub → log row written with prompt/completion tokens.
- [ ] Test: e2e streaming — POST with `stream: true` → SSE chunks flow through; log row written on stream close with aggregated tokens.
- [ ] Test: upstream error → log row has `status: "upstream_error"` and error_msg.
- [ ] Impl: handler reads body → router resolves → provider streams or completes → on completion, write log row.
- [ ] Commit: `feat: e2e chat completion with request logging`

### Task 7: MCP stdio server skeleton
**Files:** `internal/mcp/server.go`

- [ ] Test: MCP server starts, responds to `initialize` with correct protocol version.
- [ ] Test: `tools/list` returns the registered tools.
- [ ] Impl: integrate `mcp-go` (or chosen lib), wire stdio transport.
- [ ] Commit: `feat: mcp stdio server skeleton`

### Task 8: MCP provider tools
**Files:** `internal/mcp/tools_provider.go`

- [ ] Test for each: `list_providers`, `add_provider`, `remove_provider`, `set_default_provider`.
- [ ] Impl.
- [ ] Commit: `feat: mcp tools for provider management`

### Task 9: MCP alias tools
**Files:** `internal/mcp/tools_alias.go`

- [ ] Test for each: `list_model_aliases`, `set_model_alias`, `remove_model_alias`.
- [ ] Impl.
- [ ] Commit: `feat: mcp tools for model aliases`

### Task 10: MCP observability tools
**Files:** `internal/mcp/tools_observability.go`

- [ ] Test: `get_usage(window: "24h")` returns aggregated token counts by provider/model.
- [ ] Test: `tail_logs(n: 20)` returns most recent log rows.
- [ ] Test: `get_request(id: N)` returns full request detail.
- [ ] Impl.
- [ ] Commit: `feat: mcp tools for usage and logs`

### Task 11: `init` subcommand
**Files:** `cmd/llm-gateway/init.go`

- [ ] Test: running `init` creates SQLite at `~/.config/llm-gateway/state.db` and prompts for `LLM_GATEWAY_TOKEN` if not set.
- [ ] Impl.
- [ ] Commit: `feat: init subcommand for first-run bootstrap`

### Task 12: goreleaser + GitHub Actions
**Files:** `.goreleaser.yaml`, `.github/workflows/test.yml`, `.github/workflows/release.yml`

- [ ] CI workflow runs `go test ./...` on push and PR.
- [ ] Release workflow triggers on tag push, runs goreleaser, publishes artifacts.
- [ ] Commit: `chore: ci + goreleaser config`

### Task 13: README with 5-minute setup
**Files:** `README.md`

- [ ] Step-by-step: download binary → `init` → start → register GLM provider via Cline → send first chat completion.
- [ ] Commit: `docs: 5-minute getting started`

---

## §5. Test Strategy

- **Unit:** Provider adapters tested against `httptest.NewServer` mock upstreams. Router tested with in-memory store.
- **Integration:** End-to-end HTTP request → provider mock → log row written. Uses real SQLite (in-memory `:memory:` for fast tests).
- **MCP:** Tools tested by feeding `tools/call` JSON-RPC requests to the stdio handler in-process.
- **No network in tests.** Real GLM/DeepSeek hit only via manual smoke test against `examples/smoke.sh`.

Test plan artifact will be elaborated by `/plan-eng-review` and written to `~/.gstack/projects/llm-gateway/panda-main-test-plan-<datetime>.md`.

---

## §6. Open Questions

(Re-stated from design doc, with current leanings.)

1. **路由策略 v0：** static `model→provider` 映射够。Auto-degrade defer to v0.3.
2. **Inbound auth：** single bearer token. Multi-token defer to v0.2.
3. **Streaming retry：** v0 不 retry. Document explicitly.
4. **mcp-go 选型：** Need 1h spike. Lean toward `mark3labs/mcp-go` (more stars, more recent activity as of 2026-05).
5. **Prompt logging 隐私：** Default = token counts + 200-char excerpt. Full prompt only if `LOG_FULL_PROMPT=1`.
6. **Model alias：** MVP yes (simple alias table). Rich routing (fallback chains, conditional rules) defer to v0.2.
7. **GLM auth：** Verify against current Zhipu docs (2026-05). Likely bearer; may need JWT.
8. **MCP transport：** stdio v0; add HTTP/SSE in v0.2.

---

## §7. Distribution

- **GitHub Releases**: 4 cross-compiled binaries via goreleaser on `git push origin v*` tag.
- **Container (defer to v0.2):** scratch image.
- **Package managers:** none in v0.

---

## §8. NOT in Scope (Boil-the-Lake guard)

These are intentionally deferred — flag scope-creep attempts back to this list:

- ❌ Web UI of any kind, including a "small admin page"
- ❌ Multi-user / RBAC / team accounts
- ❌ Billing / cost forecasting / quotas (other than displaying usage)
- ❌ Provider auto-discovery / health probing
- ❌ Rate limiting (handled upstream by providers themselves)
- ❌ Embeddings / image / audio endpoints (only chat completions in v0)
- ❌ Function calling beyond pass-through (don't intercept tool calls)
- ❌ Postgres / Redis / external state stores
- ❌ Kubernetes manifests / Helm charts

---

## §9. What Already Exists

Nothing — greenfield project. But heavy reuse from these ecosystems:
- `modernc.org/sqlite` — pure-Go SQLite, used widely (Caddy, etc.)
- `mark3labs/mcp-go` — community MCP server library
- Go stdlib `net/http` — battle-tested SSE support via `http.Flusher`
- Go stdlib `database/sql` — connection pooling, prepared statements
- `goreleaser` — standard Go release tool

Lessons borrowed from AI-native OSS:
- **LiteLLM:** provider abstraction shape (interface with `ChatComplete` + streaming variant) — copy the shape, don't copy the multi-tenant baggage.
- **OpenRouter:** model alias concept (their `meta/llama-3.1-70b:nitro` style) — adopted as simple alias table.
- **One API:** request log schema shape (token in/out, latency, status) — adopted directly.
- **Anthropic MCP examples:** stdio transport conventions, tool naming style (`snake_case`, verb-first).

---

## §10. Sections to be filled by review phases

- ✅ `## CEO Review` — appended below
- ✅ `## Eng Review` — appended below
- ✅ `## DX Review` — appended below
- ✅ `## Decision Audit Trail` — appended below

---

## CEO Review (Phase 1 — /plan-ceo-review via /autoplan)

### Setup

- **Mode** (auto-decided per autoplan P1+P2): **SELECTIVE EXPANSION**
- **Dual voices:** `[codex-unavailable]` (binary not installed); Claude subagent ran in foreground after plan write
- **Premise gate:** passed in office-hours D2 — premises P1–P6 carried forward verbatim
- **Subagent verdict:** RECONSIDER (with 5 substantive challenges)

### Step 0A. Premise Challenge

Already executed in office-hours D2. Premises P1–P6 locked by user. Subagent challenged P4 (GLM+DeepSeek scope) and P6 (AI-native thesis validation) — see User Challenges UC-2 and UC-5 below.

### Step 0B. Existing Code Leverage Map

Greenfield repo (single commit `d9abf05`). No existing code to leverage. Ecosystem leverage:

| Sub-problem | Existing code/lib |
|---|---|
| OpenAI-compat HTTP routes | `net/http` stdlib + manual handler |
| SSE streaming | `http.Flusher` stdlib pattern |
| MCP server | `mark3labs/mcp-go` (pending Open Q #4 spike) |
| SQLite | `modernc.org/sqlite` (pure Go, no CGO) |
| Provider adapter shape | LiteLLM's interface shape (not its code) |
| Model alias concept | OpenRouter's `alias:tag` model |
| Request log schema | One API's log table |
| Release artifacts | `goreleaser` |

### Step 0C. Dream State Mapping

```
CURRENT (2026-05):
  - User talks to GLM, DeepSeek, OpenAI directly from each coding agent.
  - Provider config duplicated across Cline cfg, Cursor cfg, Continue cfg, ad hoc scripts.
  - Zero usage visibility per provider.

THIS PLAN (v0.1.0, ~2 weeks out):
  - One static binary running locally. All coding agents point at it.
  - Adding a provider = ask Cline "add provider X with key Y" — one chat turn.
  - "How much GLM did I burn today" answerable without leaving chat.

12-MONTH IDEAL:
  - Multi-agent coordination over the gateway: scheduled task agents, eval agents, debugging agents share provider pool + policy table.
  - Read-only /ui dashboard (subagent recommended — see UC-3).
  - Cost-aware routing: "use cheapest model meeting quality bar" — gateway resolves.
  - 6+ providers; positioning as Chinese-provider-first (see UC-5).
  - HTTP/SSE MCP transport (currently v0.2; see UC-4).
```

**Delta this plan covers:** ~30% of the 12-month ideal — solid MVP wedge. Remainder sequenced in §3 v0.2 → v1.0.

### Step 0C-bis. Implementation Alternatives

Executed in office-hours D3. Approach A (Go single binary + mcp-go) chosen by user. Approach C (LiteLLM shim) explicitly rejected for P1/P2/P5/P6 conflicts. Subagent reopens C with a narrower framing — see UC-1.

### Step 0D. Mode-Specific Analysis (SELECTIVE EXPANSION)

**Hold scope on:** provider count (P4), no web UI for writes (P2), stdio MCP v0.1 (deferral), Go choice (D3).

**Cherry-pick expansions** (in blast radius + <1 day CC effort, auto-approved per P2 boil-the-lake):

- ✅ Pull encryption-at-rest into v0.1 (Finding A-3; ~50 LOC XChaCha20)
- ✅ Add §0 "Why now, why us" positioning paragraph (Finding A-12)
- ✅ Add Bun/TS rejection rationale to §Tech Stack (Finding A-13)
- ✅ Insert pre-Task 1 Wizard-of-Oz validation step (Finding A-10/UC-2 constructive part)
- ✅ Rename `internal/http/` → `internal/server/` (Finding A-6)
- ✅ Add empty-messages test, stream-cancel test (Findings A-4, A-7)

**Defer to TODOS.md:**

- Router fuzz/property test (A-8) → v0.3
- SSE load test (A-9) → v0.3
- Read-only UI in v0.2 (UC-3) → contingent on final-gate decision
- HTTP/SSE MCP transport in v0.1 vs v0.2 (UC-4) → contingent on final-gate decision
- Chinese-provider-first positioning rewrite (UC-5) → contingent on final-gate decision

### Step 0E. Temporal Interrogation

- **HOUR 1:** spike DeepSeek HTTP echo loop, no MCP, no DB. SSE round-trip verified.
- **HOUR 6:** Provider abstraction working; GLM adapter compiles.
- **DAY 1 END:** SQLite schema migrated, providers CRUD working.
- **DAY 3:** MCP stdio server with 3 tools (list_providers / add_provider / get_usage).
- **DAY 5:** All 10 MCP tools live; bearer auth wired.
- **DAY 8:** `git tag v0.1.0` triggers goreleaser; README + 5-min test verified.

12-month re-read: deliver ~30% of ideal. Acceptable MVP scoping.

### Step 0F. Mode Selection

**Mode: SELECTIVE EXPANSION** — hold the doctrinaire constraints (P2, P4) since user confirmed them; cherry-pick small expansions in blast radius. Subagent's larger challenges (UC-1, UC-3, UC-4, UC-5) and constructive validation (UC-2) escalate to the final gate.

---

### CEO Dual Voices — Consensus Table

```
═══════════════════════════════════════════════════════════════
  Dimension                            Claude  Codex  Consensus
  ──────────────────────────────────── ─────── ─────── ─────────
  1. Premises valid?                   DISAGREE   N/A   single-voice → UC-2, UC-5
  2. Right problem to solve?           DISAGREE   N/A   single-voice → UC-1
  3. Scope calibration correct?        DISAGREE   N/A   single-voice → UC-3, UC-4
  4. Alternatives sufficiently explored?DISAGREE  N/A   single-voice → A-13 auto
  5. Competitive/market risks covered? DISAGREE   N/A   single-voice → A-12 auto
  6. 6-month trajectory sound?         DISAGREE   N/A   split → A-3 auto + UC-3
═══════════════════════════════════════════════════════════════
Source: subagent-only. [codex-unavailable].
Single critical finding from one voice = flagged regardless (per autoplan).
```

---

### Review Sections 1–10

#### Section 1: Architecture Review

Examined: §2 component map, file structure, storage schema, MCP tool set. Architecture is simple: 1 binary, 2 servers (HTTP + MCP stdio), 1 store, 2 providers, 1 router. Coupling acceptable — provider/router/store boundaries are explicit interfaces. P1 (single-user) usefully constrains the design space.

- **A-1 (subagent, high):** stdio-only MCP precludes multi-agent management. → UC-4 (user gate).

#### Section 2: Error & Rescue Map

| Error mode | Detection | User-facing surface | Recovery |
|---|---|---|---|
| Upstream 401 | `provider.ErrUpstreamAuth` | `{error:{type:"invalid_api_key"}}` | Operator overwrites via MCP `add_provider` |
| Upstream 5xx | `provider.ErrUpstreamFail` | `{error:{type:"upstream_error"}}` | No auto-retry v0; agent re-fires manually |
| Upstream timeout | `context.DeadlineExceeded` | `408 Request Timeout` | Agent re-fires; v0.3 adds configurable timeouts |
| Stream interrupted mid-body | SSE write error | Partial SSE then disconnect | Client resets; documented in README |
| Bearer missing/wrong | http auth mw | `401 Unauthorized` | Operator checks `LLM_GATEWAY_TOKEN` env |
| No matching route | `router.ErrNoRoute` | `404` with hint | Operator adds alias or default via MCP |
| SQLite locked | `sql.ErrBusy` | `503 retry-after: 1` | Shouldn't happen (single process); log + alert |

- **A-2 (Claude, medium):** Stream-mid-failure UX fuzzy. → ACCEPT, add to README troubleshooting.

#### Section 3: Security & Threat Model

| Threat | Mitigation in v0.1 | Decision |
|---|---|---|
| Bearer leak | don't log token; recommend `.env` file 0600 | ACCEPT |
| SQLite file readable | `init` sets 0600 perms; document | ACCEPT |
| Plaintext API keys | **encrypt with master-key env var (subagent)** | **ACCEPT → pull into v0.1 as Task 4.5** |
| MITM upstream | HTTPS-only outbound; reject `http://` base_urls in `add_provider` | ACCEPT |
| Prompt injection in logs | truncate `prompt_excerpt` to 200 chars; `LOG_FULL_PROMPT` default off | ACCEPT |
| MCP unauthorized use | stdio = process boundary; P1 single-user acceptable | ACCEPT |

- **A-3 (subagent, medium → critical for OSS):** Plaintext keys is an HN-launch landmine. → ACCEPT, pull XChaCha20 (~50 LOC) into v0.1 Task 4.5.

#### Section 4: Data Flow & Interaction Edge Cases

| Edge case | Behavior | Coverage |
|---|---|---|
| `stream: true` then client disconnect | cancel upstream ctx; log status="abort" | Task 6 |
| `stream: false` + over-context | upstream 400 surfaced as-is | Task 6 |
| Empty messages array | **400 at handler — needs explicit test** | **A-4 ACCEPT, add to Task 1** |
| Multiple `tool_calls` in response | pass through verbatim | §8 — explicit non-goal |
| Concurrent same-alias updates | SQLite locks serialize | §3 storage |
| Provider duplicate name | upsert behavior — needs test | A-5 ACCEPT, add to Task 8 |

- **A-4 (Claude, medium):** Empty-messages test missing. ACCEPT.
- **A-5 (Claude, low):** tool_calls pass-through asserted in code but not docs. ACCEPT.

#### Section 5: Code Quality Review

Examined: file structure, naming, separation. Naming consistent (`tools_*.go`, `internal/<domain>/`). Files projected <300 LOC each. No DRY violations visible. Cyclomatic complexity untestable pre-implementation.

- **A-6 (Claude, low):** `internal/http/` collides with stdlib `net/http` in mental parsing. → ACCEPT, rename to `internal/server/`.

#### Section 6: Test Review

Test diagram (new UX flows × test coverage):

| Codepath | Unit | Integration | E2E |
|---|---|---|---|
| Bearer auth | ✅ Task 1 | ✅ Task 1 | manual |
| Provider chat blocking | ✅ Task 2/3 | — | manual smoke |
| Provider chat streaming | ✅ Task 2/3 | — | manual smoke |
| Router model→provider | ✅ Task 5 | ✅ Task 6 | — |
| MCP tool call | ✅ Tasks 7–10 | in-proc JSON-RPC | manual via Cline |
| Logging side-effect | — | ✅ Task 6 | — |
| Stream cancel mid-body | — | **MISSING — A-7 add** | — |
| Empty messages | — | **MISSING — A-4 add** | — |

- **A-7 (Claude, medium):** Stream cancel test. ACCEPT, add to Task 6.
- **A-8 (Claude, medium):** Router fuzz test. DEFER to v0.3.
- **A-9 (Claude, low):** SSE load test. DEFER to v0.3 (single-user).

#### Section 7: Performance Review

| Path | Budget | Risk |
|---|---|---|
| `/v1/chat/completions` blocking | <30ms gateway overhead | Low |
| Streaming first-byte overhead | <50ms | Low (SSE pass-through) |
| MCP `list_providers` | <10ms | Low (1 SQLite query) |
| MCP `get_usage` 24h window | <100ms | Med — needs `idx_logs_ts` (already in schema ✅) |
| Binary startup | <100ms | Low |

**Findings:** None. Indices cover queries; goroutine-per-request handles SSE without blocking.

#### Section 8: Observability & Debuggability Review

Examined: `request_logs` table, MCP `tail_logs` / `get_request` / `get_usage`. No `/metrics` v0.1 (deferred to v0.3).

- **A-10 (subagent, medium reframed):** "How do I debug a failed request" is MCP-only with ASCII tables. → ACCEPT — `prompt_excerpt` covers v0 80% case; full-prompt UI defers to v0.2 contingent on UC-3 final-gate decision.

#### Section 9: Deployment & Rollout Review

Examined: §7 Distribution + Task 12 (goreleaser + GHA). Single-binary release fits P5. 4 platforms covered. No container v0.1 (defer v0.2).

- **A-11 (Claude, low):** Released binary not verified on clean target machine. → ACCEPT, add `examples/smoke.sh` to Task 13.

#### Section 10: Long-Term Trajectory Review

Examined: §3 roadmap v0.1 → v0.2 → v0.3 → v1.0. Plausible sequencing. v0.2 adds encryption (now pulled to v0.1) + HTTP/SSE MCP; v0.3 metrics + providers; v1.0 API stability.

- **A-12 (subagent, high):** Competitive risk — OpenRouter/LiteLLM may ship comparable MCP control planes first. → ACCEPT, add §0 "Why now, why us" paragraph differentiating this gateway.
- **A-13 (subagent, medium):** Bun/TS rationale missing from §Tech Stack. → ACCEPT, 3-line rationale.

#### Section 11: Design & UX Review

**SKIPPED** — no UI scope (premise P2 forbids web UI for writes; final-gate decision pending on UC-3 read-only UI).

---

### Auto-Decided Findings Summary

Accept and integrate into plan now (13 items; updates queued for post-final-gate edit pass):

| # | Severity | Action | Principle |
|---|---|---|---|
| A-2 | medium | Document streaming non-retry in README | P1 completeness |
| A-3 | medium | Pull XChaCha20 encryption into v0.1 as Task 4.5 (~50 LOC) | P1 + P2 |
| A-4 | medium | Add empty-messages test to Task 1 | P1 |
| A-5 | low | Document tool_calls pass-through in §8 NOT in scope | P5 explicit |
| A-6 | low | Rename `internal/http/` → `internal/server/` | P5 |
| A-7 | medium | Add stream-cancel-mid-body test to Task 6 | P1 |
| A-8 | medium | Router fuzz test → defer v0.3 TODOS | P3 pragmatic |
| A-9 | low | SSE load test → defer v0.3 TODOS | P3 |
| A-10 | medium | `prompt_excerpt` covers v0 debug; full UI → UC-3 | P1 + P3 |
| A-11 | low | `examples/smoke.sh` smoke script → Task 13 | P1 |
| A-12 | high | Add §0 "Why now, why us" positioning | P1 |
| A-13 | medium | Tech Stack: 3-line Go-vs-TS rationale | P5 |

### User Challenges (NOT auto-decided — final gate)

**UC-1: LiteLLM-shim alternative** (Critical, subagent)
- What you said: Approach A (build new Go gateway). Approach C (LiteLLM shim) rejected in D3 for P1/P2/P5/P6 conflicts.
- What subagent recommends: "Spike 1 day on `litellm proxy + 200-line MCP shim`; if it works, kill 80% of the plan."
- Why: 90% of effort is on commodity proxy, 10% on actual differentiator (MCP control plane).
- What we might be missing: maybe Python runtime + LiteLLM's opinions are tolerable; user might not have tried `litellm proxy` recently.
- If we're wrong, the cost is: 8 工程日 building Go primitives LiteLLM provides.
- ⚠️ This is not a security or feasibility risk — it's a value-of-work question.

**UC-2: P6 (AI-native) requires UX validation** (High, subagent → constructive)
- What you said: P6 confirmed; Approach A chosen.
- What subagent recommends: 30-min Wizard-of-Oz comparing MCP-driven ops vs editing config.yaml before Task 1.
- Why: P6 is the project's thesis; if MCP doesn't clearly beat YAML editing, the AI-native premise collapses.
- This is constructive — a 30-min validation that may save 7 days.
- Recommend: ACCEPT as Task 0 unless user vetoes.

**UC-3: Allow read-only UI in v0.2** (High, subagent)
- What you said: 永远不要 web UI.
- What subagent recommends: reframe to "no UI for writes"; allow read-only dashboard in v0.2.
- Why: `tail_logs`, `get_usage`, `get_request` are inherently visual; agent rendering ASCII tables forever is friction.
- What we might be missing: maybe ASCII tables in chat are exactly what you want; the doctrine has aesthetic + philosophical value.
- If we're wrong, the cost is: long-term debug friction. But: amputates the AI-native purity argument.

**UC-4: HTTP/SSE MCP transport in v0.1 (not v0.2)** (High, subagent)
- What you said: Open Question #8 — stdio only in v0; HTTP/SSE in v0.2.
- What subagent recommends: move HTTP/SSE into v0.1 (~½ day in mcp-go).
- Why: stdio = one agent-process per gateway-process. Multi-agent value prop blocked from day 1.
- What we might be missing: maybe single-agent (Cline only) is fine for personal MVP; multi-agent is later concern.
- If we're wrong, the cost is: re-architect MCP layer when adding Cursor/Continue parallel use.

**UC-5: Reframe P4 as Chinese-provider-first** (Medium, subagent)
- What you said: P4 = GLM + DeepSeek (no positioning statement).
- What subagent recommends: explicitly position as "Chinese-provider-first gateway" — a defensible wedge vs LiteLLM's global-flat coverage.
- Why: differentiation against LiteLLM; underserved audience (Chinese-provider users).
- What we might be missing: maybe positioning is premature for a personal tool; you may not want OSS user-acquisition burden.
- If we're wrong, the cost is: undifferentiated project competing head-on with LiteLLM if it goes public.

---

### CEO Completion Summary

| Element | Status |
|---|---|
| Mode | SELECTIVE EXPANSION (auto-decided) |
| Premise gate | Passed in office-hours D2 |
| Dual voices | Claude subagent ran; Codex `[codex-unavailable]` |
| Subagent verdict | RECONSIDER (5 substantive challenges) |
| Sections 1–10 reviewed | Complete; Section 11 skipped (no UI) |
| Findings | 13 auto-decided + 5 user challenges |
| Error & Rescue Registry | Written in Section 2 |
| Failure Modes Registry | Section 2 + 4 |
| NOT in scope additions | tool_calls pass-through (A-5) |
| Dream state delta | ~30% of 12-month ideal |
| Status | **DONE_WITH_CONCERNS** — 5 user challenges await final gate |

---

---

## Eng Review (Phase 3 — /plan-eng-review via /autoplan)

### Setup

- **Dual voices:** `[codex-unavailable]`; Claude eng subagent ran foreground after CEO review
- **Subagent verdict:** SHIP-WITH-FIXES (19 findings, all technical; none are user-direction challenges)
- **Test plan artifact:** `~/.gstack/projects/llm-gateway/panda-main-test-plan-20260513-040833.md` (~63 tests for v0.1.0)
- **Findings classification:** All 19 (F-1 through F-19) are architectural/test/security/operational — auto-decided ACCEPT per P1 (completeness) + P5 (explicit over clever). Zero user challenges.

### Scope Challenge (Step 0)

Read against actual plan §1–§8. The plan is greenfield, well-scoped, no existing code to compare against. The eng review's job here is to find missing surfaces, not contest scope (CEO phase already did that).

**Complexity check result:** Plan covers the architectural what; subagent found 19 missing how-details. Sequencing of F-fixes:

- **Pre-Task 1:** F-1/F-18 (shutdown coordination) → updates Task 1 architecture spec
- **Pre-Task 2/3:** F-15 (GLM spike) — 2h live test before adapter implementation
- **Task 4:** F-4 (DSN), F-9 (versioned migration + seeded re-run), F-13 (parameterized queries)
- **Task 4.5 (new):** A-3 encryption-at-rest + F-17 cross-compile sanity
- **Task 5:** F-2 (cache semantics), F-11 (orphaned alias), F-5 (namespace collision)
- **Task 6:** F-3 (provider snapshot), F-7 (ctx propagation), F-19 (async log writer)
- **Task 7:** F-16 (mcp-go budget 1d not 1h)
- **Task 8/9:** F-10 (arg validation), F-12 (SSRF reject)
- **Task 11:** F-6 (init idempotency), F-14 (token file)
- **Task 12/13:** F-17 (cross-compile CI), A-11 (smoke.sh)

### Eng Dual Voices — Consensus Table

```
═══════════════════════════════════════════════════════════════
  Dimension                            Claude  Codex  Consensus
  ──────────────────────────────────── ─────── ─────── ─────────
  1. Architecture sound?               ISSUES    N/A   single-voice: F-1, F-2, F-3, F-18
  2. Test coverage sufficient?         ISSUES    N/A   single-voice: F-8, F-9, F-10, F-11
  3. Performance risks addressed?      ISSUES    N/A   single-voice: F-4, F-19
  4. Security threats covered?         ISSUES    N/A   single-voice: F-12, F-13, F-14
  5. Error paths handled?              ISSUES    N/A   single-voice: F-5, F-6, F-7
  6. Deployment risk manageable?       ISSUES    N/A   single-voice: F-15, F-16, F-17
═══════════════════════════════════════════════════════════════
Source: subagent-only. [codex-unavailable].
All 19 findings auto-accepted (technical, not user-direction).
```

### Section 1: Architecture Review — Augmented ASCII Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                   llm-gateway (1 process)                       │
│                                                                 │
│         ┌──────── SIGINT/SIGTERM ────────┐                      │
│         │                                ▼                      │
│         │      ┌────────────────────────────────┐               │
│         │      │  Lifecycle owner (errgroup +   │               │
│         │      │   root context with cancel)    │ ← F-1/F-18    │
│         │      └─────────────┬──────────────────┘               │
│         │                    │                                  │
│         │           ┌────────┼────────┬─────────────┐           │
│         │           ▼        ▼        ▼             ▼           │
│         │      ┌────────┐┌──────┐┌──────────┐┌────────────┐    │
│ inbound─┼─────►│ HTTP   ││ MCP  ││ AsyncLog ││ SQLite     │    │
│ (OpenAI │      │ server ││ stdio││  writer  ││ (WAL,      │    │
│ -compat)│      │        ││server││ (F-19)   ││ busy_to=5s,│    │
│         │      └────┬───┘└───┬──┘└─────┬────┘│ F-4)       │    │
│         │           │        │         │     └────┬───────┘    │
│         │           ▼        ▼         │          ▲            │
│         │      ┌────────────────┐      │          │            │
│         │      │ Auth (bearer)  │      │          │            │
│         │      │ (F-14 file)    │      │          │            │
│         │      └────┬───────────┘      │          │            │
│         │           ▼                  │          │            │
│         │      ┌────────────────┐      │          │            │
│         │      │ Router         │──────┼──────────┤            │
│         │      │ (reads store   │      │          │            │
│         │      │  every req,    │      │          │            │
│         │      │  snapshots     │      │          │            │
│         │      │  provider per  │      │          │            │
│         │      │  request ctx;  │      │          │            │
│         │      │  F-2, F-3)     │      │          │            │
│         │      └────┬───────────┘      │          │            │
│         │           ▼                  │          │            │
│         │      ┌────────────────┐      │          │            │
│         │      │ Provider       │      │          │            │
│         │      │ (DeepSeek/GLM, │      │          │            │
│         │      │  SSE normalize │      │          │            │
│         │      │  F-15;         │      │          │            │
│         │      │  upstream HTTP │      │          │            │
│         │      │  uses          │      │          │            │
│         │      │  r.Context()   │      │          │            │
│         │      │  F-7)          │      │          │            │
│         │      └────┬───────────┘      │          │            │
│         │           │                  │          │            │
│         │           └──→ log event ────┘          │            │
│         │                                         │            │
│         │      ┌────────────────────────┐         │            │
│         │      │  MCP tools (10)        │─────────┘            │
│         │      │  - SSRF check (F-12)   │                      │
│         │      │  - JSON Schema (F-10)  │                      │
│         │      │  - dup-name err (F-5)  │                      │
│         │      └────────────────────────┘                      │
│         │                                                       │
│         └───── Shutdown sequence (F-1/F-18) ───────             │
│                1. Stop HTTP accept                              │
│                2. Close MCP stdin reader                        │
│                3. Wait in-flight (timeout 30s)                  │
│                4. Drain AsyncLog channel                        │
│                5. db.Close()                                    │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

**Section 1 findings:**
- F-1: Lifecycle owner with errgroup + root ctx → ACCEPT, becomes Task 1 architecture requirement.
- F-2: Router cache invalidation semantics → ACCEPT, document "Router reads from store every request, snapshots provider data into request ctx."
- F-3: Provider snapshot at resolve-time → ACCEPT, Task 5 spec.
- F-18: Shutdown sequence (5 steps above) → ACCEPT, Task 1 + Task 7 + Task 11.

### Section 2: Code Quality Review

Examined: file structure, naming, separation. Subagent found no DRY/naming/complexity violations. (CEO phase already flagged `internal/http/` → `internal/server/`; carried forward.)

**Section 2 findings:** none beyond CEO phase A-6.

### Section 3: Test Review

**Test diagram and full coverage matrix** moved to artifact: `~/.gstack/projects/llm-gateway/panda-main-test-plan-20260513-040833.md`.

Highlights (new tests added by this review):
- F-8: Golden-file SSE replay (byte-level wire format)
- F-9: Seeded SQLite migration safety; PRAGMA user_version
- F-10: MCP arg validation (unknown kind, empty name, path-traversal alias)
- F-11: Orphaned alias → ErrNoRoute
- F-12: SSRF reject for private/loopback/link-local base_url
- F-15: GLM live spike (2h, pre-Task 3) + chunk normalization tests
- F-19: Async log writer backpressure
- F-1/F-18: Shutdown integration tests (drain + MCP close + DB close)

**Test count for v0.1.0 (target):** ~63 automated + 1 manual smoke script.

**Section 3 findings:** F-8, F-9, F-10, F-11 all ACCEPT. See artifact.

### Section 4: Performance Review

**Section 4 findings:**
- F-4: DSN settings `_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL` + `SetMaxOpenConns` tuning → ACCEPT, Task 4 spec.
- F-19: Async log writer to decouple HTTP latency from log insert → ACCEPT, Task 6.

### Section 5: Security & Threat Model

CEO phase covered this; eng adds SSRF + SQL injection defense:

**Section 5 findings:**
- F-12: SSRF allowlist for `base_url` (scheme=https + reject RFC1918/loopback/link-local + optional `allowed_hosts` env) → ACCEPT, Task 8.
- F-13: Mandate `database/sql` `?` placeholders everywhere; static-check or convention → ACCEPT, Task 4.
- F-14: Token file 0600 instead of env var (env var as override) → ACCEPT, Task 11.

### Section 6: Error Paths

**Section 6 findings:**
- F-5: Duplicate provider name → return structured `already_exists` (don't crash) → ACCEPT, Task 8.
- F-6: `init` idempotent (refuse to clobber without `--force`); `start` fails fast if token unset → ACCEPT, Task 11.
- F-7: Upstream HTTP request constructed with `r.Context()` (not `context.Background()`) → ACCEPT, Tasks 2/3 spec.

### Section 7: Deployment / Release

**Section 7 findings:**
- F-15: GLM live spike 2h pre-Task 3 + normalize layer in `internal/provider/common.go` → ACCEPT (schedule eater warning).
- F-16: mcp-go integration budget 1 day (not 1h); documented fallback to `metoro-io/mcp-golang` → ACCEPT, Task 7.
- F-17: Cross-compile CI matrix + pin `modernc.org/sqlite` to known-good version → ACCEPT, Task 12.

### Section 8: Concurrency / Lifecycle

**Section 8 findings:**
- F-1/F-18: see Section 1 above.
- F-19: Async log writer with buffered channel (size 1024), `drop-oldest` on overflow, documented at-most-once log semantics → ACCEPT, Task 6.

### Auto-Decided Findings Summary (Eng)

All 19 findings ACCEPT. Updated task list incorporates fixes; see test plan artifact for coverage.

| # | Severity | Action | Task |
|---|---|---|---|
| F-1 | critical | errgroup + root ctx + signal handling | Task 1 |
| F-2 | high | Router reads store every request | Task 5 |
| F-3 | high | Provider snapshot in request ctx | Task 5/6 |
| F-4 | medium | SQLite DSN: WAL + busy_timeout=5s + NORMAL | Task 4 |
| F-5 | high | `add_provider` dup name → structured error | Task 8 |
| F-6 | medium | `init` idempotent; `start` fails fast on missing token | Task 11 |
| F-7 | medium | Upstream HTTP uses `r.Context()` | Task 2/3 |
| F-8 | high | Golden-file SSE replay tests | Task 2/3 |
| F-9 | high | Versioned schema + seeded migration test | Task 4 |
| F-10 | medium | MCP tool arg validation tests | Task 8/9 |
| F-11 | medium | Orphaned alias → ErrNoRoute test | Task 5 |
| F-12 | high | SSRF allowlist (https + non-private only) | Task 8 |
| F-13 | medium | Parameterized SQL queries everywhere | Task 4 |
| F-14 | low | Token file 0600; env var override | Task 11 |
| F-15 | high | 2h GLM live spike + chunk normalization | pre-Task 3 |
| F-16 | medium | mcp-go: budget 1 day; document fallback | Task 7 |
| F-17 | low | Cross-compile CI + pin sqlite version | Task 12 |
| F-18 | critical | 5-step shutdown sequence | Task 1/7/11 |
| F-19 | high | Async log writer + drop-oldest backpressure | Task 6 |

### Eng Completion Summary

| Element | Status |
|---|---|
| Dual voices | Subagent ran; Codex `[codex-unavailable]` |
| Subagent verdict | SHIP-WITH-FIXES (19 technical findings) |
| Architecture ASCII diagram | Produced (Section 1) |
| Test diagram + coverage matrix | Artifact at `~/.gstack/projects/llm-gateway/panda-main-test-plan-20260513-040833.md` |
| Test count target | ~63 automated + 1 manual smoke |
| Critical-path gates | F-8, F-1/F-18, F-12, F-15, A-3, cross-compile smoke |
| Findings | 19 all auto-decided ACCEPT (none are user challenges) |
| Failure modes registry | F-1 to F-19 above, mapped to tasks |
| Status | **DONE** — eng review clean; 0 user challenges, 19 technical fixes queued for plan edit pass |

---

---

## DX Review (Phase 3.5 — /plan-devex-review via /autoplan)

### Setup

- **DX scope detection:** TRUE — product is a developer tool (CLI binary + MCP server consumed by coding agents). Primary user **is** an AI agent (plus the human installing the binary).
- **Mode (auto-decided):** DX POLISH
- **Personas:** (1) Human developer who installs and operates; (2) AI agent (Cline / Claude Code / Cursor) which is both consumer and operator (P6 thesis).
- **Dual voices:** `[codex-unavailable]`; Claude DX subagent ran in foreground
- **Subagent verdict:** DX score 6.5/10; TTHW currently 12–15 min vs 5-min target; 3 critical blockers before Task 1
- **Findings:** 15 total, all auto-decided ACCEPT (technical/process, no user-direction changes)

### Step 0: DX Scope Assessment

**Product type:** developer tool with dual primary users.

**Developer journey (9 stages):**

| # | Stage | Current friction | Target |
|---|---|---|---|
| 1 | Discover | (out of scope — assumes user found GitHub repo) | — |
| 2 | Install | `curl -L .../llm-gateway-darwin-arm64 -o llm-gateway && chmod +x` | <1 min |
| 3 | Bootstrap | `llm-gateway init` — currently no MCP-config emission | <30 sec |
| 4 | Wire to coding agent | **MISSING** in plan — user must hand-edit Cline cfg | <1 min (auto-emit snippet) |
| 5 | Start gateway | `llm-gateway start` | <5 sec |
| 6 | First provider | "Hey Cline, add provider GLM with key X" via MCP | <30 sec |
| 7 | First chat completion | Any LLM call from Cline routes through gateway | immediate |
| 8 | Verify it worked | "Show last 5 requests" via MCP | <10 sec |
| 9 | Stop / restart | Ctrl-C or `llm-gateway stop` | immediate |

**TTHW current:** 12–15 min. **TTHW target:** <5 min. **Delta** comes from Stage 4 friction (no MCP-config emission) + Stage 6 fragility (no `--provider` flag fallback).

### DX Dual Voices — Consensus Table

```
═══════════════════════════════════════════════════════════════
  Dimension                            Claude  Codex  Consensus
  ──────────────────────────────────── ─────── ─────── ─────────
  1. Getting started < 5 min?          NO        N/A   single-voice: F-DX-01, F-DX-02
  2. API/CLI naming guessable?         MOSTLY    N/A   single-voice: F-DX-03, F-DX-04
  3. Error messages actionable?        NO        N/A   single-voice: F-DX-05, F-DX-06
  4. Docs findable & complete?         PARTIAL   N/A   single-voice: F-DX-07, F-DX-08
  5. Upgrade path safe?                AMBIGUOUS N/A   single-voice: F-DX-09, F-DX-10
  6. Dev environment friction-free?    NO        N/A   single-voice: F-DX-11, F-DX-12
═══════════════════════════════════════════════════════════════
Source: subagent-only. [codex-unavailable].
```

### Developer Empathy Narrative (first-person)

> "I downloaded the binary. I ran `init` — it asked for a token, I generated one with `openssl rand -hex 32`, stored it in `~/.config/llm-gateway/token`. I ran `start` — it's listening on `:7421`. Now what?
>
> The README says 'talk to your gateway via Cline.' But Cline doesn't know my gateway exists. I have to hand-edit `~/.config/cline/mcp_settings.json` and add an entry. The format isn't shown in the README. I find a Cline blog post; the JSON shape uses different keys than I expected. After 5 minutes of trial and error, Cline can see the gateway.
>
> Now I tell Cline: 'Add my GLM provider, the key is abc123.' Cline tries `add_provider(name='glm', kind='glm', base_url=??)`. What's the base_url for GLM? Not in any error message. Cline guesses `https://open.bigmodel.cn`. Wrong (it's `/api/paas/v4`). I correct it. Provider added.
>
> 'Send me a completion using glm-4-flash.' Cline tries — gets `404 no route`. Why? Because I never set an alias or default. The error doesn't tell me that. I dig through the MCP tool list and find `set_default_provider`. Set it. Try again. Works.
>
> Total: 18 minutes. Not the 5 I was promised."

This narrative drives the 15 findings below.

### Sections 1–8 (DX Passes)

#### Section 1: TTHW (Time to Hello World)

**Score: 4/10 → 9/10 after fixes**

- **F-DX-01 (critical, both):** `init` doesn't print MCP client config snippet → user fumbles at Stage 4. → ACCEPT: `init` prints copy-pastable JSON for `claude_desktop_config.json` AND `cline_mcp_settings.json` and a `curl` smoke-test (so user can verify gateway without MCP first).
- **F-DX-02 (high, human):** No first-class "register provider via flag" path; user is locked into MCP-via-agent for first request. → ACCEPT: add `llm-gateway init --provider glm --api-key $GLM_KEY` flag. Doesn't violate P2 (init is already a CLI subcommand).
- **F-DX-15 (high, both):** 5-min test omits MCP client wiring. → ACCEPT: README defines the bar as "5-min happy path after MCP wiring + `init` auto-emits config snippet." Add verification step.

#### Section 2: API/CLI Naming Guessability

**Score: 7/10 → 9/10**

- **F-DX-03 (medium, AI agent):** `set_model_alias` upserts but name doesn't signal it; `get_request` ambiguous (HTTP req? log entry?). → ACCEPT: rename `get_request` → `get_request_log`; document upsert semantics in `set_model_alias` description.
- **F-DX-04 (high, AI agent):** `kind` enum unstated; agent might guess "GLM"/"zhipu"/"glm". → ACCEPT: enumerate valid `kind` values in tool description AND in JSON Schema (`"enum": ["glm", "deepseek"]`) so constrained-generation agents pick correctly.

#### Section 3: Error Messages Actionable

**Score: 4/10 → 9/10**

- **F-DX-05 (high, both):** `ErrNoRoute` says "404 with hint" — hint shape undefined. Agent can't pick next action. → ACCEPT: structured error body:
  ```json
  {"error":{"type":"no_route","message":"No alias or default for model 'gpt-4'","fix":"Call set_model_alias(alias='gpt-4', provider='glm-prod', upstream_model='glm-4-flash') OR set_default_provider"}}
  ```
  Same pattern for SSRF reject (F-12), dup-name (F-5), missing token (F-6), unknown provider kind (F-DX-04).
- **F-DX-06 (medium, both):** Upstream 401 doesn't say WHICH provider's key is bad. → ACCEPT: include `provider_name` in error body.

#### Section 4: Documentation Findability

**Score: 5/10 → 9/10**

- **F-DX-07 (high, both):** No "Cheatsheet of prompts to paste to your agent" in README. For the AI-native thesis (P6) this is the killer feature. → ACCEPT: README §"Talk to your gateway" with 8–10 ready prompts:
  - *"Add my GLM key abc123 as 'glm-prod' and make it the default"*
  - *"Show me my last 20 requests"*
  - *"How much GLM did I use today?"*
  - *"Set 'fast' as an alias for DeepSeek's deepseek-chat model"*
  - *"What providers do I have configured?"*
  - *"Tail the last 5 errors"*
  - *"Remove the 'glm-test' provider"*
  - *"Show me request #42 in full"*

  This same content doubles as the `gateway://how-to` MCP prompt resource (F-DX-13) — single source.
- **F-DX-08 (medium, both):** No troubleshooting section. → ACCEPT: README §"When things break" — stream-mid-failure (A-2), SQLite locked (rare), token mismatch (F-6), provider 401 (F-DX-06).

#### Section 5: Upgrade Path

**Score: 5/10 → 8/10**

- **F-DX-09 (critical, human):** Plan has contradiction — §3 v0.1 line in plan still says "API key encryption at rest — file-permission-protected, deferred to v0.2" but CEO phase A-3 pulled encryption INTO v0.1. → ACCEPT: edit §3 of plan to mark encryption-at-rest as v0.1 included. Add `user_version` PRAGMA as migration contract from day 1. Add `llm-gateway migrate` subcommand to roadmap for v0.2.
- **F-DX-10 (medium, AI agent):** No MCP tool schema deprecation policy. Agents that hard-code tool names break on rename. → ACCEPT: state the policy explicitly: "MCP tool names and required arg names are stable within minor versions; new optional args allowed; removals require major bump."

#### Section 6: Escape Hatches

**Score: 4/10 → 8/10**

- **F-DX-11 (high, human):** Only 2 env vars exposed (`LLM_GATEWAY_TOKEN`, `LOG_FULL_PROMPT`). HTTP listen address is hard-coded `:7421`. No request timeout knob, log retention, MCP read-only mode. → ACCEPT: standardize env vars:

  | Env var | Default | Purpose |
  |---|---|---|
  | `LLM_GATEWAY_TOKEN` | required | Bearer token |
  | `LLM_GATEWAY_ADDR` | `:7421` | HTTP listen address |
  | `LLM_GATEWAY_TIMEOUT` | `120s` | Per-request timeout |
  | `LLM_GATEWAY_LOG_RETENTION_DAYS` | `30` | Auto-purge old request_logs |
  | `LLM_GATEWAY_LOG_FULL_PROMPT` | `0` | Log full prompts vs 200-char excerpts |
  | `LLM_GATEWAY_MCP_READONLY` | `0` | Disable destructive MCP tools |
  | `LLM_GATEWAY_DB_PATH` | `~/.config/llm-gateway/state.db` | SQLite location |
  | `LLM_GATEWAY_MASTER_KEY` | required (per A-3) | Encryption-at-rest key |

- **F-DX-12 (medium, AI agent):** No way for agent to distinguish read-only vs mutating MCP tools. → ACCEPT: tool description prefixes:
  - `[read-only]` `list_providers`, `list_model_aliases`, `get_usage`, `tail_logs`, `get_request_log`, `diagnose`
  - `[mutates state]` `add_provider`, `remove_provider`, `set_default_provider`, `set_model_alias`, `remove_model_alias`

  Then `LLM_GATEWAY_MCP_READONLY=1` hides the `[mutates state]` set.

#### Section 7: Agent UX (Differentiated Dimension)

**Score: 6/10 → 9/10**

- **F-DX-13 (critical, AI agent):** `gateway://how-to` MCP prompt resource is mentioned once in plan §2 but has no implementation task. It's THE agent onboarding doc. → ACCEPT: add **Task 7.5** before tool implementations:

  > **Task 7.5: Implement `gateway://how-to` MCP prompt resource**
  > - Returns a markdown cheatsheet with: GLM/DeepSeek base_urls + key formats, common alias patterns, decision tree ("user asked X → call tool Y"), example tool calls with verbatim shapes.
  > - This is the same content as README §"Talk to your gateway" — embed via `//go:embed gateway-howto.md`.
  > - Test: agent calls `prompts/get` for `gateway://how-to` and receives the markdown.

- **F-DX-14 (high, AI agent):** Agent has no debug loop when MCP tool fails. No "try `tail_logs`" guidance. → ACCEPT: add **`diagnose`** MCP tool to the tool set (11 tools total now). Returns `{token_set: bool, db_writable: bool, providers_count: int, last_upstream_error: string|null, gateway_pid: int, gateway_uptime_s: int}`. Reference this tool in every structured error response.

#### Section 8: Onboarding Friction

**Score: 5/10 → 9/10**

- **F-DX-15 (high, both):** Covered in Section 1 above.

### DX Scorecard

| Dimension | Current | Post-fix |
|---|---|---|
| TTHW | 4 | 9 |
| API/CLI naming | 7 | 9 |
| Error messages | 4 | 9 |
| Documentation | 5 | 9 |
| Upgrade path | 5 | 8 |
| Escape hatches | 4 | 8 |
| Agent UX | 6 | 9 |
| Onboarding | 5 | 9 |
| **Overall** | **5.0** | **8.75** |

(Subagent's headline 6.5 was an overall feel; section-by-section it averages 5.0.)

### DX Implementation Checklist

Auto-decided ACCEPTs (15 items), to be folded into plan §2/§4/§7 in post-final-gate edit pass:

| # | Severity | Action | Task |
|---|---|---|---|
| F-DX-01 | crit | `init` prints MCP config snippets + curl smoke-test | Task 11 |
| F-DX-02 | high | `init --provider glm --api-key X` flag | Task 11 |
| F-DX-03 | med | Rename `get_request` → `get_request_log`; document upsert in `set_model_alias` | Tasks 9/10 |
| F-DX-04 | high | `kind` enum in JSON Schema + tool description | Task 8 |
| F-DX-05 | high | Structured error body with `fix` field | Tasks 1/5/6/8 |
| F-DX-06 | med | Include `provider_name` in upstream-error body | Task 6 |
| F-DX-07 | high | README cheatsheet of 8–10 prompts | Task 13 |
| F-DX-08 | med | README troubleshooting section | Task 13 |
| F-DX-09 | crit | Correct §3 v0.1 line re encryption; add `user_version` PRAGMA | §3 + Task 4 |
| F-DX-10 | med | State MCP tool stability policy | §3 |
| F-DX-11 | high | Standardize 8 env vars (table above) | All tasks |
| F-DX-12 | med | `[read-only]` / `[mutates state]` prefixes; `MCP_READONLY` gates writers | Tasks 8/9/10 |
| F-DX-13 | crit | New Task 7.5: `gateway://how-to` prompt resource | new Task 7.5 |
| F-DX-14 | high | New `diagnose` MCP tool (11th tool); referenced in errors | Task 10 |
| F-DX-15 | high | Reframe 5-min test as post-MCP-wiring; add verification step | Task 13 |

### DX Completion Summary

| Element | Status |
|---|---|
| DX scope detected | YES (developer tool, agent-as-user) |
| Mode | DX POLISH (auto-decided) |
| Dual voices | Subagent ran; Codex `[codex-unavailable]` |
| Subagent verdict | 6.5/10 overall; section-by-section 5.0 → 8.75 post-fix |
| Developer journey map | 9-stage table above |
| Developer empathy narrative | First-person 18-min walkthrough above |
| DX scorecard | 8 dimensions scored before/after |
| TTHW assessment | 12–15 min current → <5 min target after F-DX-01/02/15 |
| Findings | 15 all auto-decided ACCEPT |
| New tasks added | Task 7.5 (`gateway://how-to`), expanded Tasks 11/13 |
| New MCP tool added | `diagnose` (brings count to 11) |
| Status | **DONE** — DX review complete; 0 user challenges; 15 technical/process improvements queued |

---

---

## Decision Audit Trail (autoplan Phase 4)

**Important caveat:** Codex was `[codex-unavailable]` throughout. All "dual voices" reduced to single-voice Claude subagent. Per autoplan rules, single critical findings from one voice are flagged regardless — but the cross-model consensus check that normally validates findings was absent. Treat subagent findings as "best independent take" rather than "two agents converged."

### Decisions Made: 46 total

- **42 auto-decided** (12 CEO + 19 Eng + 15 DX – 1 deduplication for UC-2 which is constructive add → auto-accept = 45 auto, but A-1 = UC-4 is a duplicate counted once)
- **0 taste decisions** (no close-approach forks emerged at sub-section level beyond the architecture choice already decided in D3)
- **4 user challenges** (UC-1, UC-3, UC-4, UC-5) — gated to user at this approval step
- **1 reframed UC** (UC-2 P6 validation Wizard-of-Oz) auto-accepted as constructive add, not a direction change

### Auto-Decided Decision Log

#### CEO Phase (12 auto-decided)

| # | Decision | Classification | Principle | Rationale |
|---|---|---|---|---|
| A-2 | Document streaming non-retry in README | mechanical | P1 completeness | User-visible behavior must be documented |
| A-3 | Pull XChaCha20 encryption into v0.1 as Task 4.5 | mechanical | P1 + P2 boil-the-lake | OSS HN landmine; ~50 LOC fix; blast-radius small |
| A-4 | Add empty-messages test to Task 1 | mechanical | P1 | Missing edge case test |
| A-5 | Document tool_calls pass-through in §8 NOT in scope | mechanical | P5 explicit | Removes ambiguity for future contributors |
| A-6 | Rename `internal/http/` → `internal/server/` | mechanical | P5 | Reduces stdlib name collision in mental parsing |
| A-7 | Add stream-cancel-mid-body test to Task 6 | mechanical | P1 | Missing edge case test |
| A-8 | Router fuzz test → defer v0.3 | mechanical | P3 pragmatic | Single-user scope; diminishing returns v0 |
| A-9 | SSE load test → defer v0.3 | mechanical | P3 | Single-user scope; not the bottleneck v0 |
| A-10 | prompt_excerpt covers v0 debug; full UI → UC-3 | mechanical | P1 + P3 | Excerpt is the 80% case |
| A-11 | `examples/smoke.sh` smoke script to Task 13 | mechanical | P1 | Release-artifact verification gap |
| A-12 | Add §0 "Why now, why us" positioning | mechanical | P1 | Competitive risk acknowledgment |
| A-13 | Tech Stack: 3-line Go-vs-TS rationale | mechanical | P5 | Removes "why not Bun?" anonymous question |

#### Eng Phase (19 auto-decided)

| # | Decision | Classification | Principle |
|---|---|---|---|
| F-1 | errgroup + root ctx + SIGINT handling (Task 1) | mechanical | P1 + P5 |
| F-2 | Router reads store every request (Task 5) | mechanical | P5 explicit |
| F-3 | Provider snapshot into request ctx (Task 5/6) | mechanical | P5 |
| F-4 | SQLite DSN: WAL + busy_timeout=5s + NORMAL (Task 4) | mechanical | P5 |
| F-5 | `add_provider` dup name → structured error (Task 8) | mechanical | P5 |
| F-6 | `init` idempotent; `start` fails fast on missing token | mechanical | P1 |
| F-7 | Upstream HTTP uses `r.Context()` | mechanical | P5 |
| F-8 | Golden-file SSE replay tests | mechanical | P1 |
| F-9 | Versioned schema + seeded migration test | mechanical | P1 |
| F-10 | MCP tool arg validation tests | mechanical | P1 |
| F-11 | Orphaned alias → ErrNoRoute test | mechanical | P1 |
| F-12 | SSRF allowlist (https + non-private only) (Task 8) | mechanical | P1 + security |
| F-13 | Parameterized SQL queries everywhere | mechanical | P5 + security |
| F-14 | Token file 0600; env var override | mechanical | P5 |
| F-15 | 2h GLM live spike + chunk normalization (pre-Task 3) | mechanical | P3 pragmatic |
| F-16 | mcp-go: budget 1 day; document fallback | mechanical | P3 |
| F-17 | Cross-compile CI + pin sqlite version | mechanical | P1 + P5 |
| F-18 | 5-step shutdown sequence (Task 1/7/11) | mechanical | P1 + P5 |
| F-19 | Async log writer + drop-oldest backpressure (Task 6) | mechanical | P1 + P3 |

#### DX Phase (15 auto-decided)

| # | Decision | Classification | Principle |
|---|---|---|---|
| F-DX-01 | `init` prints MCP config snippets + curl smoke (Task 11) | mechanical | P1 |
| F-DX-02 | `init --provider glm --api-key X` flag (Task 11) | mechanical | P5 |
| F-DX-03 | Rename `get_request` → `get_request_log`; doc upsert | mechanical | P5 |
| F-DX-04 | `kind` enum in JSON Schema + tool description | mechanical | P5 |
| F-DX-05 | Structured error body with `fix` field | mechanical | P1 |
| F-DX-06 | Include `provider_name` in upstream-error body | mechanical | P1 |
| F-DX-07 | README cheatsheet of 8–10 prompts (Task 13) | mechanical | P1 |
| F-DX-08 | README troubleshooting section (Task 13) | mechanical | P1 |
| F-DX-09 | Correct §3 v0.1 line re encryption; `user_version` PRAGMA | mechanical | P5 |
| F-DX-10 | State MCP tool stability policy in §3 | mechanical | P5 |
| F-DX-11 | Standardize 8 env vars | mechanical | P5 |
| F-DX-12 | `[read-only]` / `[mutates state]` prefixes; `MCP_READONLY` | mechanical | P5 + security |
| F-DX-13 | New Task 7.5: `gateway://how-to` prompt resource | mechanical | P1 |
| F-DX-14 | New `diagnose` MCP tool (11th tool) | mechanical | P1 (boil-the-lake within scope) |
| F-DX-15 | Reframe 5-min test post-MCP-wiring + verification | mechanical | P5 |

#### Constructive Add (1, reframed from UC-2)

| # | Decision | Classification | Principle |
|---|---|---|---|
| UC-2→A | Add Task 0: 30-min Wizard-of-Oz validating P6 thesis (MCP vs YAML editing) before Task 1 | constructive | P3 pragmatic + P1 |

### Cross-Phase Themes

Where 2+ phases independently flagged the same concern (high-confidence signal):

**Theme 1: "Structured errors with fix hints"** — flagged by CEO (A-2 documents non-retry), Eng (F-5 dup-name structured error), and DX (F-DX-05/06 actionable error bodies). Consensus: every error path returns `{error: {type, message, fix, provider_name?}}` JSON shape. Implementation cuts across Tasks 1, 5, 6, 8.

**Theme 2: "Encryption at rest as v0.1 not v0.2"** — CEO (A-3) pulled it in, Eng (F-9) added versioned schema as enabler, DX (F-DX-09) caught plan-text contradiction. Consensus: ship encrypted-at-rest in v0.1 with `LLM_GATEWAY_MASTER_KEY` env var.

**Theme 3: "stdio MCP limits multi-agent value prop"** — CEO (UC-4 → user challenge), Eng (F-1 lifecycle coordination assumes single-channel), DX (F-DX-13 `gateway://how-to` resource undermined if multiple agents can't share state). Consensus: deeply tied to UC-4 final-gate decision.

**Theme 4: "Test-before-code spikes"** — Eng (F-15 GLM live 2h spike) + DX (UC-2-reframed Wizard-of-Oz before Task 1) + Eng (F-16 mcp-go selection spike). Consensus: ~3.5h of pre-Task-1 validation work needed; previously implicit, now explicit.

### Deferred to TODOS.md

| Item | Source | Defer to | Reason |
|---|---|---|---|
| Router fuzz / property tests | A-8 | v0.3 | Single-user scope; not bottleneck |
| SSE load tests | A-9 | v0.3 | Single-user scope |
| Read-only UI dashboard | UC-3 (if user vetoes) | v0.2 (contingent) | P2 violation unless final-gate flips |
| HTTP/SSE MCP transport | UC-4 (if user vetoes pull-in) | v0.2 | Current OQ #8 default |
| Chinese-provider-first positioning | UC-5 (if user vetoes) | post-v0.1 | Reframe doesn't affect MVP code |
| LiteLLM-shim alternative | UC-1 (if user vetoes) | never (or replace plan) | Wholly different project |

### Pre-Gate Verification Checklist

Phase 1 (CEO):
- [x] Premise challenge — D2 confirmed; subagent challenged subset → UC-2/UC-5
- [x] All review sections 1–10 have findings or examined-and-nothing-flagged statements
- [x] Error & Rescue Registry — CEO §2 table
- [x] Failure Modes Registry — combination of CEO §2/§4 + Eng F-1..F-19
- [x] "NOT in scope" — plan §8 + A-5 tool_calls addition
- [x] "What already exists" — plan §9
- [x] Dream state delta — CEO §0C (~30% of 12-month ideal)
- [x] Completion Summary — CEO end
- [x] Dual voices ran — subagent ran; Codex `[codex-unavailable]`
- [x] CEO consensus table — produced (single-voice columns)

Phase 2 (Design): SKIPPED per Phase 0 (no UI scope).

Phase 3 (Eng):
- [x] Scope challenge with code analysis — Eng Step 0
- [x] Architecture ASCII diagram — Eng §1 augmented
- [x] Test diagram codepaths × coverage — Eng §3 + artifact
- [x] Test plan artifact on disk — `~/.gstack/projects/llm-gateway/panda-main-test-plan-20260513-040833.md`
- [x] "NOT in scope" — Eng §8 ratifies plan §8
- [x] "What already exists" — Eng review references plan §9
- [x] Failure modes registry — F-1..F-19 mapped to tasks
- [x] Completion Summary — Eng end
- [x] Dual voices ran — subagent only
- [x] Eng consensus table — produced

Phase 3.5 (DX):
- [x] 8 DX dimensions scored before/after
- [x] Developer journey map — 9-stage table
- [x] Developer empathy narrative — 18-min first-person walkthrough
- [x] TTHW assessment — 12–15 min current → <5 min target
- [x] DX Implementation Checklist — 15 items table
- [x] Dual voices ran — subagent only
- [x] DX consensus table — produced

Cross-phase: themes section above.

Audit trail: this section (not empty).

All checklist items: ✅. Proceed to gate.

---

---

## Plan Revision 1 — Dual-Protocol Inbound (post-Task-0, 2026-05-13)

### Origin

During Task 0 Wizard-of-Oz S3, user noted: "现在大模型 api 标准有 openai 和 anthropic 两种模式." This challenges premise P3 directly.

**Why it matters:** Claude Code (the agent currently being used to operate this gateway) speaks Anthropic Messages API. If the gateway only speaks OpenAI Chat Completions, Claude Code can't use it as a backend — defeating P6 (control plane ≡ data plane caller). Task 0 surfaced this exactly as designed.

### P3 — Revised

**OLD P3:** "OpenAI Chat Completions is inbound lingua franca."

**NEW P3:** "**Both** OpenAI Chat Completions (`POST /v1/chat/completions`) **and** Anthropic Messages (`POST /v1/messages`) are inbound lingua franca. Gateway parses either into a unified internal request IR, dispatches to provider adapter, then serializes the response back to whichever shape the inbound endpoint expects."

### Architecture delta

Add an Intermediate Representation (IR) layer:

```
        OpenAI inbound        Anthropic inbound
        ─────┬───────         ──────┬─────────
             │                       │
             ▼                       ▼
        ┌─────────────┐         ┌─────────────┐
        │ openai→IR   │         │ anthropic→IR│
        │ parser      │         │ parser      │
        └──────┬──────┘         └──────┬──────┘
               │                       │
               └───────────┬───────────┘
                           ▼
                   ┌───────────────┐
                   │  Internal IR  │ (Request)
                   │  - messages   │
                   │  - system     │
                   │  - tools      │
                   │  - stream     │
                   └───────┬───────┘
                           ▼
                   ┌───────────────┐
                   │  Router       │ (unchanged)
                   └───────┬───────┘
                           ▼
                   ┌───────────────┐
                   │  Provider     │ (IR → upstream native)
                   │  GLM/DeepSeek │
                   └───────┬───────┘
                           ▼
                   ┌───────────────┐
                   │  Internal IR  │ (Response, including SSE chunks)
                   └───────┬───────┘
                           ▼
                  ┌────────┴─────────┐
                  ▼                  ▼
           ┌───────────┐       ┌─────────────┐
           │ IR→openai │       │ IR→anthropic│
           │ serializer│       │ serializer  │
           └─────┬─────┘       └──────┬──────┘
                 ▼                    ▼
            OpenAI SSE           Anthropic SSE
            (delta chunks)       (event types:
                                  message_start,
                                  content_block_*,
                                  message_delta,
                                  message_stop)
```

### File structure delta

Adds 1 package + 1 handler:

```
internal/
  ir/                                  ← NEW: unified request/response types
    request.go                         (Request, Message, Tool, etc.)
    response.go                        (Response, ContentBlock, StopReason)
    stream.go                          (StreamChunk — superset of both formats)
  server/
    chat_completions.go                ← UPDATED: openai→IR, IR→openai SSE
    messages.go                        ← NEW: anthropic→IR, IR→anthropic SSE
  provider/
    provider.go                        ← UPDATED: interface now takes ir.Request,
                                                 returns ir.Response / ir.StreamChan
    glm.go                             ← UPDATED: IR→GLM native + GLM→IR
    deepseek.go                        ← UPDATED: IR→DeepSeek native + DeepSeek→IR
```

### Task list delta (insert / modify)

**New Task 1.5 — Define IR types** (~0.5 day, inserts between original Task 1 and Task 2)
- `internal/ir/request.go`: `Request{Messages []Message; System string; Tools []Tool; Stream bool; Model string; ...}`
- `internal/ir/response.go`: `Response{Content []ContentBlock; StopReason string; Usage Usage}`
- `internal/ir/stream.go`: `StreamChunk{Type string; Delta string; ContentBlockIndex int; Usage *Usage}`
- IR shape is closer to Anthropic Messages than OpenAI (system as top-level field, multi-block content) — explicit choice: Anthropic is the more expressive superset, OpenAI is mostly mappable down.
- Tests: round-trip empty / single-message / system-prompt / multi-block / tool-use requests through IR.

**Task 2 modified** (DeepSeek adapter): now `func (p *DeepSeek) Complete(ctx, ir.Request) (ir.Response, error)` and `Stream(ctx, ir.Request) (<-chan ir.StreamChunk, error)`. Tests assert ir-shape round-trip.

**Task 3 modified** (GLM adapter): same shape; F-15 spike now also verifies GLM SSE → IR.StreamChunk conversion preserves semantics.

**New Task 6.5 — `/v1/messages` handler** (~1 day, inserts between original Task 6 and Task 7)
- `internal/server/messages.go`
- Parse Anthropic Messages POST body → ir.Request
- Route → provider → ir.Response / ir.StreamChan
- Serialize → Anthropic SSE event stream (`event: message_start\ndata: ...\n\n`, `content_block_delta`, `message_stop`)
- Tests:
  - Blocking happy path
  - Streaming happy path — golden-file replay of an Anthropic Messages SSE stream
  - System-prompt-as-top-level handled
  - Multi-block content response
  - `r.Context().Done()` propagates (per F-7)
  - Upstream error → Anthropic-shaped error JSON `{type:"error", error:{type:"...", message:"..."}}`

**Task 6 modified** (`/v1/chat/completions` e2e): adds OpenAI→IR→OpenAI round-trip; same golden-file approach for OpenAI SSE deltas.

**No change** to: Tasks 0, 1, 4, 4.5, 5, 7, 7.5, 8, 9, 10, 11, 12, 13. MCP layer and SQLite layer untouched — they're inbound-protocol-agnostic.

### Effort delta

| Item | Before | After |
|---|---|---|
| Task 0 (Wizard-of-Oz) | 30 min | 30 min (in progress, partial result is THIS revision) |
| Task 1.5 (IR types) | — | +0.5 day |
| Task 2/3 (providers) | 1d + 0.5d | 1d + 0.5d (no change, but adapter code changes shape) |
| Task 6 (OpenAI e2e) | 1d | 1d |
| Task 6.5 (Anthropic e2e) | — | +1 day |
| **v0.1.0 total** | ~9.5 工程日 | **~11 工程日** |

+1.5 days for inbound dual-protocol. Cost is concentrated in Task 1.5 (IR types) and Task 6.5 (Anthropic handler).

### Open Questions added

- **Q9:** Streaming chunk fidelity — Anthropic Messages SSE has explicit event types (`message_start`, `content_block_start`, `content_block_delta`, `content_block_stop`, `message_delta`, `message_stop`, `ping`) vs OpenAI's flat delta chunks. IR.StreamChunk must hold enough info to serialize either. Settled: Anthropic-shaped is the superset; OpenAI is a lossy down-conversion (no `content_block_start` events emitted, just `delta` chunks until done).
- **Q10:** Tool use schema — both support tools but with different JSON shape (`tools/tool_use/tool_result` Anthropic vs `tools/tool_calls/tool` OpenAI). v0.1 ships pass-through only; lossless round-trip across formats is v0.2 if needed (likely never, since each caller stays in its own ecosystem).
- **Q11:** Anthropic `system` parameter is top-level string; OpenAI `system` is a role in the messages array. IR has system as a top-level optional field; openai-parser hoists any leading `{role:"system", content:...}` into IR.System.
- **Q12:** Anthropic supports image blocks and PDF blocks in content. v0.1 chat-only; reject (400) non-text content blocks on inbound until a provider supports them.

### Failure mode added

- **FM-2-1:** Inbound is OpenAI, upstream is GLM, but client sent a tool_calls response back as input message. OpenAI→IR parses; IR→GLM native must include the tool_calls. If GLM doesn't support that tool_calls shape, return structured error per F-DX-05.

### Decision Audit Trail addendum

| # | Decision | Classification | Principle | Origin |
|---|---|---|---|---|
| R1-1 | Expand P3 to dual-protocol inbound | strategic | P1 + P6 alignment | Task 0 S3 finding |
| R1-2 | Internal IR shape closer to Anthropic | mechanical | P5 (superset is simpler than union) | derived from R1-1 |
| R1-3 | v0.1 rejects non-text content blocks (Q12) | mechanical | P3 pragmatic (scope guard) | derived from R1-1 |
| R1-4 | v0.1 chat-only; no image/PDF/audio | mechanical | scope unchanged | reaffirmed |

### What Task 0 has already proven

Task 0 was budgeted 30 min to validate P6. Before reaching S4, it already:
- Surfaced a P3 protocol gap (this revision)
- Confirmed that MCP-driven config edits feel natural and bundle well (S1+S2: 2 ops in one breath, 3-tool-call sequence in S2's add-deepseek-and-aliases)
- Shown the agent sensibly stalls when prerequisite missing (S2 caught missing deepseek before calling set_model_alias)

If Task 0 finishes without surfacing more P-revisions, MCP-WINS verdict is leaning in.

---

<!-- /autoplan restore point: ~/.gstack/projects/llm-gateway/panda-main-autoplan-restore-*.md (none yet; first /autoplan pass in progress) -->

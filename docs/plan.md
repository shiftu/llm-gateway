# llm-gateway — Implementation Plan

> **Status: APPROVED + REVISED-2** (autoplan D4 = option A, 2026-05-13; Revision 1 + Revision 2 added same day). Approved with 42 auto-decided fixes + Task 0 Wizard-of-Oz validation. 4 user challenges (UC-1/3/4/5) explicitly declined — archived in `TODOS.md`.
>
> **🆕 Plan Revision 1 (post-Task-0):** P3 expanded from OpenAI-only inbound to **dual-protocol inbound** (OpenAI Chat Completions + Anthropic Messages). Triggered by user noting Claude Code uses Anthropic API natively.
>
> **🆕 Plan Revision 2 (post-DeepSeek-spike):** Architecture pivots from "always IR-translate" to **pass-through preferred, IR-translate as fallback**. Provider config gets dual `base_url`s. IR types expand to include reasoning/thinking content blocks + reasoning_tokens. Triggered by DeepSeek v4 spike — see `docs/spikes/2026-05-13-deepseek-protocols.md`.
>
> **Precedence:** When older sections below (§1 Premises, §2 Architecture, §4 Tasks) contradict revisions, the most recent revision wins (R2 > R1 > original).
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

---

## Plan Revision 2 — Pass-Through Routing + Reasoning Content (post-DeepSeek-spike, 2026-05-13)

### Origin

DeepSeek v4-flash live spike against `api.deepseek.com` revealed:

1. DeepSeek natively serves BOTH `/v1/chat/completions` (OpenAI) AND `/anthropic/v1/messages` (Anthropic). Same key.
2. v4-flash is a **reasoning model** — every response carries hidden chain-of-thought as `reasoning_content` (OpenAI shape) or `type:thinking` content block (Anthropic shape).
3. Anthropic-endpoint latency is 2.24× the OpenAI endpoint for identical work (1.74s vs 3.90s). DeepSeek's own translation layer is the cost.

Full smoke results: `docs/spikes/2026-05-13-deepseek-protocols.md`.

### Architecture pivot

R1 assumed gateway always parses inbound → IR → upstream native. R2 splits routing:

```
inbound openai      + provider has openai_base_url     → PASS-THROUGH (preferred)
inbound openai      + provider has only anthropic_url  → IR-translate to anthropic
inbound anthropic   + provider has anthropic_base_url  → PASS-THROUGH (preferred)
inbound anthropic   + provider has only openai_url     → IR-translate to openai
```

Pass-through means: gateway reads the inbound request body, swaps in the provider's URL + key + headers, streams the upstream response back through (with bytes transformed only for SSE framing if needed). No IR involvement. Single TCP hop, lowest latency.

IR-translate is the fallback — only fires when inbound protocol and outbound protocol differ. v0.1 estimate: ~20% of calls hit IR.

### Storage schema delta — provider table

```sql
-- R2: was `base_url TEXT`; now dual url + version
CREATE TABLE providers (
  name              TEXT PRIMARY KEY,
  kind              TEXT NOT NULL,                  -- "deepseek" | "glm" | "openai" | "anthropic"
  openai_base_url   TEXT,                           -- nullable
  anthropic_base_url TEXT,                          -- nullable
  api_key           TEXT NOT NULL,                  -- encrypted (per A-3)
  anthropic_version TEXT DEFAULT '2023-06-01',
  is_default        INTEGER NOT NULL DEFAULT 0,
  created_at        INTEGER NOT NULL,
  CHECK (openai_base_url IS NOT NULL OR anthropic_base_url IS NOT NULL)
);
```

MCP `add_provider` tool args change accordingly: `name`, `kind`, `openai_base_url?`, `anthropic_base_url?` (at least one required), `api_key`, `anthropic_version?`.

### IR types — final shape

```go
package ir

type Request struct {
    Model      string
    Messages   []Message
    System     string          // top-level, hoisted from openai role=system
    Tools      []Tool
    Stream     bool
    MaxTokens  int
    Temperature *float32
    Metadata   map[string]any  // pass-through for vendor extensions
}

type Message struct {
    Role    string           // "user" | "assistant" | "tool"
    Content []ContentBlock   // multi-block to match Anthropic
}

type ContentBlock struct {
    Type      string  // "text" | "thinking" | "tool_use" | "tool_result"
    Text      string  // for text
    Thinking  string  // for thinking
    Signature string  // for thinking (Anthropic adds this)
    ToolUse   *ToolUse
    ToolResult *ToolResult
}

type Response struct {
    Content    []ContentBlock
    StopReason string   // "end_turn" | "max_tokens" | "stop_sequence" | "tool_use"
    Usage      Usage
    Model      string
    ID         string
}

type Usage struct {
    InputTokens         int
    OutputTokens        int
    ReasoningTokens     int  // 0 if not a reasoning model
    CacheReadTokens     int
    CacheCreationTokens int
}

type StreamChunk struct {
    Type        string // "message_start" | "content_block_start" | "content_block_delta" |
                      // "content_block_stop" | "message_delta" | "message_stop" | "ping"
    BlockIndex  int
    BlockType   string  // "text" | "thinking" | "tool_use"
    DeltaText   string  // for text_delta / thinking_delta
    Usage       *Usage
    StopReason  string
    Model       string  // present on message_start
    ID          string  // present on message_start
}
```

The IR shape mirrors Anthropic Messages because Anthropic is the more expressive superset. OpenAI is a lossy down-projection (no explicit block boundaries, reasoning_content flat-collapsed).

### Provider adapter shape — final

Each provider implements 4 methods:

```go
type Provider interface {
    Name() string

    // Native pass-through paths — gateway calls these when inbound protocol
    // matches provider's available base_url. Body is forwarded with minimal
    // transformation (just URL/header swap).
    OpenAIPassthrough(ctx, body io.Reader, stream bool) (resp io.ReadCloser, err error)
    AnthropicPassthrough(ctx, body io.Reader, stream bool) (resp io.ReadCloser, err error)

    // IR paths — gateway calls these when protocols cross. Provider translates
    // IR → native upstream shape, then native response → IR.
    CompleteIR(ctx, req ir.Request) (ir.Response, error)
    StreamIR(ctx, req ir.Request) (<-chan ir.StreamChunk, error)
}
```

Pass-through methods are short — URL + header substitution + body forwarding. IR methods are the heavier path (full serde + reasoning_content ↔ thinking block mapping + usage field translation per F6 in spike).

### Task list delta (over R1)

- **Task 1.5 (IR types)** — expand from 0.5d to 1d. Now must include `ContentBlock` with `Thinking` variant + `Signature`, `Usage` with 5 fields, `StreamChunk` with event types.
- **Task 2 (DeepSeek adapter)** — split into 2a (OpenAIPassthrough + AnthropicPassthrough — both trivial since DeepSeek serves both natively) and 2b (CompleteIR + StreamIR — only fires on protocol crossing). Effort: 1d unchanged.
- **Task 3 (GLM adapter)** — same split. Hinges on Q13: if GLM only has OpenAI-compat, then AnthropicPassthrough returns ErrNotSupported and IR path carries Anthropic-inbound traffic. 0.5d unchanged.
- **Task 4 storage schema** — adjust providers table per R2 schema above. +0.25d for migration logic (R1's Task 4 already has F-9 versioned schema; just bump version).
- **Task 5 router** — adds protocol-aware decision. Given inbound protocol + provider's available base_urls, returns either (pass-through path + provider) or (IR path + provider). +0.25d.
- **Task 6 (`/v1/chat/completions` handler)** — wires pass-through fast path + IR fallback. ~unchanged.
- **Task 6.5 (`/v1/messages` handler)** — same wiring symmetrically. ~unchanged.
- **Task 8 MCP add_provider tool** — args expanded to dual base_urls. ~unchanged.

### Effort delta (over R1)

| Item | R1 estimate | R2 estimate |
|---|---|---|
| Task 1.5 (IR types) | 0.5d | 1d |
| Task 4 (storage + migration) | 1d | 1.25d |
| Task 5 (router) | 1d | 1.25d |
| **v0.1.0 total** | ~11d | **~11.5d** |

Net +0.5d on top of R1's +1.5d over baseline. Total project ~11.5 工程日 from original 8.

### Open Questions added

- **Q13:** Does GLM/Zhipu have a native Anthropic endpoint? Spike against `https://open.bigmodel.cn/anthropic` or check Zhipu docs. If yes → no IR translation needed for v0.1 provider set; IR layer becomes dormant insurance. If no → Anthropic-inbound + GLM-upstream is the only IR-hot path.
- **Q14:** Reasoning-content passthrough policy when inbound is OpenAI (no canonical `reasoning_content` field in OpenAI spec, but it's a widely-adopted vendor extension). Lean: pass it through (matches o1 + DeepSeek's own OpenAI endpoint behavior).
- **Q15:** Anthropic `signature` field on thinking blocks — verify across multiple requests if it's actually a verification signature or just request id duplication.
- **Q16:** Pass-through SSE byte-level fidelity. Pass-through MUST emit the upstream bytes verbatim including the `\n\n` SSE delimiters. Test with golden file: client byte-stream from DeepSeek direct === client byte-stream from gateway pass-through.

### Failure modes added

- **FM-R2-1:** Provider configured with only one base_url, client sends request via the other protocol. → IR translation path. Test: configure GLM with only openai_base_url; send Anthropic-format request; gateway translates inbound → IR → openai upstream → IR → anthropic response. Latency penalty expected.
- **FM-R2-2:** Reasoning-content data loss. If gateway translates DeepSeek-OpenAI `reasoning_content` to OpenAI inbound and the client doesn't understand the field, content is wasted. Mitigation: pass through unchanged (matches OpenAI o1 convention).
- **FM-R2-3:** Anthropic stream `ping` event has no OpenAI equivalent. Drop on IR translate to OpenAI (covered in F7 spike conclusion).

### Decision Audit Trail addendum

| # | Decision | Classification | Principle | Origin |
|---|---|---|---|---|
| R2-1 | Provider table dual base_urls | mechanical | P5 (explicit) | spike F2 |
| R2-2 | Pass-through preferred over IR | strategic | P3 pragmatic + perf | spike F5 (2.24× latency) |
| R2-3 | IR.ContentBlock includes `thinking` variant | mechanical | P1 completeness | spike F1 |
| R2-4 | IR.Usage takes 5-field superset | mechanical | P1 | spike F6 |
| R2-5 | StreamChunk event types (Anthropic-shape superset) | mechanical | P5 | spike F3 |
| R2-6 | Provider adapter: 4 methods (2 passthrough + 2 IR) | mechanical | P3 (cheaper happy path) | derived from R2-2 |
| R2-7 | reasoning_content pass-through to OpenAI inbound | strategic | P3 + match-incumbent | Q14 default answer |

### What Task 0 + spike have already produced

- **R1:** Caught protocol-coverage gap (P3) before any Go was written
- **R2:** Caught reasoning-content shape + pass-through performance opportunity before any Go was written
- **Total saved:** ~3 days of likely rework had these surfaced after Task 1-6 were already coded

Task 0 + the 30-min spike together cost ~45 min of decision time. ROI is unambiguous.

### Next concrete step

1. **Verify Q13** — send a 5-second curl spike against GLM's openai endpoint + try GLM Anthropic endpoint if user has GLM key. If GLM is OpenAI-only, IR layer carries the Anthropic-inbound + GLM-upstream traffic (real, not theoretical).
2. **Then:** F-16 mcp-go selection spike (1d) — but can run in parallel with Task 1.5 since they don't share files.
3. **Then:** Task 1 (HTTP skeleton + bearer auth) per the original task ordering.

---

## 🆕 Plan Revision 3 — v0.1.5 Multi-tenancy Additive Layer (2026-05-13)

**Triggered by:** User direction "架构需要考虑给多个team,一个team多个用户create APIKey,并统计usage. mcp server token 区分super admin和normal admin." Premise gate at /autoplan run #2 selected option C (additive v0.1.5 layer) with all four scope items enabled (core 4-piece set + double-tier MCP RBAC + quota enforcement + per-team provider with optional fallback).

**Precedence:** R3 > R2 > R1 > original. Where R3 contradicts P1 ("Single-user / small-team scope. Not SaaS. No RBAC, no multi-tenancy, no billing.") in §1, R3 wins for v0.1.5+. P2 (no web UI), P3 (dual-protocol inbound, see R1), P4 (GLM+DeepSeek), P5 (single binary, SQLite, no Postgres/Redis), P6 (AI-native) all UNCHANGED.

### Premise delta

| | v0.1 (current) | v0.1.5 (R3) |
|---|---|---|
| **P1 — Tenancy** | Single bearer token, no RBAC | Multi-tenant **opt-in**, additive; legacy token still works |
| **P2 — Ops surface** | MCP only (no web UI) | MCP only (no web UI) |
| **P5 — Artifact** | Single Go binary, SQLite, no external state | Single Go binary, SQLite (additive schema v2), no external state |
| Inbound auth | Single `LLM_GATEWAY_TOKEN` | API key lookup in `api_keys` table; legacy token maps to `super_admin @ team:default` |
| Storage | providers, model_aliases, request_logs | + teams, users, api_keys, usage_counters, quotas; ALTER on providers (team_id NULL) + request_logs (api_key_id, team_id NULL) |
| MCP RBAC | n/a (single token = full power) | Two tiers: `super_admin` (cross-team) / `normal_admin` (own team) |
| Provider scoping | Global only | Optional `team_id`; NULL = global default fallback |
| Quotas | None | Optional per-team or per-key; enforced inbound (429 on exceed) |

### Backward compatibility contract (CRITICAL — drives every R3 design choice)

1. **Existing `LLM_GATEWAY_TOKEN` env var keeps working forever.** When the inbound auth middleware sees a token that equals the env value, it short-circuits to `(team:default, user:root, role:super_admin)` without consulting `api_keys`. No forced migration.
2. **Pre-1.5 single-provider config keeps working.** `providers.team_id` is nullable; NULL means "global default, served to any team that has no team-specific provider matching the request."
3. **Pre-1.5 `request_logs` rows are readable forever.** Added columns are nullable; old `tail_logs` MCP tool surfaces them with empty team/key attribution.
4. **Multi-tenancy is opt-in via MCP.** `add_team` is the explicit gesture that lights up the new world. A user who never runs it sees no behavioral change.
5. **v0.1 ship is unblocked.** R3 introduces **zero** changes to in-progress Tasks 7-13. The schema hooks (request_logs.api_key_id, request_logs.team_id) are added via migration v1→v2, which only fires when the operator upgrades.

### Schema additions (migration v1 → v2)

```sql
-- New tables
CREATE TABLE teams (
  id          TEXT PRIMARY KEY,        -- nanoid: tm_<rand>
  slug        TEXT NOT NULL UNIQUE,    -- short handle: "acme", "platform"
  name        TEXT NOT NULL,
  created_at  INTEGER NOT NULL
);

CREATE TABLE users (
  id           TEXT PRIMARY KEY,       -- usr_<rand>
  team_id      TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  email        TEXT NOT NULL,          -- identity label, not an auth factor in v0.1.5
  display_name TEXT,
  created_at   INTEGER NOT NULL,
  UNIQUE (team_id, email)
);

CREATE TABLE api_keys (
  id            TEXT PRIMARY KEY,      -- ak_<rand>; also the displayable identifier
  user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  hash          TEXT NOT NULL,         -- bcrypt of the full secret; plaintext shown ONCE at issuance
  prefix        TEXT NOT NULL,         -- first 12 chars for UI display + prefix-indexed lookup hint
  name          TEXT,                  -- human label ("CI key", "Cline laptop")
  scope         TEXT NOT NULL,         -- "inbound" | "mcp_super" | "mcp_normal"
  revoked_at    INTEGER,               -- NULL = active
  expires_at    INTEGER,               -- NULL = never
  last_used_at  INTEGER,
  created_at    INTEGER NOT NULL,
  CHECK (scope IN ('inbound','mcp_super','mcp_normal'))
);
CREATE INDEX idx_api_keys_user   ON api_keys(user_id);
CREATE INDEX idx_api_keys_prefix ON api_keys(prefix);

CREATE TABLE usage_counters (
  -- per (api_key, calendar-day) rolling aggregate; UPSERT on each request
  api_key_id       TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
  day              INTEGER NOT NULL,    -- unix-millis / 86_400_000, server-local-TZ for v0.1.5
  request_count    INTEGER NOT NULL DEFAULT 0,
  input_tokens     INTEGER NOT NULL DEFAULT 0,
  output_tokens    INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (api_key_id, day)
);

CREATE TABLE quotas (
  -- optional per-team or per-key cap; absence = unlimited
  scope_kind   TEXT NOT NULL,           -- "team" | "key"
  scope_id     TEXT NOT NULL,
  window       TEXT NOT NULL,           -- "month" | "day" | "minute"
  max_requests INTEGER,                 -- NULL = no request cap
  max_tokens   INTEGER,                 -- NULL = no token cap (sum of input+output)
  PRIMARY KEY (scope_kind, scope_id, window),
  CHECK (scope_kind IN ('team','key')),
  CHECK (window IN ('month','day','minute'))
);

-- Additive ALTERs to existing tables (preserve all v0.1 data + behavior)
ALTER TABLE providers     ADD COLUMN team_id    TEXT REFERENCES teams(id) ON DELETE CASCADE;
ALTER TABLE request_logs  ADD COLUMN api_key_id TEXT REFERENCES api_keys(id) ON DELETE SET NULL;
ALTER TABLE request_logs  ADD COLUMN team_id    TEXT;  -- denormalized for fast filter; no FK to keep ALTER cheap

-- model_aliases gets scoped too: NULL team_id = global alias
ALTER TABLE model_aliases ADD COLUMN team_id    TEXT REFERENCES teams(id) ON DELETE CASCADE;
```

### Inbound auth flow (after R3)

```
inbound request → extract token from Authorization/x-api-key →
  1. If token == LLM_GATEWAY_TOKEN env var (legacy) →
       (team:default-synth, user:root-synth, scope:super_admin) → grant
  2. Else: SELECT id, user_id, hash, scope, revoked_at, expires_at
            FROM api_keys WHERE prefix = <first-12-chars>
       bcrypt-compare token against hash
       If match + not revoked + not expired + scope='inbound' →
         load user.team_id → grant with (team, user, key) attribution
  3. Else → 401 structured error {type:"unauthorized", fix:"check the API key, or issue a new one via MCP `issue_api_key`"}
```

Key invariants:
- Plaintext token NEVER hits disk after issuance; only the bcrypt hash + prefix.
- Lookup is O(1) by prefix (12 chars random ≈ 72 bits, more than enough for table size).
- Legacy `LLM_GATEWAY_TOKEN` shortcut is checked FIRST so old setups skip the bcrypt cost entirely.

### MCP control-plane RBAC matrix

| Tool | super_admin | normal_admin | inbound |
|---|---|---|---|
| `list_teams`, `add_team`, `remove_team`, `rename_team` | ✓ | ✗ | ✗ |
| `list_users` (across teams) | ✓ all | own team only | ✗ |
| `add_user`, `remove_user`, `rename_user` | ✓ any team | own team only | ✗ |
| `issue_api_key` (scope=inbound) | ✓ any user | own team users | ✗ |
| `issue_api_key` (scope=mcp_super or mcp_normal) | ✓ | ✗ | ✗ |
| `revoke_api_key`, `list_api_keys` | ✓ any | own team only | ✗ |
| `add_provider` with `team_id=NULL` (global) | ✓ | ✗ | ✗ |
| `add_provider` with `team_id=<own>` | ✓ any team | own team only | ✗ |
| `set_model_alias` (alias.team_id NULL = global) | ✓ | own team only | ✗ |
| `set_quota` (scope=team) | ✓ any team | own team only | ✗ |
| `set_quota` (scope=key) | ✓ any key | own team's keys only | ✗ |
| `get_usage` | ✓ any team / any key | own team only | ✗ |
| `tail_logs`, `get_request` | ✓ all rows | own team rows only | ✗ |
| `whoami` | ✓ | ✓ | ✗ (HTTP only; no MCP tool for inbound) |

Enforcement: every MCP tool handler calls a single `requireRole(ctx, scope, team_id_predicate)` helper. Helper reads the calling token from MCP transport metadata, looks it up in `api_keys`, and gates by `scope` + ownership. **Single chokepoint** so RBAC bugs are one-fix-fits-all.

### Router resolution (after R3)

Priority order (replaces the existing R2 resolution rules):

1. **Alias hit, scoped:**
   - `SELECT * FROM model_aliases WHERE alias = ? AND (team_id = caller.team_id OR team_id IS NULL) ORDER BY team_id IS NULL ASC LIMIT 1` (team-specific first, then global)
   - If found → `(alias.provider, alias.upstream_model, via_alias)`
2. **Team-specific provider** (caller has at least one `providers` row with their `team_id`):
   - If the request's `model` matches a kind-default (e.g., model name starts with `deepseek-`) → first matching team-specific provider
   - Else → team-specific provider with `is_default=1`
3. **Global default provider** (`team_id IS NULL AND is_default=1`):
   - Pass `caller.model` through unchanged (current R2 behavior)
4. **None of the above** → `ErrNoRoute` with diagnostic.

Crucially: orphan aliases (provider was deleted) continue to return `ErrNoRoute` per F-11, unchanged. R3 just adds a scoping predicate on the WHERE clause.

### Quota / rate-limit enforcement

**Daily / monthly request + token caps** — checked inbound, in a single transaction with the usage UPSERT to prevent races:

```sql
-- pseudocode, executed in one SQLite txn:
SELECT request_count, input_tokens + output_tokens AS toks_today
  FROM usage_counters WHERE api_key_id=? AND day=?;
SELECT max_requests, max_tokens FROM quotas
  WHERE (scope_kind='key' AND scope_id=?) OR (scope_kind='team' AND scope_id=?)
  ORDER BY scope_kind='key' DESC LIMIT 1;  -- key beats team
IF current+1 > max_requests OR toks_today+est_input > max_tokens →
  COMMIT (with current usage); return 429.
ELSE INSERT/UPDATE usage_counters SET request_count = request_count + 1; COMMIT.
```

Token counts are credited on stream completion (we know real input + real output then). Reservation-based quota: when checking, use `est_input` = bytes/4 heuristic so a request that hasn't streamed yet can be approximated; final `output_tokens` corrects the counter on stream end.

**Per-minute rate limit** — in-memory token bucket per `api_key_id`, seeded from `quotas WHERE window='minute' max_requests`. Lost on process restart (acceptable for v0.1.5 single-binary).

429 response shape:
```json
{"error":{"type":"quota_exceeded","scope":"team","window":"day","reset_at":1747180800,"limit":1000,"current":1000,"fix":"contact your super admin to raise the limit via `set_quota`"}}
```

### New tasks (T14–T20) — added to §4 task table

| # | Task | Effort | Depends |
|---|---|---|---|
| **T14** | Schema migration v1→v2 + Store CRUD: teams / users / api_keys / usage_counters / quotas; ALTERs on providers, request_logs, model_aliases | 1.5d | T4 |
| **T15** | Inbound auth: api_keys lookup + bcrypt verify + prefix-indexed scan + legacy LLM_GATEWAY_TOKEN fallback | 0.75d | T14 |
| **T16** | MCP RBAC enforcement layer (`requireRole(ctx, scope, team_pred)` helper) wired into every existing + new MCP tool | 0.75d | T7, T14 |
| **T17** | New MCP tools: `add_team`, `remove_team`, `list_teams`, `add_user`, `list_users`, `issue_api_key`, `list_api_keys`, `revoke_api_key`, `set_quota`, `get_usage`, `whoami` | 1.5d | T16 |
| **T18** | Router team-scoping (alias + provider WHERE-clause predicate) + per-team provider fallback chain | 0.5d | T5, T14 |
| **T19** | Usage counter rollup (txn-safe UPSERT) + quota enforcement middleware (429 path) + token-bucket RPM limiter | 1d | T15, T18 |
| **T20** | v0.1.5 README delta + migration guide + sample MCP client snippets for super_admin vs normal_admin onboarding flows | 0.5d | T17, T19 |
| **v0.1.5 total** | | **~6.5 工程日** | (on top of v0.1's ~11.5d) |

### v0.1 freezes — small hooks to add BEFORE v0.1 ship (preserve velocity)

These are the minimum "future-proofing" diffs to in-progress / planned v0.1 work. Everything else stays on the existing trajectory.

| Where | Hook | Why |
|---|---|---|
| **T7 MCP server (current task)** | MCP transport carries the calling token in request metadata. Even though v0.1 has only one token, expose it as `ctx.Value("mcp_token")` for handlers. | T16 needs the chokepoint to exist; expensive to retrofit later. |
| **T11 init subcommand** | Doc-string addition: "this printed token will be auto-promoted to `scope=mcp_super` in v0.1.5; you can pass `--no-promote` then to skip and require explicit `issue_api_key`." | Set user expectation; zero code change in v0.1. |
| **MCP tool signatures (T8/T9/T10 — planned)** | Accept an optional `team_id` field; in v0.1, defaults to `"default"` and is ignored. In v0.1.5, defaults to caller's resolved team. | Tool signature stability across the v0.1 → v0.1.5 jump. |

These three hooks add roughly **+0.25 工程日** to v0.1 — small enough to absorb in Task 7.

### Open Questions added (R3)

- **Q17:** API key format. Proposed `lgw_<scope>_<base64rand>` where scope ∈ `{ak, msu, mna}` (inbound, mcp-super, mcp-normal). Scope inferrable from prefix without DB hit. **Lean:** adopt as drafted; confirm in CEO/Eng review.
- **Q18:** bcrypt cost factor. **Lean:** 10 across the board for v0.1.5 (single-binary, not internet-exposed); revisit if deployments go public.
- **Q19:** Quota window calendar-day vs rolling-24h. **Lean:** calendar-day, server-local TZ, documented limitation. Rolling-24h moves to v0.2 if asked.
- **Q20:** Usage counter write strategy. **Lean:** synchronous (in-request UPSERT inside the auth/quota txn). Atomicity matters for quota correctness; SQLite WAL keeps cost low.
- **Q21:** Revocation semantics on in-flight streams. **Lean:** in-flight finishes; revocation prevents only NEW requests.
- **Q22:** Per-team provider visibility. **Lean:** strict isolation — team B's super admin sees only own-team providers + global defaults; cross-team provider metadata is hidden.
- **Q23:** Whether `add_team` should auto-create a `super_admin` user-zero or require a separate `add_user` call. **Lean:** auto-create `root@<team-slug>` user with no API key issued (operator must explicitly `issue_api_key`); zero-keys-issued state is fine.
- **Q24:** What happens to existing single-provider config the first time someone runs `add_team`? Does the existing provider stay global (`team_id=NULL`) or migrate to the new team? **Lean:** stay global, no automatic association — caller explicitly chooses scope.

### Failure modes added (R3)

- **FM-R3-1:** Legacy token + multi-tenant data coexist. Operator upgrades, has `LLM_GATEWAY_TOKEN` env, runs `add_team acme`. Now there's both a synthesized "default" team AND a real team. **Test:** legacy token continues to resolve to "default"; super admin operations on "acme" work via either path.
- **FM-R3-2:** `mcp_super` key revoked mid-call. Existing call completes; next call returns auth error. MCP subprocess must surface this cleanly so the agent can request a fresh key.
- **FM-R3-3:** Quota race — two concurrent requests both pass quota check, both increment counter, total now over cap. **Mitigation:** SQLite txn with `UPDATE usage_counters SET request_count = request_count + 1 WHERE request_count + 1 <= cap RETURNING *`; if rowsAffected=0 → 429.
- **FM-R3-4:** Prefix enumeration. Prefix is displayable; an attacker who learns a prefix gains zero secret material (bcrypt). Acceptable.
- **FM-R3-5:** Per-team provider misconfigured (team has provider row, but upstream key revoked at vendor). Router still selects it; first request fails 401 at upstream. **Surface clearly:** include team-provider attribution in the structured error so the operator knows which team's key broke.
- **FM-R3-6:** Migration v1→v2 fails mid-statement (e.g., disk full during ALTER). SQLite is transactional per statement; failed ALTER leaves schema at v1. **Test:** start fresh v0.1.5 binary against a corrupted state.db; assert it refuses to start with a clean error and a `--skip-migration` opt-out for emergency rollback.
- **FM-R3-7:** Token reuse across scopes. Operator copies a `mcp_super` token into a coding agent's `Authorization: Bearer` field. **Mitigation:** at inbound auth, reject tokens whose `scope != 'inbound'` with a specific error: "this is an admin token; issue an inbound key via `issue_api_key`."

### Decision Audit Trail addendum (R3 pre-review)

| # | Decision | Classification | Principle | Origin |
|---|---|---|---|---|
| R3-1 | Multi-tenancy as v0.1.5 additive (not Revision-to-v0.1, not v0.2) | strategic | P2 (boil lakes within blast radius) + P3 (pragmatic — preserve v0.1 ship velocity) | Premise gate D1=C |
| R3-2 | Per-team provider optional, falls back to global default | mechanical | P5 (explicit over clever) | User scope answer |
| R3-3 | MCP RBAC tiers: super_admin / normal_admin only | mechanical | P5 + P3 | User scope answer |
| R3-4 | Quotas enforced (not just observed) | strategic | P1 (completeness) | User scope answer |
| R3-5 | API keys stored as bcrypt hash, plaintext shown ONCE at issuance | mechanical | Standard practice + F-DX-09 partial mitigation | Eng common practice |
| R3-6 | Legacy `LLM_GATEWAY_TOKEN` env keeps working forever; no forced migration | strategic | P3 + DX (backward compat) | Premise gate ELI10 |
| R3-7 | v0.1.5 schema is purely ALTER + new tables (zero destructive DDL) | mechanical | P5 + safety | R3-1 implication |
| R3-8 | MCP tool signatures gain optional `team_id` field in v0.1 already (defaults ignored) | mechanical | P3 (cheap hook now vs expensive retrofit later) | v0.1 freezes section |
| R3-9 | RBAC enforced via single `requireRole(ctx, scope, team_pred)` chokepoint | mechanical | P5 (single fix surface) | Standard pattern |
| R3-10 | Token bucket RPM in-memory only (reset on restart) | strategic | P3 — SQLite-backed rate limit deferred to v0.2 | Effort/value tradeoff |

### What R3 leaves OPEN (deferred to v0.2 / v0.3)

- **Encryption-at-rest for `providers.api_key` AND `users.email`** — still F-DX-09 gap. Tracked as Task 4.5; v0.1.5 ships without it. Note: api_keys.hash is bcrypt'd, so the most-sensitive secret IS at-rest-safe.
- **Audit log of admin actions** (who created which team, who revoked which key) — propose `admin_audit` table in v0.1.6 if R3 ships and demand emerges.
- **OAuth / SSO** for user identity — out of scope. v0.1.5 users are identified only by team_id+email (no auth handshake); API keys remain the only auth factor.
- **Cost tracking ($ per request)** — Already in TODOS.md as v0.2 item; R3 confirms it stays there. Token counts are sufficient for usage views; multiplication by per-model $/token comes later.
- **HTTP/SSE MCP transport** — Still v0.2 per TODOS.md UC-4; multi-tenancy doesn't change the verdict.
- **Per-key telemetry surfaced in `list_api_keys`** (e.g., `remaining_today` snapshot) — nice-to-have; defer to "next" if asked.

### What Plan Revision 3 produces (deliverables checklist)

When R3 ships as v0.1.5:

- [ ] Schema migration v1→v2 applied idempotently on first boot of v0.1.5 against any v0.1 state.db
- [ ] `add_team`, `add_user`, `issue_api_key` MCP tools functional from `super_admin` token
- [ ] Inbound HTTP recognizes BOTH legacy `LLM_GATEWAY_TOKEN` and new `lgw_ak_*` keys
- [ ] `tail_logs` filtered by team_id when called with `normal_admin` scope
- [ ] `set_quota` + `get_usage` operate per spec
- [ ] 429 response on quota exceed with structured `fix:` guidance
- [ ] README v0.1.5 section showing "upgrade from v0.1 takes one MCP call: `add_team`"

---

## Plan Revision 3 — CEO Review (2026-05-13)

**Mode:** /autoplan Phase 1, dual-voice attempted; Codex `[codex-unavailable]` (binary missing on machine). Single-voice review by Claude subagent, marked `[subagent-only]` per autoplan degradation matrix. Per skill rule "Single critical finding from one voice = flagged regardless."

### CEO Consensus Table (single voice — most cells N/A)

| Dimension | Claude subagent | Codex | Consensus |
|---|---|---|---|
| 1. Premises valid? | ✗ (P1 reversal is partial/incoherent) | N/A | FLAGGED |
| 2. Right problem to solve? | Mixed — multi-tenancy is real ask; framing wrong | N/A | FLAGGED |
| 3. Scope calibration correct? | ✗ (additive is wrong call vs B halt+redo) | N/A | FLAGGED |
| 4. Alternatives sufficiently explored? | ✗ (B dismissed too cheaply at premise gate) | N/A | FLAGGED |
| 5. Competitive/market risks covered? | ✗ (LiteLLM home-turf entry not interrogated) | N/A | FLAGGED |
| 6. 6-month trajectory sound? | ✗ (users.email + binary RBAC create cliff) | N/A | FLAGGED |

### Findings (10)

**F-R3-CEO-1 — "Additive opt-in" is a self-deception. [critical]**
What's wrong: every inbound request now traverses a multi-tenant auth path with a legacy-shortcut branch, doubling the auth surface area regardless of operator opt-in. Calling the legacy path "opt-in" hides that you are shipping two products in one binary.
Fix: replace the "opt-in" language in Premise delta and Backward Compatibility Contract §1 with explicit deprecation: "v0.1.5 is multi-tenant; legacy code path is removed at v0.2 (date)." Add a deprecation entry to §1 with a v0.3 removal target, OR carry two auth paths forever.

**F-R3-CEO-2 — Premise-gate option C was the wrong call vs option B. [critical]**
What's wrong: R3 adds 5 tables, 4 ALTERs, new auth flow, RBAC chokepoint, quota enforcement, router scoping — this is a re-foundation, not a layer. Every v0.1 task touching storage/auth/router must be re-validated under the new schema anyway, so the "preserve velocity" argument is thin.
Fix: either (i) halt T7 now, fold T14–T19 into v0.1, ship one coherent multi-tenant v0.1 — collapse v0.1/v0.1.5 distinction; OR (ii) cut R3 scope to teams+users+api_keys only and defer quotas, per-team providers, double-tier RBAC to v0.2. The current path is the worst of both — too much schema to be "additive," too little discipline to be a clean redo.

**F-R3-CEO-3 — Binary RBAC (super_admin / normal_admin) forces Revision 4 within one quarter. [high]**
What's wrong: real multi-tenant deployments need at least a read-only auditor (compliance, finance) and a billing-only role (cost dashboards without provider keys). Collapsing those into `normal_admin` leaks provider API keys to anyone who can see usage.
Fix: widen `api_keys.scope` enum to `{inbound, mcp_super, mcp_admin, mcp_auditor, mcp_billing}` with explicit stubs + helpful rejection for unimplemented scopes, OR rename column to `role` and add a `permissions` JSON column so future roles don't require migration.

**F-R3-CEO-4 — Quota without $-billing is a foot-gun. [high]**
What's wrong: first user to hit a token quota asks "how much did that cost me?" — token caps without cost caps means operator hand-computes $ from request_logs, defeating quota's value.
Fix: either (i) add `model_costs(provider, model, usd_per_input_1k, usd_per_output_1k)` table and surface `$_today` in `get_usage` — ~0.25d work; OR (ii) remove quota enforcement from R3, ship usage *observation* only, defer enforcement until cost is in scope.

**F-R3-CEO-5 — Per-team provider fallback to global = data-leakage. [high]**
What's wrong: when team B has no provider and global is team A's key, team B's requests bill team A's vendor account and prompts traverse team A's infra — no consent, no audit, no cost-attribution column on `request_logs` to disambiguate "team-billed" from "global-billed."
Fix: add `providers.fallback_eligible BOOLEAN NOT NULL DEFAULT 0`; global providers must explicitly opt in to serving non-owning teams. Add `request_logs.provider_owner_team_id` denormalized column. Document Q22 default = OFF, not ON.

**F-R3-CEO-6 — `users.email` as identity-only with no auth handshake is indefensible. [high] [single-most-important]**
What's wrong: plan says users identified by `(team_id, email)` and "API keys remain the only auth factor" — but super_admin can `issue_api_key` for any email in any team, so the trust model collapses to "whoever holds super_admin token IS every user." It's identity without identity.
Fix: **drop the `users` table from R3 entirely.** API keys belong to teams, not to fictional users. Add `api_keys.created_for_label TEXT` free-form ("Cline laptop", "joy's CI"). When real auth lands in v0.2 (OAuth/SSO), introduce `users` then with actual auth meaning.

**F-R3-CEO-7 — "Preserve v0.1 ship velocity" is rationalization. [medium]**
What's wrong: v0.1 is 9/13 done with T7 in-progress; +0.25d freeze hooks + 6.5d v0.1.5 means the actual delta over B (halt+redo) is ~1-2 days, not catastrophic.
Fix: add an honest effort comparison to Decision Audit Trail: "B (halt+redo): +2d to v0.1 ship, one product. C (additive): +0.25d to v0.1, +6.5d to v0.1.5, indefinite two-product maintenance." If C wins on that table, it wins for the right reasons.

**F-R3-CEO-8 — Competitive positioning weakens, not strengthens. [high]**
What's wrong: differentiation thesis is "AI-native MCP-only, Chinese-provider-first, single-binary." R3 enters LiteLLM's strength zone (multi-tenancy, quotas, RBAC) where LiteLLM has multi-year head start, while doing nothing to deepen actual moats.
Fix: add a "Competitive delta" subsection to R3 naming LiteLLM explicitly. Answer: "what does v0.1.5 do that LiteLLM does not, that justifies the user staying?" If only "MCP-only control plane," then R3 should invest there (richer MCP tools, agent-driven team provisioning flows) instead of re-implementing LiteLLM's table stakes.

**F-R3-CEO-9 — Quota race mitigation contradicts the §Quota pseudocode. [medium]**
What's wrong: §Quota section shows non-atomic SELECT-then-INSERT, FM-R3-3 promises atomic `UPDATE...WHERE...RETURNING`. These are different mechanisms; the SELECT-then-INSERT version is racy.
Fix: replace pseudocode with atomic `UPDATE usage_counters SET request_count = request_count + 1 WHERE api_key_id=? AND day=? AND request_count + 1 <= cap RETURNING *` form. Add a concurrency test (50 parallel requests vs cap=10) to T19 acceptance criteria.

**F-R3-CEO-10 — Migration rollback is one-way only. [medium]**
What's wrong: FM-R3-6 mentions `--skip-migration` for rollback but schema additions aren't reversible — a v0.1.5 binary that wrote to teams/users/api_keys cannot be downgraded to v0.1 without data loss.
Fix: either (i) commit to no rollback and remove `--skip-migration` from FM-R3-6 (false comfort); OR (ii) add `dump-v1-compatible` MCP tool that exports legacy-shape state.db for downgrade. Pick one, document honestly.

### Strategic Risk Synthesis

R3 is a tactical retreat dressed as strategic advance. User asked for multi-tenancy, plan delivers it, but the "additive, opt-in, preserves velocity" framing papers over the fact that v0.1.5 fundamentally changes what the product IS. The original wedge ("AI-native single-binary MCP-controlled gateway for Chinese providers, single-user scope") was sharp + defensible vs LiteLLM. R3 dulls that wedge by entering LiteLLM's home turf (teams, quotas, RBAC) without years of polish LiteLLM has there, while adding two-code-path maintenance burden, a fake identity layer (`users.email` with no auth), and a binary RBAC tier that won't survive contact with real deployments. The 6.5-day estimate is plausible for code; the indefinite maintenance tax of two auth paths + two tenancy models is not in the estimate at all.

### Verdict

**REJECT** — current R3 should be revised before proceeding to v0.1.5 implementation.

### Single Most Important Change

**Delete the `users` table from R3.** `users.email` as identity-only is the load-bearing self-deception. Removing it cuts ~15% of the schema, eliminates the "identity without auth" boundary, and forces the plan to confront whether v0.1.5 is actually about multi-tenancy OR just about API key issuance — the latter being a much smaller, much shippable feature.

### Auto-Decided CEO Findings Summary (per autoplan 6 principles)

Per autoplan rule: critical findings from a single voice are **flagged but not auto-decided** — they surface at the Phase 4 final gate as user-challenge candidates. Mediums get auto-decided where the 6 principles fit; criticals/highs get user judgment.

| # | Severity | Auto-decided? | Decision / Surface-at-gate |
|---|---|---|---|
| F-R3-CEO-1 | critical | NO — surface at gate | Phrased as user-challenge: "drop legacy LLM_GATEWAY_TOKEN path entirely, OR keep but add v0.3 removal date?" |
| F-R3-CEO-2 | critical | NO — surface at gate | Re-opens premise gate D1: C vs B vs A. Most important question. |
| F-R3-CEO-3 | high | NO — surface at gate | "widen scope enum now (cheap)" vs "stay binary (clean)"; needs user judgment |
| F-R3-CEO-4 | high | NO — surface at gate | "add $-billing now (~0.25d)" vs "remove quota enforcement"; both are real options |
| F-R3-CEO-5 | high | YES — apply fix | `fallback_eligible BOOLEAN DEFAULT 0` is mechanical P5 (explicit over clever). Update Q22 default to OFF. |
| F-R3-CEO-6 | high | NO — surface at gate | Single Most Important Change. Drops `users` table entirely. |
| F-R3-CEO-7 | medium | YES — apply fix | Add honest effort comparison table to R3 Decision Audit Trail. P5 (explicit) |
| F-R3-CEO-8 | high | NO — surface at gate | Competitive delta subsection — needs user direction on what *to* invest in if not in LiteLLM-parity |
| F-R3-CEO-9 | medium | YES — apply fix | Update §Quota pseudocode to atomic UPDATE...RETURNING form. P5 + correctness |
| F-R3-CEO-10 | medium | YES — apply fix | Remove `--skip-migration` from FM-R3-6 (it's false comfort); add explicit "no rollback in v0.1.5; downgrade requires fresh state.db" |

Mechanically-applied fixes (CEO-5, CEO-7, CEO-9, CEO-10) will be appended to R3 charter as a "CEO Auto-Decided Patches" subsection when Phase 4 gate is reached and other phases' findings are merged.

---

## Plan Revision 3 — Eng Review (2026-05-13)

**Mode:** /autoplan Phase 3, dual-voice attempted; Codex `[codex-unavailable]`. Single-voice review by Claude subagent, marked `[subagent-only]`.

### Eng Consensus Table (single voice — most cells N/A)

| Dimension | Claude subagent | Codex | Consensus |
|---|---|---|---|
| 1. Architecture sound? | Mixed — chokepoint pattern fragile | N/A | FLAGGED |
| 2. Test coverage sufficient? | ✗ (12 new test files identified) | N/A | FLAGGED |
| 3. Performance risks addressed? | ✗ (bcrypt-on-every-request) | N/A | FLAGGED |
| 4. Security threats covered? | ✗ (revocation race, prefix collision) | N/A | FLAGGED |
| 5. Error paths handled? | Mixed (rollback story weak) | N/A | FLAGGED |
| 6. Deployment risk manageable? | ✗ (migration idempotency unclear) | N/A | FLAGGED |

### Findings (12)

**F-R3-ENG-1 — `ALTER TABLE ... REFERENCES` is silently weaker than declared. [high]**
`modernc.org/sqlite` honors SQLite's `ADD COLUMN ... REFERENCES` only with `NULL` default; FK not enforced for pre-existing rows. `database/sql` pool means different conns may have differing PRAGMA state.
Fix: write `internal/store/migrations/002_multitenancy.sql` with `ALTER TABLE providers ADD COLUMN team_id TEXT NULL REFERENCES teams(id) ON DELETE CASCADE` AND set `_pragma=foreign_keys(1)` on every conn (already in `buildDSN`, but verify it sticks). Add `TestMigration_FKEnforcement` integration test.

**F-R3-ENG-2 — bcrypt cost=10 on inbound hot path = 60–100ms per request, fatal for streaming TTFB. [critical]**
Inbound auth runs bcrypt every request. M-class silicon: ~60ms; cheap VMs: 120–150ms. No verification cache. A coding agent doing 30 streams/min pays bcrypt 30 times.
Fix: add `internal/auth/cache.go` — `sync.Map` keyed by hash with TTL=60s, capacity-bounded. On hit: skip bcrypt. On miss: bcrypt + insert. Invalidate via process-wide `revocationEpoch atomic.Int64` bumped by `RevokeAPIKey()`; entries stamped with epoch at insert, force re-check if `entry.epoch < currentEpoch`. Document warm-path = ~5µs, cold-path = ~80ms. Optionally lower bcrypt cost to 8 (~15ms) if cache cannot be shipped.

**F-R3-ENG-3 — §Quota pseudocode contradicts FM-R3-3; current §Quota version is racy. [critical]**
§Quota shows `SELECT current; check; INSERT/UPDATE` (non-atomic). FM-R3-3 promises atomic `UPDATE...WHERE...RETURNING`. Two writers can both pass the SELECT.
Fix: replace §Quota body with FM-R3-3's single-statement form: `UPDATE usage_counters SET request_count = request_count + 1 WHERE api_key_id=? AND day=? AND request_count + 1 <= cap RETURNING *`. Two-phase reserve/commit for token caps (input estimated at reserve, real input+output at commit; rollback on stream error via `defer quota.RollbackReservation(reqID)`). Add `TestQuotaRace_100Goroutines_ExactlyCapAccepted` to `internal/quota/quota_test.go`.

**F-R3-ENG-4 — In-memory token bucket loses state on restart → deterministic burst exploit. [high]**
R3-10 admits restart-resets. Combined with `systemd` restart loops or OOM-induced restarts, adversary triggering restart resets the bucket. No documented burst tolerance contract.
Fix (TWO OPTIONS — surface at gate):
- (a) Document explicitly: "RPM is best-effort; over-burst window ≤ uptime since last restart, max burst = max_requests per restart event." Add `TestRPMBurst_AfterRestart_GrantsFullBucket` capturing the limitation.
- (b) Persist bucket state in `usage_counters` keyed by `unix_minute = unix_seconds/60`. One row per active key per minute. Adds ~0.5d to T19.
Lean: (a) for v0.1.5 (pragmatic + documented), (b) for v0.2 if real abuse seen.

**F-R3-ENG-5 — Prefix-collision probability non-negligible; schema doesn't enforce uniqueness. [high]**
12-char prefix has ~24–30 bits of random entropy after the `lgw_<scope>_` fixed bytes. At N=10000 keys, birthday-collision probability ~5–25%. `idx_api_keys_prefix` is non-unique. SELECT returns one row implicitly; on collision, lookup arbitrarily picks one.
Fix: widen `prefix` to first 16 chars (8 random base64 chars ≈ 48 bits, negligible at N=1M) AND make index UNIQUE: `CREATE UNIQUE INDEX idx_api_keys_prefix ON api_keys(prefix)`. Regenerate-on-collision at issuance in `Store.IssueAPIKey`. Add `TestPrefixCollision_RegenerateRetry`.

**F-R3-ENG-6 — `usage_counters.day` is wall-clock; DST creates 23h/25h buckets. [medium]**
`day = unix_millis / 86_400_000` server-local-TZ. DST spring-forward = 23h day, fall-back = 25h. A 90-min stream straddling midnight credits the start-day, doubling that bucket.
Fix: use UTC `day = time.Now().UTC().UnixMilli() / 86_400_000`. Credit usage at stream-START time (known) not completion. Document: "day is UTC; users in non-UTC TZ see midnight at non-local-midnight; intentional for v0.1.5." Add `TestUsageRollup_MidnightUTCBoundary`.

**F-R3-ENG-7 — `requireRole` chokepoint is single-point-of-failure; closure-based predicate has wide API surface. [high]**
"Single chokepoint" promised; reality: chokepoint PLUS N correctly-constructed predicates. If a tool handler forgets to resolve target team_id from request (e.g., `set_quota` scope=key needs to resolve `api_key.user.team_id`, not the request's claimed team_id), it leaks.
Fix: replace closure pattern with declarative ACL: `type ToolACL struct { Scope Scope; OwnershipResolver func(args) (team_id, error) }` registered in `aclRegistry map[string]ToolACL` at startup. MCP server calls `requireRole(ctx, registry[toolName])` before dispatch. Add `internal/mcp/rbac_matrix_test.go` with table-driven `TestRBACMatrix` walking every (tool, caller_scope, caller_team, target_team) tuple. Add `go-fuzz` target `FuzzRequireRole`.

**F-R3-ENG-8 — Migration idempotency relies on `user_version` but ALTERs need explicit txn wrapping. [high]**
`modernc.org/sqlite v1.x` embeds SQLite ≥3.45 (DDL-in-txn supported). Plan doesn't say "wrap migration in `BEGIN; ... COMMIT;` and bump `user_version` inside the txn." FM-R3-6's "failed ALTER leaves schema at v1" is only true if each ALTER auto-commits.
Fix: `internal/store/migrate.go` does `BEGIN IMMEDIATE; <ALTERs>; PRAGMA user_version=2; COMMIT;`. Verify embedded SQLite reports `>=3.35` at startup; refuse to start otherwise. Add `TestMigration_PartialFailure_RollsBack` injecting forced error mid-migration. Gate `--skip-migration` behind `--i-know-what-im-doing` flag.

**F-R3-ENG-9 — `model_aliases` PK isn't widened; two teams cannot have same alias name. [high]**
Original `model_aliases` PK is single-column `alias`. R3 adds `team_id` but doesn't redefine PK to `(alias, team_id)`. Team A `fast`→`glm-prod` + Team B `fast`→`deepseek` fails with UNIQUE constraint. Per-team alias feature silently broken.
Fix: SQLite can't ALTER PK. Migration must `CREATE TABLE model_aliases_v2 (alias TEXT NOT NULL, team_id TEXT REFERENCES teams(id) ON DELETE CASCADE, provider_name TEXT NOT NULL REFERENCES providers(name) ON DELETE CASCADE, upstream_model TEXT NOT NULL, created_at INTEGER NOT NULL, PRIMARY KEY (alias, team_id))`, `INSERT INTO model_aliases_v2 SELECT alias, NULL AS team_id, provider_name, upstream_model, created_at FROM model_aliases`, `DROP TABLE model_aliases`, `ALTER TABLE model_aliases_v2 RENAME TO model_aliases`. Update §Schema additions block to reflect this. Add `TestAlias_SameNameDifferentTeams_BothResolve`.

**F-R3-ENG-10 — Revocation race window exploitable. [medium]**
Q21 says in-flight finishes. But auth check happens BEFORE stream-open. Sequence: t=0 attacker sends; t=1 server starts auth lookup; t=2 admin revokes; t=3 lookup completes with stale cached `revoked_at IS NULL` and grants. Combined with F-R3-ENG-2's cache (60s TTL), gives 60s of post-revocation access.
Fix: epoch-gated cache from F-R3-ENG-2 fix. Add `TestRevocation_InvalidatesCache_WithinOneRequest` to `internal/auth/cache_test.go`.

**F-R3-ENG-11 — `api_keys.user_id ON DELETE CASCADE` silently destroys audit linkage. [medium]**
If a user is removed, all `api_keys` rows vanish, `usage_counters` rows vanish, `request_logs.api_key_id` becomes NULL. Lose attribution of past requests.
Fix: change to `ON DELETE RESTRICT` for `api_keys.user_id`; require explicit `revoke_api_key` before `remove_user`. Document: user-delete with active keys returns structured error pointing admin to revoke first.

**F-R3-ENG-12 — Token-scope rejection requires reading scope before bcrypt, but scope is in the bcrypt'd row. [medium]**
FM-R3-7: reject `scope != 'inbound'` at inbound. Q17: scope inferrable from prefix. Plan documents both mechanisms; pick one as authoritative.
Fix: first-line in `internal/auth/middleware.go`: `if !strings.HasPrefix(token, "lgw_ak_") && token != legacyToken { return 401 }`. DB-resident scope check is defense-in-depth backup. Add `TestInboundAuth_RejectsMCPScopeToken_NoDoublePath`.

### Architecture Dependency Diagram (R3-aware)

```
HTTP inbound (/v1/chat/completions, /v1/messages)
        |
        v
+----------------------------+
| auth.Middleware            |<--- LLM_GATEWAY_TOKEN (env, legacy fast-path; eq compare)
|  - prefix-scope precheck   |
|  - cache.Lookup(prefix)----+--> auth.Cache (sync.Map, TTL=60s, epoch-gated) <----+
|  - bcrypt.Compare (cold)   |                                                     |
|  - emit (team, user, key)  |                                                     |
+-------------+--------------+                                                     |
              |                                                                    |
              v                                                                    |
+----------------------------+                                                     |
| quota.Reserve(keyID)       |                                                     |
|  - single UPDATE-RETURNING |                                                     |
|  - rpm.TokenBucket (mem)   |                                                     |
+-------------+--------------+                                                     |
              v                                                                    |
+----------------------------+                                                     |
| router.Resolve(req,team)   |                                                     |
|  - alias (team OR NULL)    |                                                     |
|  - provider scoping        |                                                     |
+-------------+--------------+                                                     |
              v                                                                    |
        upstream call --> stream                                                   |
              |                                                                    |
              v                                                                    |
+----------------------------+                                                     |
| usage.Commit(keyID, toks)  |---> store.UPSERT usage_counters (day=UTC)           |
+----------------------------+                                                     |
                                                                                   |
MCP stdio inbound                                                                  |
        |                                                                          |
        v                                                                          |
+----------------------------+                                                     |
| mcp.Server (T7)            |                                                     |
|  - reads token from xport  |                                                     |
+-------------+--------------+                                                     |
              v                                                                    |
+----------------------------+                                                     |
| rbac.requireRole(ctx,acl)  |---> aclRegistry[toolName] (declarative, T16)        |
+-------------+--------------+                                                     |
              v                                                                    |
+----------------------------+                                                     |
| tools/{teams,users,keys,   |---- store.{Issue,Revoke}APIKey -----> bumps epoch --+
|  quotas,usage,logs}        |
+----------------------------+
```

### Test Plan Delta (12 new test files)

- `internal/store/migrate_test.go` — `TestMigrate_v1_to_v2_Idempotent`, `TestMigrate_PartialFailure_RollsBack`, `TestMigrate_FreshDB_GoesDirectlyToV2`, `TestMigrate_RejectsOldSQLite`
- `internal/store/api_keys_test.go` — `TestIssueAPIKey_ReturnsPlaintextOnce`, `TestPrefixCollision_RegenerateRetry`, `TestRevoke_Idempotent`, `TestRevoke_PreservesLogAttribution`, `TestBcrypt_CostFactor10`
- `internal/store/teams_test.go` — `TestAddTeam_SlugUnique`, `TestRemoveTeam_CascadesToOwnedResources`, `TestRemoveTeam_RefusesIfActiveKeys` (per F-R3-ENG-11)
- `internal/store/aliases_test.go` — `TestAlias_SameNameDifferentTeams_BothResolve`, `TestAlias_GlobalFallback_WhenTeamAliasMissing`, `TestAlias_OrphanReturnsErrNoRoute` (regression for F-11)
- `internal/store/usage_test.go` — `TestUsage_UpsertConcurrent`, `TestUsageRollup_MidnightUTCBoundary`, `TestUsage_ReservationRolledBackOnStreamError`
- `internal/auth/middleware_test.go` — `TestLegacyToken_FastPath_SkipsBcrypt`, `TestNewKey_BcryptVerify`, `TestRevokedKey_Rejected`, `TestExpiredKey_Rejected`, `TestMCPScopeToken_RejectedAtInbound` (F-R3-ENG-12), `TestNoToken_401WithFix`
- `internal/auth/cache_test.go` — `TestCache_HitSkipsBcrypt`, `TestCache_EvictsOnTTL`, `TestRevocation_InvalidatesCache_WithinOneRequest`, `TestCache_EpochGated`
- `internal/quota/quota_test.go` — `TestQuotaRace_100Goroutines_ExactlyCapAccepted`, `TestQuota_KeyBeatsTeam`, `TestQuota_TokenCap_ReserveCommitTwoPhase`, `TestQuota_429ResponseShape`
- `internal/quota/rpm_test.go` — `TestRPMBurst_FreshBucket`, `TestRPMBurst_AfterRestart_GrantsFullBucket`, `TestRPMBurst_AcrossKeys_Isolated`
- `internal/mcp/rbac_test.go` — `TestRequireRole_RejectsRevoked`, `TestRequireRole_ScopeMismatch`, `TestRequireRole_TeamMismatch`
- `internal/mcp/rbac_matrix_test.go` — table-driven `TestRBACMatrix` covering all 14 rows × 3 scopes; `FuzzRequireRole` go-fuzz target
- `internal/integration/upgrade_v1_to_v2_test.go` — boot v0.1 binary, write rows, boot v0.1.5, assert read-through; covers the upgrade migration path end-to-end

### Verdict

**REJECT** — F-R3-ENG-2 (bcrypt-on-every-request kills streaming TTFB), F-R3-ENG-3 (race vs FM-R3-3 contract contradiction), F-R3-ENG-9 (alias PK silently breaks per-team alias feature) are showstoppers as written. Each is fixable in <0.5d, but the plan must be patched before T14 starts.

### Single Most Important Fix

Add `internal/auth/cache.go` with epoch-gated in-memory verification cache (TTL=60s) AND atomically resolve the §Quota / FM-R3-3 contradiction by adopting the single-statement `UPDATE usage_counters SET request_count = request_count + 1 WHERE request_count + 1 <= cap RETURNING *` form as the sole contract. Without these two, every inbound request costs 60–100ms and the documented quota guarantee is unenforceable, negating v0.1.5's value proposition.

### Auto-Decided Eng Findings Summary

| # | Severity | Auto-decided? | Decision |
|---|---|---|---|
| F-R3-ENG-1 | high | YES — apply fix | mechanical (P5) — explicit DEFAULT NULL + DSN pragma audit |
| F-R3-ENG-2 | critical | YES — apply fix | mechanical engineering need; no taste decision on whether-to-cache |
| F-R3-ENG-3 | critical | YES — apply fix | correctness; replace §Quota body with UPDATE-RETURNING |
| F-R3-ENG-4 | high | NO — surface at gate | (a) doc-the-limitation vs (b) SQLite-backed bucket; user picks |
| F-R3-ENG-5 | high | YES — apply fix | mechanical (P1 completeness) — 16-char UNIQUE prefix + regen-on-collision |
| F-R3-ENG-6 | medium | YES — apply fix | UTC + credit-at-start; mechanical |
| F-R3-ENG-7 | high | YES — apply fix | declarative ACL registry (P5 explicit) + matrix test |
| F-R3-ENG-8 | high | YES — apply fix | transactional migration + sqlite version check |
| F-R3-ENG-9 | high | YES — apply fix | feature would silently break otherwise; required correctness |
| F-R3-ENG-10 | medium | YES — apply fix | epoch-gated cache (subsumed by ENG-2 fix) |
| F-R3-ENG-11 | medium | YES — apply fix | ON DELETE RESTRICT preserves audit; mechanical |
| F-R3-ENG-12 | medium | YES — apply fix | prefix-scope precheck = defense-in-depth, mechanical |

11 auto-decided fixes; only F-R3-ENG-4 (token bucket strategy) gets surfaced at the gate as a user taste decision.

---

## Plan Revision 3 — DX Review (2026-05-13)

**Mode:** /autoplan Phase 3.5, dual-voice attempted; Codex `[codex-unavailable]`. Single-voice review by Claude subagent, marked `[subagent-only]`.

### DX Consensus Table (single voice — most cells N/A)

| Dimension | Claude subagent | Codex | Consensus |
|---|---|---|---|
| 1. Getting started < 5 min? | ✗ (8–18 min super_admin TTHW) | N/A | FLAGGED |
| 2. API/CLI naming guessable? | Mixed (verb inconsistency) | N/A | FLAGGED |
| 3. Error messages actionable? | ✗ (only 429 specified) | N/A | FLAGGED |
| 4. Docs findable & complete? | ✗ (T20 undercooked) | N/A | FLAGGED |
| 5. Upgrade path safe? | Mixed (silent upgrade) | N/A | FLAGGED |
| 6. Dev environment friction-free? | ✗ (5 client configs, no helper) | N/A | FLAGGED |

### Developer Journey Map (9-stage, condensed)

| Stage | Time | Top friction |
|---|---|---|
| 1. Download | 30s | Install line unspecified (go install / GH release / Homebrew) |
| 2. Init | 30s | No terminal-output sample for v0.1.5; legacy vs new token format unclear |
| 3. Start | 10s | No "v0.1.5; legacy detected; default team synthesized" banner spec |
| 4. **Connect MCP** | **5–15 min** | **Biggest hidden cost: 5 client config formats, 0 committed snippets** |
| 5. Create team | 5s | `add_team` request/response shape undefined in plan |
| 6. Create user | 5s | `users.email` is identity-without-auth (echoes CEO-6) |
| 7. Issue API key | 10s | Plaintext shown ONCE; no response shape; no recovery if lost |
| 8. Distribute key | varies | Zero affordances; out-of-band by design, undocumented |
| 9. First inbound | 30s | Header matrix (`Authorization: Bearer` vs `x-api-key`) per endpoint unlocked |

**Super_admin TTHW:** 8–18 min, dominated by client config editing. **Normal_admin recipient TTHW:** 3–10 min with snippets, 20+ without.

### Findings (12)

**F-R3-DX-1 — Boot output unspecified; silent upgrade = silent confusion. [critical]**
No "what's new" banner, no `init` output spec, no migration-success line. Fix: T11/T15 acceptance must include exact stdout lines covering schema version, migration applied, legacy env detection, synthesized team, and next-step command.

**F-R3-DX-2 — MCP client snippet coverage undefined; T20 is half a day for an unspecified deliverable. [critical]**
Fix: name the 5 clients (Claude Desktop, Cline, Cursor, Continue, ZED), commit to one canonical config per client in T20, AND add `llm-gateway mcp-config --client=<name>` subcommand to T11 scope (~0.25d). Subcommand pays back T20 effort and erases the 10-min friction point.

**F-R3-DX-3 — Token-format scope confusion is built into the design. [high]**
`lgw_ak_*` / `lgw_msu_*` / `lgw_mna_*` are opaque. Operator looking at three strings can't tell which is inbound vs MCP admin vs MCP normal. Fix: rename to human-readable `lgw_inbound_*`, `lgw_admin_*`, `lgw_team_*`. At MCP transport, reject `lgw_inbound_*` with: "this is an inbound API key, paste it into your client app, not your MCP config."

**F-R3-DX-4 — Lost-key recovery is unspecified. [high]**
v0.1 has env fallback as recovery; v0.1.5's legacy fast-path IS the recovery path but never stated. Fix: add "Recovery" subsection: "lost all `mcp_super` tokens → restart with `LLM_GATEWAY_TOKEN=<anything-new>`; legacy fast-path resynthesizes super_admin; `issue_api_key scope=mcp_super`; unset env."

**F-R3-DX-5 — Error message audit is partial; only 429 specified. [high]**
Fix: spec 401 (4 distinct `type`s: revoked / expired / scope_mismatch / unknown_key), 403 (forbidden with `required_scope` + `your_scope` + `fix`), 404 (not_found with resource + id + fix), 409 (conflict with field + value + fix). Add error reference table to T20.

**F-R3-DX-6 — `whoami` payload undefined; agent tool-discovery broken. [high]**
RBAC matrix lists `whoami` but no return shape. Fix: spec response as `{scope, team_id, team_slug, user_email, key_name, available_tools, unavailable_tools_with_reason}`. Agent calls `whoami` on handshake; learns its tier and capabilities in one round-trip.

**F-R3-DX-7 — MCP tool filtering vs blanket 403 is unaddressed. [high]**
Fix: pick (ii) — MCP `tools/list` response is scope-filtered at handshake. Super-only tools never appear for `mcp_normal` agents. Surface them in `whoami.unavailable_tools` with reason for explainability.

**F-R3-DX-8 — README delta scope undercooked at 0.5d for 7 deliverables. [medium]**
Fix: budget T20 = 1.0d, OR scope to 3 sections + inline tool docstrings.

**F-R3-DX-9 — `init` doesn't bootstrap teams in v0.1.5. [medium]**
Fix: add `init --team=<slug>` flag that runs add_team + add_user + issue_api_key inline (~0.25d to T11). Zero-MCP-roundtrip onboarding to multi-tenant state.

**F-R3-DX-10 — "Distribute key" stage has zero affordances. [medium]**
Fix: at minimum document "out-of-band — use your team's existing secret channel" in T20. Optional: `--share-via=op` shortcut.

**F-R3-DX-11 — `list_api_keys` lacks `remaining_quota_today`. [medium]**
Fix: include `remaining_requests_today`, `remaining_tokens_today`, `quota_resets_at` in response. ~0.1d incremental.

**F-R3-DX-12 — Tool naming inconsistency invites guessing. [medium]**
`add` / `issue` / `set` / `get` / `tail` verb mix. Agent guessing "delete a team" tries `delete_team` first. Fix: pick `create_*` / `delete_*` OR `add_*` / `remove_*`. Apply uniformly. Document convention in T20.

### DX Scorecard (rate / 10)

| Dimension | Score | Note |
|---|---|---|
| Getting started TTHW | 3 | 8–18 min super_admin without mcp-config helper |
| API/CLI naming guessability | 5 | Token-scope prefixes opaque; verbs inconsistent |
| Error message quality | 4 | Only 429 spec'd; 401/403/404/409 missing |
| Docs findability/completeness | 3 | T20 half a day for 7 deliverables |
| Upgrade path safety | 6 | Schema OK; user-facing narrative missing |
| Dev environment friction | 4 | 5 clients, 0 snippets, no helper subcommand |
| AI-agent operability | 4 | whoami underspec'd, tool-list filter unaddressed |
| Recovery from mistakes | 5 | Recovery exists (legacy env) but unwritten |
| **Overall** | **4.25** | |

### Verdict

**APPROVE_WITH_CONCERNS** — multi-tenancy design is solid; DX surface is half-built. Findings DX-1, DX-2, DX-3 must land before T17/T20 start or v0.1.5 ships unusable to humans.

### Single Most Important Fix

Add `llm-gateway mcp-config --client=<claude|cline|cursor|continue|zed>` subcommand to T11 (~0.25d), AND require T11/T15 to emit a deterministic boot banner with schema version, migration result, legacy-env status, synthesized team, and a single copy-pasteable next-step command. Together they convert the operator's first 10 minutes from "edit five JSON files, guess at three opaque tokens" into "copy this line, paste, done."

### Auto-Decided DX Findings Summary

| # | Severity | Auto-decided? | Decision |
|---|---|---|---|
| F-R3-DX-1 | critical | YES — apply fix | spec exact boot banner lines in T11/T15 acceptance |
| F-R3-DX-2 | critical | YES — apply fix | name 5 MCP clients + add `mcp-config` subcommand to T11 (+0.25d) |
| F-R3-DX-3 | high | NO — surface at gate | `lgw_inbound_*` (readable) vs `lgw_ak_*` (compact) — taste decision |
| F-R3-DX-4 | high | YES — apply fix | document recovery via legacy env fast-path (P5 explicit) |
| F-R3-DX-5 | high | YES — apply fix | spec 4 missing error shapes; P1 completeness |
| F-R3-DX-6 | high | YES — apply fix | spec `whoami` response shape; required for agent discoverability |
| F-R3-DX-7 | high | YES — apply fix | pick (ii) tool filtering at MCP handshake; AI-native answer (P5+P6) |
| F-R3-DX-8 | medium | YES — apply fix | bump T20 to 1.0d |
| F-R3-DX-9 | medium | NO — surface at gate | `init --team` flag (+0.25d) — real scope expansion, user picks |
| F-R3-DX-10 | medium | YES — apply fix | document secure handoff in T20 (zero code) |
| F-R3-DX-11 | medium | NO — surface at gate | `remaining_quota_today` in `list_api_keys` — minor scope, user picks |
| F-R3-DX-12 | medium | NO — surface at gate | verb-pair choice: add/remove vs create/delete — taste decision |

8 auto-decided fixes; 4 surface-at-gate (DX-3, DX-9, DX-11, DX-12).

---

## Plan Revision 3 — Cross-phase themes (2026-05-13)

These concerns surfaced INDEPENDENTLY in 2+ phases — high-confidence signals worth special attention at the gate.

- **Quota correctness contradiction** — flagged by CEO (F-R3-CEO-9) AND Eng (F-R3-ENG-3). Both phases independently caught that §Quota pseudocode contradicts FM-R3-3. Auto-decided fix already converges on UPDATE-RETURNING form.
- **`users` table as identity-without-auth** — CEO (F-R3-CEO-6, "single most important change") + DX (F-R3-DX-6 walked into same issue when defining `whoami.user_email`). Both saw the same problem from different angles. Surface at gate as User Challenge.
- **Premise gate option C may have been wrong** — CEO (F-R3-CEO-2) is explicit. Eng's verdict REJECT + showstopper count (3 critical, 7 high) reinforces that the additive layer is more invasive than the framing suggested. Surface at gate as the load-bearing User Challenge.
- **Two REJECT verdicts + one APPROVE_WITH_CONCERNS** — CEO REJECT, Eng REJECT, DX APPROVE_WITH_CONCERNS. The two REJECTs both name premise + scope concerns; DX names execution-surface concerns. R3 cannot proceed to T14 implementation without resolution.

---

## Plan Revision 3 — Decision Audit Trail (autoplan auto-decisions, R3 pass)

| # | Phase | Decision | Classification | Principle | Rationale |
|---|---|---|---|---|---|
| AD-R3-1 | CEO-5 | Add `providers.fallback_eligible BOOLEAN DEFAULT 0` + denormalized `request_logs.provider_owner_team_id`; Q22 default = OFF | mechanical | P5 (explicit over clever) | Data-leakage prevention; default-off matches least-surprise |
| AD-R3-2 | CEO-7 | Add honest effort comparison (B halt+redo vs C additive) to Decision Audit Trail | mechanical | P5 | Surfaces the real tradeoff numbers |
| AD-R3-3 | CEO-9 | Replace §Quota pseudocode with FM-R3-3's atomic UPDATE-RETURNING form | mechanical | Correctness | Resolves internal contradiction |
| AD-R3-4 | CEO-10 | Remove `--skip-migration` from FM-R3-6 (false comfort); document "no rollback in v0.1.5; downgrade requires fresh state.db" | mechanical | P5 + honesty | False rollback story removed |
| AD-R3-5 | ENG-1 | Migration uses explicit `ALTER TABLE x ADD COLUMN y TEXT NULL REFERENCES ... ON DELETE CASCADE` + DSN pragma audit | mechanical | Correctness | Foreign keys enforced as designed |
| AD-R3-6 | ENG-2 | Add `internal/auth/cache.go` with epoch-gated in-mem verification cache (TTL=60s, capacity-bounded) | mechanical | Critical perf fix | Cold path 80ms, warm 5µs |
| AD-R3-7 | ENG-3 | Adopt UPDATE-RETURNING atomic single-statement form for quota reserve; two-phase reserve/commit for token caps; add 100-goroutine race test | mechanical | Correctness | Quota guarantee enforceable |
| AD-R3-8 | ENG-5 | Widen prefix to 16 chars + `UNIQUE INDEX idx_api_keys_prefix` + regen-on-collision at issuance | mechanical | P1 completeness | Collision-free at N=1M keys |
| AD-R3-9 | ENG-6 | `usage_counters.day` is UTC-bucketed; credit usage at stream-start | mechanical | Correctness | DST-immune |
| AD-R3-10 | ENG-7 | Replace closure `requireRole` with declarative `aclRegistry[toolName] -> ToolACL{Scope, OwnershipResolver}` + table-driven `TestRBACMatrix` + `FuzzRequireRole` | mechanical | P5 (explicit) + security | Single chokepoint actually single |
| AD-R3-11 | ENG-8 | Migration wrapped in `BEGIN IMMEDIATE; ... PRAGMA user_version=2; COMMIT;` + SQLite version check at startup (≥3.35) | mechanical | Correctness | Atomic schema upgrade |
| AD-R3-12 | ENG-9 | `model_aliases` PK widened to `(alias, team_id)` via table-rename migration (CREATE _v2, INSERT, DROP, RENAME) | mechanical | Feature correctness | Per-team aliases actually work |
| AD-R3-13 | ENG-10 | Revocation invalidates auth cache via epoch bump (subsumed by AD-R3-6) | mechanical | Security | No 60s post-revoke window |
| AD-R3-14 | ENG-11 | `api_keys.user_id` becomes `ON DELETE RESTRICT`; remove_user with active keys returns structured error | mechanical | Audit preservation | Past attribution survives user-delete |
| AD-R3-15 | ENG-12 | Inbound auth middleware first-line: `if !strings.HasPrefix(token,"lgw_<inbound>_") && token != legacyToken { 401 }` (defense-in-depth before DB) | mechanical | Security | Cross-scope token use rejected at HTTP layer |
| AD-R3-16 | DX-1 | T11/T15 acceptance must include exact stdout banner (schema version, migration result, legacy detection, synthesized team, next-step command) | mechanical | P5 explicit | Silent upgrade eliminated |
| AD-R3-17 | DX-2 | Name 5 MCP clients (Claude Desktop, Cline, Cursor, Continue, ZED); add `llm-gateway mcp-config --client=<name>` subcommand to T11 (+0.25d) | mechanical | P2 boil lakes within blast radius | Erases 10-min onboarding friction |
| AD-R3-18 | DX-4 | Add "Recovery" subsection documenting legacy-env-fastpath as official lost-key recovery procedure | mechanical | P5 explicit | Recovery path exists, now written |
| AD-R3-19 | DX-5 | Spec 4 missing error shapes (401 with 4 subtypes; 403; 404; 409); add error reference table to T20 | mechanical | P1 completeness | All paths have problem+cause+fix |
| AD-R3-20 | DX-6 | Spec `whoami` response: `{scope, team_id, team_slug, user_email, key_name, available_tools, unavailable_tools_with_reason}` | mechanical | P5 + AI-native | Agent learns capabilities in one call |
| AD-R3-21 | DX-7 | MCP `tools/list` response scope-filtered at handshake; super-only tools hidden from normal_admin agents (with `unavailable_tools` reason exposed via whoami) | mechanical | P6 AI-native + P1 | No blind 403 attempts |
| AD-R3-22 | DX-8 | Bump T20 budget to 1.0d | mechanical | P1 completeness | 7 deliverables ≠ 0.5d |
| AD-R3-23 | DX-10 | Document secure handoff in T20 README ("out-of-band via team's existing secret channel"); zero code | mechanical | P5 explicit | Stage-8 gap closed |

23 mechanical fixes auto-decided. Each will be applied to the R3 charter once the gate-level user-challenges (CEO-1/2/3/4/6/8, ENG-4, DX-3/9/11/12) are resolved.

---

## Plan Revision 3 FINAL — Halt+Redo to Multi-tenant v0.1 (2026-05-13)

**Status:** APPROVED via /autoplan Phase 4 user-challenge gate (option B2, then individual UC answers). The v0.1.5 versioning is collapsed; multi-tenancy folds into v0.1. Previous R3 draft + CEO/Eng/DX reviews above are retained as audit trail.

### User decisions at final gate

| Question | Answer | Effect |
|---|---|---|
| UC-R3-1 (premise) | **B halt+redo** | v0.1 is re-architected to ship as multi-tenant single product. No v0.1.5. |
| UC-R3-2 (users table) | **A delete** | api_keys directly references team_id. `api_keys.created_for_label TEXT` carries human-readable attribution. No users table. v0.2 may re-introduce when real auth (OAuth/SSO) lands. |
| UC-R3-3 (quota+$) | **A add $-billing** | `model_costs(provider, model, usd_per_input_1k, usd_per_output_1k)` table; `get_usage` returns tokens + `$_today`; quotas support both token and $ caps. |
| UC-R3-4 (RBAC) | **B widen enum, implement 2** | `api_keys.scope` enum schema = `{inbound, mcp_super, mcp_admin, mcp_auditor, mcp_billing}`. v0.1 implements `inbound` + `mcp_super` + `mcp_admin` (rename of `mcp_normal` per Eng-7 declarative ACL). `mcp_auditor` + `mcp_billing` return `{type:"scope_not_implemented_in_v0.1", fix:"upgrade to v0.2 when available"}`. |
| UC-R3-5 (legacy env) | **A permanent** | `LLM_GATEWAY_TOKEN` env stays forever as bootstrap + lost-key recovery mechanism. Print at boot when active. |
| UC-R3-6 (competitive) | **A add subsection** | New "Competitive delta vs LiteLLM" subsection after §1 (~250 words; below). |
| TD-1 (token format) | accept lean | Readable: `lgw_inbound_<base64>`, `lgw_admin_<base64>`, `lgw_team_<base64>` |
| TD-2 (verbs) | accept lean | `add_*` / `remove_*` (matches existing code) |
| TD-3 (`init --team`) | accept lean | `llm-gateway init --team=<slug>` creates team + super_admin + first key inline (+0.25d) |
| TD-4 (`list_api_keys` quota) | accept lean | Returns `remaining_requests_today` + `remaining_tokens_today` + `quota_resets_at` (+0.1d) |
| TD-5 (RPM bucket) | accept lean | In-memory token bucket per key; documented as "best-effort, resets on restart" |

### Premise re-statement (overwrites §1 P1)

P1' — **v0.1 ships multi-tenant single product.** Teams, users-via-api-keys (no separate users table), per-user API keys, MCP `super_admin` / `admin` RBAC tiers (with 2 stub tiers reserved), per-team optional providers with global fallback, token + $ quotas, RPM rate limiting. Legacy `LLM_GATEWAY_TOKEN` env is a permanent bootstrap+recovery option, NOT a deprecated path.

P2–P6 unchanged from §1.

### Competitive delta vs LiteLLM (per UC-R3-6, insert after §1)

LiteLLM (Apache 2.0, ~3 years old, large team) is the obvious comparison: same surface (proxy multiple LLM providers), broader provider coverage, multi-tenancy in OSS. `llm-gateway` does NOT compete on provider coverage and does NOT try to.

Three axes where `llm-gateway` is intentionally NOT LiteLLM:

1. **AI-native MCP-only control plane.** LiteLLM operates via REST admin API + optional UI. `llm-gateway` operates via MCP stdio exclusively — every operation (issue key, set quota, view usage) is an agent-typed tool call. An operator who uses Cline or Claude Code to write code uses the SAME interface to operate the gateway. No second UI, no second mental model.
2. **Chinese-provider-first.** LiteLLM treats GLM/DeepSeek as two of ~100 providers, with minimal awareness of their dual-protocol native support, reasoning-content field semantics, or DeepSeek's `/anthropic` base URL. `llm-gateway` is designed against these provider specifics first (see R2 spike + Q14 reasoning passthrough).
3. **Single static Go binary, no Python runtime, no external state.** LiteLLM needs Python + PostgreSQL/Redis for production. `llm-gateway` is one binary + one SQLite file. The deployment cost difference is 30× (one file vs Python venv + DB ops + container) for the same single-team self-hosted use case.

When future scope discussions arise ("should we add metric X?"), the test is: does it deepen one of these three moats? If yes, ship. If it's LiteLLM-parity, defer to v0.2 or decline.

### Final schema (one migration v0 → v1 for fresh installs)

```sql
-- 1) Teams + API keys (no users table)
CREATE TABLE teams (
  id          TEXT PRIMARY KEY,        -- tm_<rand>
  slug        TEXT NOT NULL UNIQUE,
  name        TEXT NOT NULL,
  created_at  INTEGER NOT NULL
);

CREATE TABLE api_keys (
  id                  TEXT PRIMARY KEY,        -- ak_<rand>
  team_id             TEXT NOT NULL REFERENCES teams(id) ON DELETE RESTRICT,
  hash                TEXT NOT NULL,           -- bcrypt cost=10
  prefix              TEXT NOT NULL,           -- first 16 chars; UNIQUE indexed
  created_for_label   TEXT,                    -- "Cline laptop", "joy@anthropic.com CI"
  scope               TEXT NOT NULL,           -- inbound | mcp_super | mcp_admin | mcp_auditor | mcp_billing
  revoked_at          INTEGER,
  expires_at          INTEGER,
  last_used_at        INTEGER,
  created_at          INTEGER NOT NULL,
  CHECK (scope IN ('inbound','mcp_super','mcp_admin','mcp_auditor','mcp_billing'))
);
CREATE UNIQUE INDEX idx_api_keys_prefix ON api_keys(prefix);
CREATE INDEX idx_api_keys_team ON api_keys(team_id);

-- 2) Providers — team_id NULL = global default; per-team override
CREATE TABLE providers (
  name              TEXT PRIMARY KEY,
  kind              TEXT NOT NULL,
  team_id           TEXT REFERENCES teams(id) ON DELETE CASCADE,
  openai_base_url   TEXT,
  anthropic_base_url TEXT,
  api_key           TEXT NOT NULL,
  anthropic_version TEXT NOT NULL DEFAULT '2023-06-01',
  is_default        INTEGER NOT NULL DEFAULT 0,
  fallback_eligible INTEGER NOT NULL DEFAULT 0,   -- per CEO-F5: global serves non-owning teams only if opted-in
  created_at        INTEGER NOT NULL,
  CHECK (openai_base_url IS NOT NULL OR anthropic_base_url IS NOT NULL)
);

-- 3) Aliases — composite PK so multiple teams can share an alias name
CREATE TABLE model_aliases (
  alias          TEXT NOT NULL,
  team_id        TEXT REFERENCES teams(id) ON DELETE CASCADE,  -- NULL = global alias
  provider_name  TEXT NOT NULL REFERENCES providers(name) ON DELETE CASCADE,
  upstream_model TEXT NOT NULL,
  created_at     INTEGER NOT NULL,
  PRIMARY KEY (alias, team_id)
);

-- 4) Request logs (no migration needed; multi-tenant fields included from day one)
CREATE TABLE request_logs (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  ts                INTEGER NOT NULL,
  api_key_id        TEXT REFERENCES api_keys(id) ON DELETE SET NULL,
  team_id           TEXT,                          -- denormalized for fast filter
  provider_owner_team_id TEXT,                     -- per CEO-F5: disambiguate team-billed vs global-billed
  client_model      TEXT NOT NULL,
  resolved_model    TEXT NOT NULL,
  provider_name     TEXT NOT NULL,
  input_tokens      INTEGER,
  output_tokens     INTEGER,
  reasoning_tokens  INTEGER,
  total_tokens      INTEGER,
  latency_ms        INTEGER,
  status            TEXT NOT NULL,
  error_msg         TEXT,
  prompt_excerpt    TEXT,
  cost_usd_micros   INTEGER                        -- usage_$ * 1_000_000 for integer arithmetic
);
CREATE INDEX idx_logs_ts ON request_logs(ts DESC);
CREATE INDEX idx_logs_team ON request_logs(team_id);
CREATE INDEX idx_logs_key ON request_logs(api_key_id);

-- 5) Usage counters (atomic UPSERT in hot path; UTC days only per ENG-F6)
CREATE TABLE usage_counters (
  api_key_id       TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
  day_utc          INTEGER NOT NULL,        -- (UnixMilli / 86_400_000) using UTC time
  request_count    INTEGER NOT NULL DEFAULT 0,
  input_tokens     INTEGER NOT NULL DEFAULT 0,
  output_tokens    INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens INTEGER NOT NULL DEFAULT 0,
  cost_usd_micros  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (api_key_id, day_utc)
);

-- 6) Quotas — both token caps AND $ caps (per UC-R3-3)
CREATE TABLE quotas (
  scope_kind     TEXT NOT NULL,             -- "team" | "key"
  scope_id       TEXT NOT NULL,
  window         TEXT NOT NULL,             -- "month" | "day" | "minute"
  max_requests   INTEGER,
  max_tokens     INTEGER,
  max_usd_micros INTEGER,                   -- $ cap (token-cap * usd-per-token)
  PRIMARY KEY (scope_kind, scope_id, window),
  CHECK (scope_kind IN ('team','key')),
  CHECK (window IN ('month','day','minute'))
);

-- 7) Model costs — for $ accounting (per UC-R3-3)
CREATE TABLE model_costs (
  provider              TEXT NOT NULL,
  model                 TEXT NOT NULL,
  usd_per_input_1k      REAL NOT NULL,      -- e.g., 0.00014 for deepseek-v4-flash input
  usd_per_output_1k     REAL NOT NULL,
  usd_per_reasoning_1k  REAL,               -- NULL for non-reasoning models
  effective_from        INTEGER NOT NULL,
  PRIMARY KEY (provider, model, effective_from)
);
```

### Final v0.1 task list (halt+redo: replaces §4 task table)

| # | Task | Status | Effort | Notes |
|---|---|---|---|---|
| T0 | Wizard-of-Oz P6 validation | DONE | | already shipped |
| T1 | HTTP server + bearer auth middleware | DONE | | bearer auth becomes API key lookup in T1.5 |
| T1.5 | IR types (Anthropic-aligned superset) | DONE | | unchanged |
| T2a | DeepSeek passthrough adapter | DONE | | unchanged |
| T4 | SQLite store (single migration v0→v1 with FINAL schema above) | **NEEDS REWORK** | 1.5d | re-do schema migration to include all tables; existing v1 state.db is dev-only, re-init OK |
| T5 | Router team-scoping + per-team provider fallback | **NEEDS REWORK** | 0.75d | extends existing router with team_id predicate + fallback_eligible check |
| T6 | Inbound auth path: API key lookup + cache + legacy fast-path | **NEW (folds in T15)** | 1d | replaces simple bearer; `internal/auth/cache.go` epoch-gated |
| T7 | MCP server stdio + declarative ACL registry (`requireRole`) | **IN PROGRESS, RE-SCOPE** | 1.5d | currently mid-impl; halt + redo to include ACL chokepoint from start |
| T8 | MCP tools: provider/alias (team-scoped) — `add_provider`/`set_model_alias`/`list_providers`/`list_model_aliases`/`remove_provider`/`remove_alias` | NEW | 0.75d | all team-scoped from day one |
| T9 | MCP tools: tenancy — `add_team`/`remove_team`/`list_teams`/`issue_api_key`/`list_api_keys`/`revoke_api_key`/`set_quota`/`get_usage`/`tail_logs`/`get_request`/`whoami` | NEW | 1.5d | super+admin scopes; auditor/billing return `scope_not_implemented_in_v0.1` |
| T10 | Quota enforcement middleware (atomic UPDATE-RETURNING) + in-mem RPM bucket + reserve/commit token quota | NEW | 1d | per ENG-F3 atomic single-statement form |
| T11 | init subcommand + `--team=<slug>` flag + boot banner + `mcp-config --client=<n>` subcommand | **NEEDS REWORK** | 1d | adds `--team` flag (TD-3) + `mcp-config` subcommand (DX-F2) + deterministic boot banner (DX-F1) |
| T11.5 | model_costs seed data for DeepSeek + GLM published prices | NEW | 0.25d | one CSV import; values from vendor docs |
| T12 | goreleaser + GitHub Actions CI | unchanged | 0.5d | |
| T13 | README v0.1 with 5 MCP client snippets + onboarding flow + error reference + recovery procedure | **NEEDS REWORK** | 1d | per DX-F8 bumped to 1d |
| **v0.1 total** | | | **~12 工程日** | (vs original 11.5; ~ +6.5d from R3 absorption, ~ -5d gained by NOT writing v1→v2 migration) |

Tasks **bold-marked NEEDS REWORK** require pausing current code where applicable and re-architecting. T0/T1/T1.5/T2a already-shipped code stays as-is (it doesn't conflict with the new schema; just gets extended).

### Auto-decided patches (23 mechanical fixes, applied to FINAL)

All 23 fixes from the Decision Audit Trail above are now incorporated into FINAL:
- AD-R3-1 → providers.fallback_eligible included in final schema
- AD-R3-2 → effort table above shows halt+redo delta
- AD-R3-3 → quota middleware uses UPDATE-RETURNING (T10)
- AD-R3-4 → migration is one-shot v0→v1, no rollback story needed
- AD-R3-5..8 → all schema/index choices in FINAL match
- AD-R3-9 → usage_counters.day_utc explicitly UTC
- AD-R3-10 → declarative ACL registry is the chokepoint (T7)
- AD-R3-11 → migration in single BEGIN IMMEDIATE txn (T4)
- AD-R3-12 → model_aliases PK is composite (alias, team_id) in FINAL schema
- AD-R3-13 → epoch-gated cache (T6)
- AD-R3-14 → api_keys.team_id ON DELETE RESTRICT (FINAL schema)
- AD-R3-15 → inbound auth prefix-scope precheck (T6)
- AD-R3-16 → boot banner spec in T11 acceptance
- AD-R3-17 → `mcp-config` subcommand in T11
- AD-R3-18 → recovery via legacy env documented in T13 README
- AD-R3-19 → 4 missing error shapes spec'd; error reference in T13
- AD-R3-20 → `whoami` response shape in T9 spec
- AD-R3-21 → MCP tools/list scope-filtered at handshake (T7)
- AD-R3-22 → T13 budget = 1d
- AD-R3-23 → secure handoff documented in T13

### Open Questions consolidated (R3 + carried-over R2)

Q13 (GLM Anthropic endpoint), Q14 (reasoning passthrough), Q15 (signature field), Q16 (SSE byte fidelity), Q17 (token format = lgw_inbound_/lgw_admin_/lgw_team_ per TD-1), Q18 (bcrypt cost=10), Q19 (UTC calendar day), Q20 (synchronous usage UPSERT), Q21 (revocation: in-flight finishes), Q22 (per-team provider visibility: strict isolation; fallback_eligible default OFF per CEO-F5), Q23 (`add_team` with no auto-user; first key issued via `init --team` flag or `issue_api_key`), Q24 (existing single-provider config stays global; no auto-migration to a team).

### What this revision LEAVES OPEN (v0.2+)

- Real auth (OAuth/SSO/passwords) → v0.2 brings back `users` table with real auth meaning
- HTTP/SSE MCP transport → v0.2 (TODOS.md UC-4 unchanged)
- Docker container → v0.2 unchanged
- Encryption-at-rest for `providers.api_key` (F-DX-09) → v0.2 unchanged
- Admin action audit log (`admin_audit` table) → v0.1.6 if demand emerges
- SQLite-backed RPM bucket persistence → v0.2 if real abuse observed
- `mcp_auditor` + `mcp_billing` scope implementations → v0.2

### Next concrete step

1. **Commit this FINAL revision to git** (commit message: `plan: REVISION-3 FINAL — halt+redo to multi-tenant v0.1`).
2. **Pause Task 7 in-progress code.** The MCP server scaffolding already started will become the foundation for T7's declarative ACL registry — none of it is wasted, but it needs to be re-architected to include the ACL chokepoint from start.
3. **Re-do Task 4** (store schema) FIRST per the new dependency order. This is the foundation everything else builds on. ~1.5d.
4. Continue T5/T6/T7 per the new task table.

---

<!-- /autoplan restore point: /Users/panda/.gstack/projects/llm-gateway/main-autoplan-restore-20260513-204435.md (R3 pass; original plan preserved verbatim there) -->

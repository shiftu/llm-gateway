# llm-gateway — Implementation Plan

> Working plan file. Used by `/plan-ceo-review`, `/plan-eng-review`, `/plan-devex-review` via the `/autoplan` chain. Design doc lineage: `~/.gstack/projects/llm-gateway/panda-main-design-20260513-040833.md`.

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
- `## Eng Review` — to be appended by `/plan-eng-review`
- `## DX Review` — to be appended by `/plan-devex-review`
- `## Decision Audit Trail` — to be appended by `/autoplan` Phase 4

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

<!-- /autoplan restore point: ~/.gstack/projects/llm-gateway/panda-main-autoplan-restore-*.md (none yet; first /autoplan pass in progress) -->

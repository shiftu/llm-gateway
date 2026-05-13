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

- `## CEO Review` — to be appended by `/plan-ceo-review`
- `## Eng Review` — to be appended by `/plan-eng-review`
- `## DX Review` — to be appended by `/plan-devex-review`
- `## Decision Audit Trail` — to be appended by `/autoplan` Phase 4

<!-- /autoplan restore point: ~/.gstack/projects/llm-gateway/panda-main-autoplan-restore-*.md (none yet; first /autoplan pass in progress) -->

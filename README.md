# llm-gateway

AI-native LLM proxy/gateway. Single static Go binary — no runtime deps, no web UI.
All resource management is done through your AI agent via MCP tools.

Routes requests from any OpenAI-compatible or Anthropic-compatible client to
DeepSeek, GLM (Zhipu), and other providers — with bearer auth, per-key quotas,
cost tracking, and audit logging built in.

## Quick start

```sh
# Download a release binary (linux/darwin/windows, amd64/arm64) or build from source:
make build          # → bin/llm-gateway

# 1. Generate a persistent bearer token and print your agent config
llm-gateway init

# 2. Start the gateway (default: 127.0.0.1:7421)
LLM_GATEWAY_PROVIDER_API_KEY=<your-deepseek-key> llm-gateway start

# 3. Wire your AI client (see next section)
llm-gateway mcp-config --client=1   # Claude Code
```

The banner printed on `start` shows your token and the exact curl headers to use.

### Docker

```sh
# Build and run with docker-compose
LLM_GATEWAY_PROVIDER_API_KEY=sk-... docker compose up -d

# Or build manually
docker build -t llm-gateway .
docker run -e LLM_GATEWAY_TOKEN=mytoken \
           -e LLM_GATEWAY_PROVIDER_API_KEY=sk-... \
           -p 7421:7421 llm-gateway
```

The Docker image is a scratch-based static binary (~10 MB). Data persists via
the `./data` volume mount (SQLite file + encryption key).

## Connecting your AI client

Run `llm-gateway mcp-config --client=<n>` and paste the output into your client config:

| # | Client | Config file |
|---|--------|-------------|
| 1 | Claude Code | `~/.claude.json` → `mcpServers` |
| 2 | Claude Desktop | `~/Library/Application Support/Claude/claude_desktop_config.json` |
| 3 | Cline (VS Code) | User settings JSON → `cline.mcpServers` |
| 4 | Cursor | `.cursor/mcp.json` |
| 5 | Generic stdio | any client that accepts a stdio MCP server block |

All five clients receive the same underlying config shape:

```json
{
  "mcpServers": {
    "llm-gateway": {
      "command": "llm-gateway",
      "args": ["mcp-serve"],
      "env": { "LLM_GATEWAY_TOKEN": "<your-token>" }
    }
  }
}
```

After pasting, restart your client. The gateway must be running (`llm-gateway start`).

## Sending requests

The gateway accepts both inbound API flavours on the same port:

**OpenAI-compatible**
```sh
curl http://127.0.0.1:7421/v1/chat/completions \
  -H "Authorization: Bearer ***" \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}'
```

**Anthropic-compatible**
```sh
curl http://127.0.0.1:7421/v1/messages \
  -H "x-api-key: <token>" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-v4-flash","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}'
```

## Provider setup

**DeepSeek (default)**
```sh
export LLM_GATEWAY_PROVIDER_KIND=deepseek
export LLM_GATEWAY_PROVIDER_API_KEY=sk-...
llm-gateway start
```

**GLM / Zhipu**
```sh
export LLM_GATEWAY_PROVIDER_KIND=glm
export LLM_GATEWAY_PROVIDER_NAME=glm-prod
export LLM_GATEWAY_PROVIDER_API_KEY=<zhipu-key>
export LLM_GATEWAY_PROVIDER_OPENAI_BASE_URL=https://open.bigmodel.cn/api/paas/v4
llm-gateway start
```

**Qwen / DashScope (v0.3)**
```sh
export LLM_GATEWAY_PROVIDER_KIND=qwen
export LLM_GATEWAY_PROVIDER_NAME=qwen-prod
export LLM_GATEWAY_PROVIDER_API_KEY=<dashscope-key>
llm-gateway start
```

**Moonshot / Kimi (v0.3)**
```sh
export LLM_GATEWAY_PROVIDER_KIND=moonshot
export LLM_GATEWAY_PROVIDER_NAME=moonshot-prod
export LLM_GATEWAY_PROVIDER_API_KEY=<moonshot-key>
llm-gateway start
```

For multiple providers, use the MCP `add_provider` tool from your agent chat after the
gateway is running.

## Multi-tenant teams

```sh
# Create a team and issue an inbound API key for it in one step:
llm-gateway init --team=<slug>
# Prints: lgw_<key>  — share this with the team; shown only once.
```

Teams get independent usage counters, quota controls, and routing rules — all
managed through MCP tools.

## Token / auth

| Source | Behaviour |
|--------|-----------|
| `LLM_GATEWAY_TOKEN` env | Used as-is on every start (persistent) |
| `~/.config/llm-gateway/token` | Written by `init`; loaded automatically |
| Neither set | Ephemeral token minted at start, printed to stdout; not persisted |

The legacy `LLM_GATEWAY_TOKEN` env var is permanent — it always works as a bootstrap
and lost-key recovery mechanism regardless of what is stored in the config file.

## Encryption at rest

All provider API keys and MCP bearer tokens stored in SQLite are encrypted with
AES-256-GCM. The encryption key is auto-generated on first run and stored in
`./data/encryption.key`.

**v0.3: Multi-key rotation (KEK).** Set `LLM_GATEWAY_KEK` to a 32-byte hex key
to unlock key rotation without data loss. Use MCP tools to rotate:

```sh
# Rotate to a new master key (old key stays usable until retired)
# Your agent does this via MCP:
# rotate_master_key → rekey_providers → retire_master_key
```

When `LLM_GATEWAY_KEK` is not set, the gateway falls back to the legacy
single-key mode (encryption.key file).

## Observability (v0.3)

**OpenTelemetry tracing (opt-in):**
```sh
export LLM_GATEWAY_OTEL_ENDPOINT=localhost:4317   # OTLP gRPC
llm-gateway start
```

When set, every inbound request gets an OTel span with `http.method` and `http.path`
attributes. Compatible with Jaeger, Tempo, Honeycomb, and any OTLP-capable backend.
When the env var is not set, tracing is a no-op (zero overhead).

**Existing endpoints** (unchanged):

| Endpoint | Auth | Description |
|----------|------|-------------|
| `GET /healthz` | None | Returns `{"status":"ok","version":"..."}` |
| `GET /metrics` | None | Prometheus-compatible metrics |

## Routing rules (v0.2)

Routing rules let you steer requests to specific providers or models based on
request properties. Rules are evaluated in priority order (lower number = higher
priority).

**Matching fields:**
- `model` — match the requested model name (default)
- `kind` — match the inbound protocol kind (`openai` or `anthropic`)

**Actions:**
- `provider=<name>` — route to a specific provider
- `model=<upstream-model>` — rewrite the model name

Example (via MCP tool `set_routing_rule`):
```
priority=10, match_field=model, match_value=expensive-model, action=provider=budget-provider
priority=20, match_field=kind, match_value=anthropic, action=provider=anthropic-fallback
```

## Cognitive routing (v0.3)

Cognitive routing scores all eligible providers and picks the best one per request,
with automatic fallback on errors or rate limits.

**Enable cognitive mode for an alias:**
```sh
# Via MCP (your agent does this):
# set_model_alias name="gpt4" provider="qwen-prod" model="qwen-plus" mode="cognitive"
```

**Per-team scoring weights:**
| Dimension | Default | Control via |
|-----------|---------|-------------|
| Cost | 40% | `set_routing_weights` |
| Latency | 30% | `set_routing_weights` |
| Quality | 20% | `set_routing_weights` |
| Health | 10% | `set_routing_weights` |

**Explain a routing decision:**
```sh
# Retrieve the route trace for any request via MCP:
# explain_route_trace request_id=<id>
```

**Fallback policies** — configure automatic fallback on HTTP 429 or 5xx:
```sh
# Via MCP: set_fallback_policy trigger=http_429 action=next_best max_chain_depth=3
```

## Budget limits (v0.3)

Set per-team USD spending caps that block requests when exceeded:

```sh
# Via MCP (your agent does this):
# set_budget team_id=<id> period=day usd_limit=10.0
# set_budget team_id=<id> period=month usd_limit=100.0
```

When the hard cap is hit, inbound requests return HTTP 429 with error type
`budget_exceeded`. Use `check_budget` to see current spend vs limit.

## Audit log (v0.2)

All mutating MCP operations are recorded in the `admin_audit` table with:

| Column | Description |
|--------|-------------|
| `ts` | Unix ms timestamp |
| `api_key_id` | Key that performed the action (FK, NULL if legacy token) |
| `team_id` | Team the key belongs to |
| `action` | Tool name (e.g. `add_provider`, `issue_api_key`) |
| `target_type` / `target_id` | What was affected |
| `detail` | JSON payload (best-effort) |
| `ip_address` | Source IP |

Audit entries are append-only. Use `prune_audit_log` (requires `mcp_admin`) to
delete entries older than N days.

## RPM persistence (v0.2, opt-in)

By default, per-minute RPM counters are in-memory (zero overhead, reset on
restart). Enable persistent RPM counting with:

```sh
export LLM_GATEWAY_PERSIST_RPM=1
llm-gateway start
```

When enabled, the counter is stored in `rpm_buckets` table and survives restarts.
Trade-off: one additional SQLite write per request per minute.

## API key scopes (v0.2)

| Scope | Rank | Access |
|-------|------|--------|
| `inbound` | 0 | LLM traffic only; no MCP tools |
| `mcp_auditor` | 1 | Read-only MCP tools: `list_*`, `get_*`, `ping`, `whoami`, `list_audit_logs` |
| `mcp_billing` | 2 | Billing/usage tools (reserved for future use) |
| `mcp_admin` | 3 | Full admin tools (write operations) |
| `mcp_super` | 4 | Team + key lifecycle (`add_team`, `issue_api_key`, `revoke_api_key`, `set_quota`) |

Use `whoami` to see your current scope and which tools are available/restricted.

## Error reference

Every error response has the same JSON shape:

```json
{
  "error": {
    "type":    "<error_type>",
    "message": "<human-readable description>",
    "fix":     "<actionable hint>"
  }
}
```

| HTTP | `type` | Cause | Fix |
|------|--------|-------|-----|
| 400 | `body_read_error` | Could not read request body | Resend; check client body handling |
| 400 | `invalid_json` | Body is not valid JSON | Send a JSON body matching OpenAI or Anthropic shape |
| 400 | `missing_model` | `model` field absent or empty | Set `model` to a registered alias or upstream model name |
| 401 | `missing_credentials` | No `Authorization` or `x-api-key` header | Send the gateway token as `Authorization: Bearer ***` or `x-api-key: <token>` |
| 401 | `invalid_credentials` | Token wrong, key revoked, or key not found | Verify token matches `LLM_GATEWAY_TOKEN`; revoked/expired keys must be reissued |
| 401 | `internal_error` | Auth DB lookup failed | Retry; check gateway logs if it persists |
| 404 | `no_route` | No alias matches and no default provider set | Register an alias via `set_model_alias` or set a default via `set_default_provider` |
| 429 | `quota_exceeded` | Per-minute / per-day / per-month limit hit | Check quota config or contact the gateway admin |
| 501 | `cross_protocol_not_supported` | Provider has no base URL for the inbound protocol | Register the provider with an `openai_base_url` or `anthropic_base_url` that matches the inbound protocol |
| 502 | `upstream_error` | Could not reach the upstream provider | Check provider `base_url` and network; tail `list_request_logs` for details |

## SDK configuration

Point any OpenAI-compatible or Anthropic-compatible SDK at the gateway:

```python
# OpenAI SDK
from openai import OpenAI
client = OpenAI(base_url="https://<host>/v1", api_key="lgw_<key>")

# Anthropic SDK
import anthropic
client = anthropic.Anthropic(base_url="https://<host>", api_key="lgw_<key>")
```

**Claude Code** — set in `~/.claude.json`:
```json
{ "apiBaseUrl": "https://<host>" }
```

## API key security

`issue_api_key` prints the plaintext token **once** at issuance — it is not stored and cannot be retrieved. If a key is lost, revoke it with `revoke_api_key` and issue a new one.

The legacy `LLM_GATEWAY_TOKEN` is a permanent bootstrap and lost-key recovery path; it always works regardless of the api_keys table state.

## MCP tools

The gateway exposes MCP tools via stdio (`llm-gateway mcp-serve`) and HTTP (`POST /mcp`).
Tools are gated by scope; `ping` is public.

**Provider & routing**

| Tool | Description | Min scope |
|------|-------------|-----------|
| `add_provider` | Register an upstream provider | `mcp_admin` |
| `remove_provider` | Delete a provider (cascades to its aliases) | `mcp_admin` |
| `list_providers` | List providers (API key redacted to last 4 chars) | `mcp_auditor` |
| `set_default_provider` | Set the global fallback provider | `mcp_admin` |
| `set_model_alias` | Map a model name to a provider + upstream model | `mcp_admin` |
| `delete_model_alias` | Remove a global alias | `mcp_admin` |
| `list_model_aliases` | List all global aliases | `mcp_auditor` |
| `set_model_cost` | Set per-1K-token pricing for a model | `mcp_admin` |
| `list_model_costs` | List all pricing entries | `mcp_auditor` |

**Multi-tenant**

| Tool | Description | Min scope |
|------|-------------|-----------|
| `add_team` | Create a team | `mcp_super` |
| `list_teams` | List all teams | `mcp_auditor` |
| `issue_api_key` | Issue an `lgw_` inbound key for a team | `mcp_super` |
| `revoke_api_key` | Revoke a key | `mcp_super` |
| `list_api_keys` | List a team's keys | `mcp_auditor` |
| `set_quota` | Set request / token / cost limits | `mcp_super` |
| `get_quota` | Read a quota entry | `mcp_auditor` |
| `list_quotas` | List all quota rules | `mcp_auditor` |
| `list_request_logs` | Query request logs (filterable by team or key) | `mcp_auditor` |

**Team-scoped model aliases**

| Tool | Description | Min scope |
|------|-------------|-----------|
| `set_team_model_alias` | Map an alias to a provider + model for a specific team | `mcp_admin` |
| `delete_team_model_alias` | Remove a team-scoped alias (global alias becomes visible again) | `mcp_admin` |
| `list_team_model_aliases` | List all team-scoped aliases for a team | `mcp_auditor` |

**Routing rules**

| Tool | Description | Min scope |
|------|-------------|-----------|
| `set_routing_rule` | Create or update a routing rule | `mcp_admin` |
| `remove_routing_rule` | Delete a routing rule | `mcp_admin` |
| `list_routing_rules` | List all routing rules by priority | `mcp_auditor` |

**Audit & identity**

| Tool | Description | Min scope |
|------|-------------|-----------|
| `list_audit_logs` | List admin audit log entries | `mcp_auditor` |
| `prune_audit_log` | Delete audit entries older than N days | `mcp_admin` |
| `whoami` | Show current identity, scope, and tool availability | `mcp_auditor` |
| `ping` | Health check — returns gateway version and UTC time | (public) |

**Cognitive routing (v0.3)**

| Tool | Description | Min scope |
|------|-------------|-----------|
| `get_provider_health` | SLO snapshot for all providers | `mcp_auditor` |
| `set_routing_weights` | Set per-team scoring weights (cost/latency/quality/health) | `mcp_admin` |
| `get_routing_weights` | Read current weights for a team | `mcp_auditor` |
| `set_fallback_policy` | Configure fallback trigger + chain | `mcp_admin` |
| `remove_fallback_policy` | Remove a fallback policy | `mcp_admin` |
| `list_fallback_policies` | List all fallback policies | `mcp_auditor` |
| `explain_route_trace` | Show the routing decision for a past request | `mcp_auditor` |
| `set_provider_capability` | Register a capability for a provider (e.g. `tool_use=true`) | `mcp_admin` |
| `list_provider_capabilities` | List capabilities for a provider | `mcp_auditor` |

**Budget management (v0.3)**

| Tool | Description | Min scope |
|------|-------------|-----------|
| `set_budget` | Set a per-team USD spending limit (day or month) | `mcp_admin` |
| `check_budget` | Check current spend vs budget for a team | `mcp_auditor` |
| `list_budgets` | List all configured budgets | `mcp_auditor` |

**Key rotation (v0.3)**

| Tool | Description | Min scope |
|------|-------------|-----------|
| `rotate_master_key` | Generate a new master key (requires `LLM_GATEWAY_KEK`) | `mcp_super` |
| `rekey_providers` | Re-encrypt all provider keys under the new master key | `mcp_super` |
| `retire_master_key` | Mark the old master key as retired | `mcp_super` |
| `list_master_keys` | List all master keys and their status | `mcp_super` |

**Audit chain (v0.3)**

| Tool | Description | Min scope |
|------|-------------|-----------|
| `verify_audit_chain` | Verify SHA-256 hash chain integrity of the audit log | `mcp_auditor` |

## MCP resources (v0.3)

The gateway exposes live data as MCP resources via the `lgw://` URI scheme.
Any MCP client can call `resources/read` to query:

| URI | Returns |
|-----|---------|
| `lgw://teams/{id}/usage` | Token usage rollup |
| `lgw://teams/{id}/cost` | USD cost rollup |
| `lgw://teams/{id}/quota` | Quota state |
| `lgw://teams/{id}/budget` | Budget state |
| `lgw://requests/{id}` | Full request log + route trace |
| `lgw://providers` | Provider list + health |
| `lgw://providers/{name}/health` | Single-provider SLO |
| `lgw://providers/{name}/capabilities` | Capability set |
| `lgw://audit?since={iso}` | Audit log slice |

## MCP prompts (v0.3)

Six preset operation prompts available via `prompts/get`:

| Prompt | Inputs | Purpose |
|--------|--------|---------|
| `investigate_traffic_spike` | team_id, since | Diagnose unusual traffic |
| `audit_who_used` | model, since | Compliance trail per model |
| `cost_review` | team_id, period | Cost breakdown + budget delta |
| `route_review` | team_id, since | Routing pattern analysis |
| `provider_health_check` | — | Current SLO snapshot |
| `add_provider_wizard` | kind | Guided provider setup |

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `LLM_GATEWAY_TOKEN` | — | Bearer token (bootstrap / recovery) |
| `LLM_GATEWAY_ADDR` | `127.0.0.1:7421` | Listen address |
| `LLM_GATEWAY_PROVIDER_API_KEY` | — | Seed a provider from env at startup |
| `LLM_GATEWAY_PROVIDER_NAME` | `default` | Provider name |
| `LLM_GATEWAY_PROVIDER_KIND` | `deepseek` | Provider kind (`deepseek`, `glm`, …) |
| `LLM_GATEWAY_PROVIDER_OPENAI_BASE_URL` | (kind default) | Override OpenAI-compat base URL |
| `LLM_GATEWAY_PROVIDER_ANTHROPIC_BASE_URL` | (kind default) | Override Anthropic-compat base URL |
| `LLM_GATEWAY_NO_STORE` | — | Set to `1` to run in stub mode (no SQLite) |
| `LLM_GATEWAY_PERSIST_RPM` | — | Set to `1` to persist RPM counters to SQLite (v0.2) |
| `LLM_GATEWAY_DB` | `./data/llm-gateway.db` | SQLite database path |
| `LLM_GATEWAY_ENCRYPTION_KEY` | `./data/encryption.key` | Encryption key file path |
| `LLM_GATEWAY_OTEL_ENDPOINT` | — | OTLP gRPC endpoint for tracing (e.g. `localhost:4317`); opt-in |
| `LLM_GATEWAY_KEK` | — | 32-byte hex Key Encryption Key for master key rotation (v0.3); opt-in |

## Building from source

```sh
go build -o bin/llm-gateway ./cmd/llm-gateway   # or: make build
make test                                         # go test ./...
make snapshot                                     # goreleaser snapshot (local)
```

Requires Go 1.22+. `CGO_ENABLED=0` — fully static binary, no libc dependency.

### Docker build

```sh
docker build -t llm-gateway .
# Multi-platform (if needed):
# docker buildx build --platform linux/amd64,linux/arm64 -t llm-gateway .
```

## Migration from v0.1 → v0.2

v0.2 is backward-compatible — no data migration is required.

| Area | v0.1 | v0.2 | Action needed |
|------|------|------|---------------|
| Provider API keys | Plaintext in SQLite | AES-256-GCM encrypted | None on upgrade; keys encrypted on next write. Run `init` once to generate `encryption.key` if it doesn't exist. |
| RPM counters | In-memory only | Optional SQLite persistence | Set `LLM_GATEWAY_PERSIST_RPM=1` if you want crash-safe RPM. Default unchanged (in-memory). |
| Audit log | Not available | Append-only `admin_audit` table | Automatic; no action. |
| Routing rules | Model aliases only | Priority-based rules with `match_field` | Opt-in; existing aliases work unchanged. |
| MCP scopes | `mcp_admin` / `mcp_super` | + `mcp_auditor` (read-only) | Issue auditor keys with `issue_api_key` → `scope=mcp_auditor`. Existing keys unchanged. |
| Health/metrics | None | `/healthz` + `/metrics` | Available immediately; no config. |
| Docker | Not available | `Dockerfile` + `docker-compose.yml` | `docker compose up -d` |

## Migration from v0.2 → v0.3

v0.3 is backward-compatible — the database auto-migrates on first start.

| Area | v0.2 | v0.3 | Action needed |
|------|------|------|---------------|
| Database schema | v7 | v10 | None — auto-migrated on startup |
| Provider list | DeepSeek, GLM | + Qwen (DashScope), Moonshot | Use `add_provider` MCP tool to register new providers |
| Encryption | Single master key (encryption.key file) | Multi-key rotation via KEK | None by default; set `LLM_GATEWAY_KEK` to unlock key rotation |
| Routing | Static aliases only | + cognitive mode (opt-in per alias) | Existing static aliases work unchanged |
| Fallback | None | Policy-based (opt-in) | Use `set_fallback_policy` to configure |
| Budget limits | None | Per-team USD caps (opt-in) | Use `set_budget` to configure |
| OTel tracing | None | OTLP gRPC (opt-in) | Set `LLM_GATEWAY_OTEL_ENDPOINT` |
| Audit log | Append-only | + SHA-256 hash chain | Automatic; verify with `verify_audit_chain` |
| MCP resources | None | `lgw://` URI scheme | Available immediately via `resources/list` |
| MCP prompts | None | 6 preset prompts | Available immediately via `prompts/list` |
| Helm chart | Not available | `deploy/helm/llm-gateway/` | `helm install llm-gateway ./deploy/helm/llm-gateway` |

**Upgrade steps:**
1. Replace the binary (`make build` or download a release)
2. Restart the gateway — SQLite migrates automatically to schema v10
3. Verify: `curl http://127.0.0.1:7421/healthz` returns `200 OK`
4. Optional: set `LLM_GATEWAY_KEK` and run `rotate_master_key` to enable key rotation
5. Optional: set `LLM_GATEWAY_OTEL_ENDPOINT` to enable distributed tracing

## Design notes

- **No web UI, ever.** All operations go through your AI agent via MCP. This is intentional.
- **Pass-through routing preferred.** If the inbound protocol matches the provider's native protocol, the request is forwarded as-is. IR translation is only used as a fallback.
- **Streaming supported** on all routes. No retry on stream failure in v0.1.
- **Quotas** are enforced in three windows: per-minute RPM (in-memory by default, optionally persistent), per-day, and per-month. The legacy bootstrap token bypasses quota checks.
- **Encryption at rest** protects provider API keys and MCP tokens stored in SQLite. The key file is auto-generated on first init.
- **Audit logging** records all mutating MCP operations. Entries are append-only and can be pruned by age.

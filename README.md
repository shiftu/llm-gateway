# llm-gateway

AI-native LLM proxy/gateway. Single static Go binary — no runtime deps, no web UI.
All resource management is done through your AI agent via MCP tools.

Routes requests from any OpenAI-compatible or Anthropic-compatible client to
DeepSeek, GLM (Zhipu), and other providers — with bearer auth, per-key quotas,
and cost tracking built in.

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
  -H "Authorization: Bearer <token>" \
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

For multiple providers, use the MCP `add_provider` tool from your agent chat after the
gateway is running.

## Multi-tenant teams

```sh
# Create a team and issue an inbound API key for it in one step:
llm-gateway init --team=<slug>
# Prints: lgw_<key>  — share this with the team; shown only once.
```

Teams get independent usage counters and quota controls, managed through MCP tools.

## Token / auth

| Source | Behaviour |
|--------|-----------|
| `LLM_GATEWAY_TOKEN` env | Used as-is on every start (persistent) |
| `~/.config/llm-gateway/token` | Written by `init`; loaded automatically |
| Neither set | Ephemeral token minted at start, printed to stdout; not persisted |

The legacy `LLM_GATEWAY_TOKEN` env var is permanent — it always works as a bootstrap
and lost-key recovery mechanism regardless of what is stored in the config file.

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
| 401 | `missing_credentials` | No `Authorization` or `x-api-key` header | Send the gateway token as `Authorization: Bearer <token>` or `x-api-key: <token>` |
| 401 | `invalid_credentials` | Token wrong, key revoked, or key not found | Verify token matches `LLM_GATEWAY_TOKEN`; revoked/expired keys must be reissued |
| 401 | `internal_error` | Auth DB lookup failed | Retry; check gateway logs if it persists |
| 404 | `no_route` | No alias matches and no default provider set | Register an alias via `set_model_alias` or set a default via `set_default_provider` |
| 429 | `quota_exceeded` | Per-minute / per-day / per-month limit hit | Check quota config or contact the gateway admin |
| 501 | `cross_protocol_not_supported` | Provider has no base URL for the inbound protocol | Register the provider with an `openai_base_url` or `anthropic_base_url` that matches the inbound protocol |
| 502 | `upstream_error` | Could not reach the upstream provider | Check provider `base_url` and network; tail `request_logs` for details |

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

The gateway exposes a stdio MCP server (`llm-gateway mcp-serve`). All tools require an `mcp_admin` key or the legacy token, except `ping` (no auth) and `add_team` (`mcp_super`).

**Provider & routing**

| Tool | Description |
|------|-------------|
| `add_provider` | Register an upstream provider |
| `remove_provider` | Delete a provider (cascades to its aliases) |
| `list_providers` | List providers (API key redacted to last 4 chars) |
| `set_default_provider` | Set the global fallback provider |
| `set_model_alias` | Map a model name to a provider + upstream model |
| `delete_model_alias` | Remove a global alias |
| `list_model_aliases` | List all global aliases |

**Multi-tenant**

| Tool | Description | Min scope |
|------|-------------|-----------|
| `add_team` | Create a team | `mcp_super` |
| `list_teams` | List all teams | `mcp_admin` |
| `issue_api_key` | Issue an `lgw_` inbound key for a team | `mcp_admin` |
| `revoke_api_key` | Revoke a key | `mcp_admin` |
| `list_api_keys` | List a team's keys | `mcp_admin` |
| `set_quota` | Set request / token / cost limits (minute / day / month) | `mcp_admin` |
| `get_quota` | Read a quota entry | `mcp_admin` |
| `list_quotas` | List all quota rules | `mcp_admin` |
| `list_request_logs` | Query request logs (filterable by team or key) | `mcp_admin` |

**Team-scoped model aliases**

| Tool | Description | Min scope |
|------|-------------|-----------|
| `set_team_model_alias` | Map an alias to a provider + model for a specific team (overrides global) | `mcp_admin` |
| `delete_team_model_alias` | Remove a team-scoped alias (global alias becomes visible again) | `mcp_admin` |
| `list_team_model_aliases` | List all team-scoped aliases for a team | `mcp_admin` |

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

## Building from source

```sh
go build -o bin/llm-gateway ./cmd/llm-gateway   # or: make build
make test                                         # go test ./...
make snapshot                                     # goreleaser snapshot (local)
```

Requires Go 1.22+. `CGO_ENABLED=0` — fully static binary, no libc dependency.

## Design notes

- **No web UI, ever.** All operations go through your AI agent via MCP. This is intentional.
- **Pass-through routing preferred.** If the inbound protocol matches the provider's native protocol, the request is forwarded as-is. IR translation is only used as a fallback.
- **Streaming supported** on all routes. No retry on stream failure in v0.1.
- **Quotas** are enforced in three windows: per-minute RPM (in-memory), per-day, and per-month. The legacy bootstrap token bypasses quota checks.

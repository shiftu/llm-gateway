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

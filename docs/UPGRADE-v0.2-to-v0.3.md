# Upgrading from v0.2 to v0.3

v0.3 "Cognitive Gateway" is fully backward-compatible with v0.2.
No data migration is required. The database schema auto-migrates on first start.

## Quick upgrade

```sh
# 1. Replace the binary
make build          # → bin/llm-gateway

# 2. Restart (schema migrates automatically)
llm-gateway start

# 3. Verify
curl http://127.0.0.1:7421/healthz
# → {"status":"ok","version":"0.3.0"}
```

## What's new in v0.3

### Cognitive routing

Provider scoring and automatic fallback. Enable per-alias:

```
# In your AI agent chat:
set_model_alias name="claude" provider="qwen-prod" model="qwen-plus" mode="cognitive"
set_fallback_policy trigger="http_429" action="next_best" max_chain_depth=3
```

All existing `mode="static"` aliases are unchanged — cognitive is fully opt-in.

### New providers: Qwen and Moonshot

```sh
# Add Qwen (DashScope):
# add_provider name="qwen-prod" kind="qwen" api_key="sk-..." openai_base_url="https://dashscope.aliyuncs.com/compatible-mode/v1"

# Add Moonshot (Kimi):
# add_provider name="moonshot-prod" kind="moonshot" api_key="sk-..." openai_base_url="https://api.moonshot.cn/v1"
```

### Per-team budget limits

```
# In your AI agent chat:
set_budget team_id=<id> period=day usd_limit=10.0 hard_cap_action=block
set_budget team_id=<id> period=month usd_limit=200.0 soft_threshold_pct=80
```

When the hard cap is hit, requests return HTTP 429 `budget_exceeded`.

### OpenTelemetry tracing (opt-in)

```sh
export LLM_GATEWAY_OTEL_ENDPOINT=localhost:4317
llm-gateway start
```

Compatible with Jaeger (`docker run -p 4317:4317 -p 16686:16686 jaegertracing/all-in-one`),
Grafana Tempo, Honeycomb, and any OTLP-capable backend.

### Multi-key rotation (opt-in)

Enable by setting a Key Encryption Key (32-byte hex):

```sh
export LLM_GATEWAY_KEK=$(openssl rand -hex 32)
llm-gateway start
```

Then rotate via MCP tools:
```
rotate_master_key label="2026-Q3"
rekey_providers
retire_master_key id=<old-key-id>
```

Old keys remain readable until retired. Zero data loss.

### MCP resources (`lgw://`)

Your AI agent can now query live gateway data directly:
```
resources/read uri="lgw://teams/tm_abc123/usage"
resources/read uri="lgw://providers"
resources/read uri="lgw://requests/42"
```

### MCP prompts (preset ops)

```
prompts/get name="provider_health_check"
prompts/get name="cost_review" arguments={"team_id":"tm_abc","period":"month"}
```

### Tamper-evident audit log

The audit log now maintains a SHA-256 hash chain. Verify integrity:
```
verify_audit_chain
```

### Helm chart

```sh
helm install llm-gateway ./deploy/helm/llm-gateway \
  --set env.LLM_GATEWAY_TOKEN=<token> \
  --set env.LLM_GATEWAY_PROVIDER_API_KEY=<key>
```

## Schema changes (v7 → v10)

Auto-applied on first start. No manual steps.

| Migration | Table added | Purpose |
|-----------|-------------|---------|
| v7 → v8 | `admin_audit` gains `prev_hash` column | Hash chain for tamper detection |
| v8 → v9 | `budgets` | Per-team USD spending limits |
| v9 → v10 | `master_keys` | Multi-key rotation registry |

Also added in v0.3 (earlier migrations):
- `routing_weights` — per-team cognitive scoring weights
- `fallback_policies` — trigger-based fallback chains
- `provider_capabilities` — capability registry for eligibility filtering
- `model_aliases.mode` column — static vs cognitive routing mode
- `request_logs.route_trace` column — per-request routing decision JSON

## Rollback

v0.3 does not support rollback to v0.2 (schema migrations are one-way).
If you need to roll back, restore from a v0.2 database backup.

## Environment variable additions

| Variable | Default | Description |
|----------|---------|-------------|
| `LLM_GATEWAY_OTEL_ENDPOINT` | — | OTLP gRPC tracing endpoint (opt-in) |
| `LLM_GATEWAY_KEK` | — | 32-byte hex Key Encryption Key for master key rotation (opt-in) |

All v0.2 env vars unchanged and still supported.

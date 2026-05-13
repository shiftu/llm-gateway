# llm-gateway — TODOS

Items deferred from `/autoplan` review (2026-05-13). Not in v0.1.0 scope but worth revisiting.

## Declined User Challenges (autoplan D4: option A)

These were subagent recommendations the user (intentionally) declined at the final approval gate. Documented here so future-you can re-evaluate when conditions change.

### UC-1 — Replace Go gateway with `LiteLLM proxy + 200-line MCP shim`

- **Subagent argument:** LiteLLM already handles GLM+DeepSeek+SSE+logging; the actual differentiator is the MCP control plane (~10 tools). Ship in 3–5 days instead of 8.
- **Why declined:** Conflicts with confirmed premises P1 (single-user, not multi-tenant), P5 (single Go binary, no Python runtime), P6 (AI-native end-to-end, not "AI bolted onto Python proxy").
- **Re-evaluate if:** v0.1 ships, but maintaining provider adapters becomes a time sink (>20% of v0.x work). At that point, consider a "LiteLLM as upstream" adapter (LiteLLM behind the gateway as one of many providers).

### UC-3 — Allow read-only `/ui` dashboard in v0.2

- **Subagent argument:** Reframe "no UI ever" to "no UI for writes." Inherently visual content (logs, usage, request detail) deserves a read-only dashboard.
- **Why declined:** Violates premise P2 (永远不要 web UI). User's deliberate aesthetic stance — UI is an artifact of pre-agent era.
- **Re-evaluate if:** ASCII-table rendering in agent chat genuinely fails for debug-of-debug scenarios (e.g., a 50-row request log with multi-line prompt excerpts becomes unreadable in chat). Even then, prefer "agent renders HTML and opens browser" over a long-running web service.

### UC-4 — HTTP/SSE MCP transport in v0.1 (not v0.2)

- **Subagent argument:** stdio = one-agent-per-gateway-process. Multi-agent value prop (Cline + Cursor + Continue all sharing one gateway) blocked from day 1. ~½ day extra in mcp-go.
- **Why declined:** v0.1 stays scoped to "fastest MVP that proves P6"; multi-agent is a v0.2 enhancement, not a v0.1 gate.
- **Re-evaluate if:** Multiple coding agents are actively used and re-launching the gateway subprocess from each agent becomes friction. Or if mcp-go's HTTP/SSE turns out to be trivial (<2h instead of ½ day).

### UC-5 — Reframe P4 as "Chinese-provider-first" positioning

- **Subagent argument:** Explicit Chinese-provider-first positioning is a defensible wedge vs LiteLLM's global-flat coverage; underserved audience.
- **Why declined:** v0.1 is a personal tool, not an OSS marketing exercise. Positioning premature; can be added later without architectural cost.
- **Re-evaluate if:** Project goes public on GitHub. Then add the positioning paragraph to README §0; zero code impact.

## Deferred Eng/DX Items (auto-decided defer)

### From `/plan-eng-review`

- **Router fuzz / property tests** (F-A-8) → v0.3. Single-user scope, diminishing returns at v0.
- **SSE load tests** (F-A-9) → v0.3. v0 is single-user, single-concurrent-request.

### v0.2 Roadmap Items (still in v0.2 per plan §3)

- HTTP/SSE MCP transport (declined-UC-4 keeps it in v0.2)
- Docker container (scratch base, ~10MB)
- `set_routing_rule` MCP tool (advanced routing semantics)
- Cost tracking ($ multiplication from token counts)

## Open Questions Still Open

From plan §6, items not yet resolved during autoplan:

- **Q1:** Router strategy v0 — confirmed static mapping; auto-degrade defer to v0.3.
- **Q2:** Inbound auth — confirmed single bearer; multi-token defer to v0.2.
- **Q3:** Streaming retry — confirmed v0 no retry; document in README.
- **Q4:** mcp-go selection — needs 1-day spike per F-16 (was 1h in original plan).
- **Q5:** Prompt logging privacy — confirmed default token+200-char-excerpt, `LOG_FULL_PROMPT=1` for full.
- **Q6:** Model alias — MVP simple table; rich routing defer to v0.2.
- **Q7:** GLM auth (Bearer vs JWT) — confirmed needs verification against Zhipu 2026-05 docs as part of F-15 spike.
- **Q8:** MCP transport — stdio v0.1 confirmed; HTTP/SSE v0.2 (UC-4 keeps it deferred).

# Spike: DeepSeek dual-protocol smoke tests

**Date:** 2026-05-13
**Goal:** Verify DeepSeek v4 endpoint behavior on both OpenAI and Anthropic inbound shapes; pin down IR design.
**Scope:** 4 smoke tests against real `api.deepseek.com` with real key.
**Outcome:** 7 architectural findings → Plan Revision 2.

## Endpoints & auth

| Surface | URL | Header |
|---|---|---|
| OpenAI-compat | `https://api.deepseek.com/v1/chat/completions` | `Authorization: Bearer <key>` |
| Anthropic-compat | `https://api.deepseek.com/anthropic/v1/messages` | `x-api-key: <key>` + `anthropic-version: 2023-06-01` |

## Findings

### F1. DeepSeek v4 is a reasoning model

Every response contains hidden chain-of-thought:

- OpenAI shape: `choices[0].message.reasoning_content` (string, separate from `content`)
- Anthropic shape: `content[]` array with `{type:"thinking", thinking:"...", signature:"..."}` block ordered before the `{type:"text", text:"..."}` block
- Streaming OpenAI: `delta.reasoning_content` accumulates token by token in same delta object as eventual `delta.content`
- Streaming Anthropic: explicit `content_block_start type=thinking` → multiple `content_block_delta delta.type=thinking_delta` → `content_block_stop` → then `content_block_start type=text` etc.

### F2. Same data, two field names — DeepSeek translates server-side

DeepSeek OpenAI `reasoning_content` ↔ DeepSeek Anthropic `thinking` block ARE the same semantic content, just different wire formats. DeepSeek's `/anthropic/` endpoint is a translation layer they maintain.

**Implication:** When gateway inbound protocol matches a provider's available base_url, pass-through is the cheapest path. Don't IR-translate when a native pipe exists.

### F3. Stream semantics diverge non-trivially

| Aspect | OpenAI flat | Anthropic events |
|---|---|---|
| Frame | `data: {chunk}\n\n` | `event: <name>\ndata: {chunk}\n\n` |
| Token unit | `choices[0].delta.{content,reasoning_content}` | `delta.type=thinking_delta/text_delta` inside indexed content_block |
| Block boundaries | implicit (when `content` first appears) | explicit `content_block_start/stop` |
| Heartbeat | none | `event: ping` periodically |
| Termination | `data: [DONE]` sentinel | `event: message_stop` |

IR.StreamChunk fields needed:
```go
type StreamChunk struct {
    Type        string // "message_start" | "content_block_start" | "content_block_delta" | "content_block_stop" | "message_delta" | "message_stop" | "ping" | "error"
    BlockIndex  int
    BlockType   string // "text" | "thinking" | "tool_use"
    DeltaText   string // for text_delta / thinking_delta
    Usage       *Usage // present on message_start (input) and message_stop (output)
    StopReason  string // "end_turn" | "max_tokens" | "stop_sequence" | "tool_use"
}
```

### F4. Auth header differs by outbound protocol, not by provider

DeepSeek OpenAI endpoint expects `Authorization: Bearer <key>`. DeepSeek Anthropic endpoint expects `x-api-key: <key>` + `anthropic-version: 2023-06-01`. Same provider, same key, different headers.

**Implication:** Header construction is a function of `(outbound_protocol, api_key, anthropic_version_if_applicable)`, not a per-provider constant. Provider adapter must accept "use Anthropic shape" vs "use OpenAI shape" as a parameter.

### F5. Performance gap: 1.74s vs 3.90s (2.24× slower on Anthropic endpoint)

Blocking 200-token completion:
- OpenAI: HTTP 200 in 1.74s
- Anthropic: HTTP 200 in 3.90s
- Likely because DeepSeek's Anthropic endpoint does protocol translation server-side AND the test produced more reasoning tokens

**Implication:** Pass-through (same-protocol) is meaningfully faster than cross-translation. Justifies the routing-strategy logic in F2 implication.

### F6. Usage field schemas don't overlap

| Field | OpenAI | Anthropic |
|---|---|---|
| Input tokens | `prompt_tokens` | `input_tokens` |
| Output tokens | `completion_tokens` | `output_tokens` |
| Reasoning tokens | `completion_tokens_details.reasoning_tokens` | (none — count inside output_tokens) |
| Cache hit | `prompt_cache_hit_tokens` (DeepSeek extension) | `cache_read_input_tokens` |
| Cache miss | `prompt_cache_miss_tokens` (DeepSeek extension) | `cache_creation_input_tokens` |
| Total | `total_tokens` | derive |

IR.Usage takes superset:
```go
type Usage struct {
    InputTokens          int
    OutputTokens         int
    ReasoningTokens      int // 0 if not a reasoning model
    CacheReadTokens      int
    CacheCreationTokens  int
}
```

OpenAI serializer derives `total_tokens = InputTokens + OutputTokens`; Anthropic serializer omits it. Both serializers omit fields that are 0 to match canonical examples.

### F7. Anthropic stream emits `ping` heartbeats; OpenAI doesn't

The gateway must:
- When inbound is Anthropic and upstream is Anthropic: forward `ping` events transparently.
- When inbound is Anthropic but IR-translated from OpenAI upstream: synthesize `ping` every ~15s during long streams so client doesn't time out.
- When inbound is OpenAI: drop `ping` events (no OpenAI equivalent).

## Architecture conclusions

### Routing strategy (replaces R1's "always go through IR")

```
                                    ┌─── inbound protocol ───┐
                                    │                        │
                                    ▼                        ▼
                              OpenAI request          Anthropic request
                                    │                        │
                              ┌─────┴─────┐            ┌─────┴──────┐
                              │           │            │            │
                       provider has   provider     provider has   provider
                       openai_url     only         anthropic_url  only
                                      anthropic_url               openai_url
                              │           │            │            │
                              ▼           ▼            ▼            ▼
                         PASS-THROUGH  IR-translate PASS-THROUGH  IR-translate
                                       to Anthropic              to OpenAI
                                       (heavier)                 (heavier)
```

### Provider config schema (replaces R1's `base_url` single field)

```sql
CREATE TABLE providers (
  name              TEXT PRIMARY KEY,
  kind              TEXT NOT NULL,          -- "deepseek" | "glm" | "openai" | "anthropic"
  openai_base_url   TEXT,                   -- nullable
  anthropic_base_url TEXT,                  -- nullable; must set at least one
  api_key           TEXT NOT NULL,          -- encrypted at rest per A-3
  anthropic_version TEXT DEFAULT '2023-06-01',
  is_default        INTEGER NOT NULL DEFAULT 0,
  created_at        INTEGER NOT NULL,
  CHECK (openai_base_url IS NOT NULL OR anthropic_base_url IS NOT NULL)
);
```

### IR is still needed but only fires on cross-protocol

v0.1 split:
- ~80% of calls: pass-through (zero IR involvement, single TCP hop, lowest latency)
- ~20% of calls: cross-protocol → IR translate → upstream
  - Specifically: Anthropic inbound (Claude Code) + GLM (if GLM only has OpenAI-compat) — needs verification with GLM credentials

## Open questions raised by spike

- **Q13:** Does GLM/Zhipu also offer a native Anthropic endpoint? Need GLM credentials to verify. If yes, gateway never needs IR translation for the v0.1 provider set. If no, the IR layer carries Claude-Code-to-GLM traffic.
- **Q14:** When inbound is OpenAI (no reasoning text expected) but upstream is DeepSeek v4 (always returns reasoning), how does gateway present this? Three options:
  - **a.** Pass `reasoning_content` through verbatim — DeepSeek already does this for OpenAI inbound; matches OpenAI o1's `reasoning_content` extension space.
  - **b.** Drop `reasoning_content` — strictly conform to OpenAI standard. Loses information.
  - **c.** Per-request opt-in: `extra_body: {include_reasoning: false}`. Most flexible, default to a.
- **Q15:** How does signature field in Anthropic thinking blocks work? Need to inspect across multiple requests to see if it's just request id duplication or actually a verification signature.

## Plan changes required (Plan Revision 2)

To be written separately:

- §2 Storage schema: `base_url` → `openai_base_url` + `anthropic_base_url`
- §2 Routing: add pass-through fast path; IR-translate is fallback
- §2 IR types: ContentBlock includes `thinking` variant with `signature`; StreamChunk has `Type/BlockIndex/BlockType/DeltaText`; Usage has 5 fields
- Task 1.5 (IR types): expanded scope; ~1 day (was 0.5)
- Task 2/3 (provider adapters): now each adapter exposes `OpenAIComplete()`, `OpenAIStream()`, `AnthropicComplete()`, `AnthropicStream()` methods. Pass-through skips IR entirely.
- Task 6 / 6.5: routing logic — try pass-through first, fall back to IR.
- MCP `add_provider` tool: now takes `openai_base_url` + `anthropic_base_url` (at least one required).
- Effort: +0.5 day on top of R1 → total ~11.5 工程日

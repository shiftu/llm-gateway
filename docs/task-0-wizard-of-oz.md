# Task 0 — Wizard-of-Oz: Validate P6 (AI-Native Thesis)

**Goal:** Before writing any Go, prove that operating an LLM gateway through MCP tools is genuinely better than editing a YAML config + `sqlite3` + `tail -f log.jsonl`. If MCP doesn't clearly win, the project's foundational premise (P6) is wrong and we should re-brainstorm before burning 8 工程日.

**Budget:** 30 minutes total. 15 min for the MCP arm, 15 min for the YAML arm.

**Outcome:** A single line at the bottom of this file: `VERDICT: MCP-WINS | TIE | YAML-WINS`.

---

## Setup — Cline Bootstrap Prompt

Open Cline (or Claude Code in a different project, or any coding agent you regularly use). Paste this prompt verbatim:

```
You're roleplaying as a coding agent connected to a brand-new LLM gateway
called llm-gateway. The gateway exposes 10 MCP tools listed below. None of
these tools really exist — when I ask you to use them, fake the JSON-RPC
call: print what tool call you'd make, then print a plausible JSON response
as if the gateway answered. Don't apologize that the tools don't exist; just
play along. We're stress-testing whether MCP-driven ops feel natural.

TOOLS:
1. list_providers() → {providers: [{name, kind, base_url, is_default}]}
2. add_provider({name, kind: "glm"|"deepseek", base_url, api_key}) → {ok, message, fix?}
3. remove_provider({name}) → {ok}
4. set_default_provider({name}) → {ok}
5. list_model_aliases() → {aliases: [{alias, provider_name, upstream_model}]}
6. set_model_alias({alias, provider, upstream_model}) → {ok}
7. remove_model_alias({alias}) → {ok}
8. get_usage({window: "1h"|"24h"|"7d"}) → {by_provider: {name: {prompt_tokens, completion_tokens, requests, errors}}}
9. tail_logs({n: int}) → {logs: [{ts, client_model, provider_name, prompt_tokens, status, latency_ms, prompt_excerpt}]}
10. get_request({id: int}) → full log row + prompt_full

The 5 scenarios I'll throw at you are below. Respond to each as if you were
the agent. Be terse. No filler. After each scenario, log the elapsed seconds
in a running tally at the bottom.
```

Then paste the 5 scenarios from §A below, one by one. Time each end-to-end (you typing → Cline finishing the "MCP call + fake response + plain-language summary").

---

## §A. MCP Arm — 5 Scenarios

### S1. Register GLM provider as default

> *"Add my GLM provider. Token is `glm-xxxxx`. Base URL is `https://open.bigmodel.cn/api/paas/v4`. Make it the default."*

Expected agent trace:
```
add_provider({name: "glm-prod", kind: "glm", base_url: "...", api_key: "glm-xxxxx"})
→ {ok: true, message: "Registered glm-prod"}
set_default_provider({name: "glm-prod"})
→ {ok: true}
"Done — glm-prod is registered and default."
```

### S2. Add a fast/smart alias pair

> *"I want `fast` to mean DeepSeek's `deepseek-chat` and `smart` to mean GLM's `glm-4-plus`. Set both."*

Expected agent trace:
```
set_model_alias({alias: "fast", provider: "deepseek", upstream_model: "deepseek-chat"})
set_model_alias({alias: "smart", provider: "glm-prod", upstream_model: "glm-4-plus"})
"Done. 'fast' → deepseek-chat, 'smart' → glm-4-plus."
```

(Implicit: deepseek provider must already be registered. If agent asks, fake-add deepseek first.)

### S3. "How much did I burn yesterday?"

> *"How much GLM did I use in the last 24 hours?"*

Expected agent trace:
```
get_usage({window: "24h"})
→ {by_provider: {glm-prod: {prompt_tokens: 142000, completion_tokens: 18000, requests: 47, errors: 1}, ...}}
"24h: GLM-prod used 142k prompt + 18k completion across 47 requests (1 error)."
```

### S4. "Why did my last request fail?"

> *"My last chat completion failed. What happened?"*

Expected agent trace:
```
tail_logs({n: 5})
→ {logs: [{id: 412, ts: "...", status: "upstream_error", error_msg: "401 invalid_api_key", provider_name: "glm-prod", ...}]}
get_request({id: 412})
→ {... prompt_full: "...", provider_name: "glm-prod"}
"Request 412 failed: glm-prod returned 401 invalid_api_key. Your token likely expired or rotated."
```

### S5. Rotate a leaked key

> *"I accidentally pushed my old GLM token. Rotate it to `glm-yyyyy` and make sure everything still works."*

Expected agent trace:
```
remove_provider({name: "glm-prod"})
add_provider({name: "glm-prod", kind: "glm", base_url: "...", api_key: "glm-yyyyy"})
set_default_provider({name: "glm-prod"})
get_usage({window: "1h"})
→ ...
"Rotated glm-prod to the new key. Aliases auto-rebound via name. Last hour shows X requests, none failing post-rotation."
```

**Tally after S5:**
- S1 elapsed: ___ sec
- S2 elapsed: ___ sec
- S3 elapsed: ___ sec
- S4 elapsed: ___ sec
- S5 elapsed: ___ sec
- **MCP TOTAL: ___ sec**

Also note: how many times did you have to clarify what the tool did? How many times did Cline pick the right tool zero-shot?

---

## §B. YAML/CLI Arm — Same 5 Scenarios

Imagine the gateway is now config-file based with a SQLite log. Schema:

```yaml
# ~/.config/llm-gateway/config.yaml
providers:
  glm-prod:
    kind: glm
    base_url: https://open.bigmodel.cn/api/paas/v4
    api_key: glm-xxxxx
default_provider: glm-prod
aliases:
  fast: {provider: deepseek, upstream_model: deepseek-chat}
  smart: {provider: glm-prod, upstream_model: glm-4-plus}
```

```sql
-- ~/.config/llm-gateway/state.db
CREATE TABLE request_logs (id, ts, client_model, provider_name, prompt_tokens, completion_tokens, latency_ms, status, error_msg, prompt_excerpt);
```

For each scenario, write out what you'd actually type at the shell (editor invocations, sqlite3 queries, validation steps). Time it from "user wants to do X" to "verified done."

### S1' YAML

```bash
vim ~/.config/llm-gateway/config.yaml
# edit providers section, save
llm-gateway reload  # or restart
```

### S2' YAML

```bash
vim ~/.config/llm-gateway/config.yaml
# edit aliases section, save
llm-gateway reload
```

### S3' SQLite

```bash
sqlite3 ~/.config/llm-gateway/state.db \
  "SELECT provider_name, SUM(prompt_tokens), SUM(completion_tokens), COUNT(*), SUM(status='upstream_error') FROM request_logs WHERE ts > strftime('%s','now','-24 hours')*1000 GROUP BY provider_name;"
```

### S4' SQLite + log

```bash
sqlite3 ~/.config/llm-gateway/state.db \
  "SELECT id, ts, status, error_msg, prompt_excerpt FROM request_logs ORDER BY ts DESC LIMIT 5;"
sqlite3 ~/.config/llm-gateway/state.db \
  "SELECT * FROM request_logs WHERE id = 412;"
```

### S5' YAML + verify

```bash
vim ~/.config/llm-gateway/config.yaml
# change api_key, save
llm-gateway reload
# verify
sqlite3 ~/.config/llm-gateway/state.db \
  "SELECT status, COUNT(*) FROM request_logs WHERE ts > strftime('%s','now','-1 hour')*1000 GROUP BY status;"
```

**Tally:**
- S1' elapsed: ___ sec
- S2' elapsed: ___ sec
- S3' elapsed: ___ sec
- S4' elapsed: ___ sec
- S5' elapsed: ___ sec
- **YAML TOTAL: ___ sec**

Also note: how many context-switches between editor / shell / docs?

---

## §C. Scoring Rubric

MCP arm wins **decisively** if any 2 of these are true:

1. **Time:** MCP_TOTAL < 0.7 × YAML_TOTAL (≥30% faster)
2. **Context cost:** MCP arm stayed inside Cline; YAML arm needed editor + shell + sqlite3 + docs lookup
3. **Zero-shot correctness:** Cline picked the right MCP tool first try in ≥4 of 5 scenarios
4. **Recovery delight:** S5 (key rotation) felt qualitatively different — chat-driven rotation including verification in one breath vs editor + reload + manual verify
5. **Inverse-skill demand:** YAML arm required remembering SQL/YAML schema; MCP arm just required natural language

MCP arm wins **marginally** if exactly 1 is true.

MCP arm **loses** if 0 are true. → Re-brainstorm: maybe a TUI + YAML config is the right shape, or maybe LiteLLM-shim (UC-1) was the right call after all.

## §D. Failure Mode Detection

If during the MCP arm Cline does any of these, MCP isn't actually winning:
- Asks you to "show the current config file" — means it wants to read state directly, not via tools
- Picks the wrong tool repeatedly (e.g., uses `tail_logs` when you wanted `get_usage`)
- Returns a tool call but then asks you to verify it manually — means MCP isn't load-bearing
- Refuses to bundle multi-step ops (S5) into one breath — means agent needs a "macro" tool, not 10 atoms

Note these incidents in the verdict line below.

---

## §E. Verdict (fill in after running both arms)

**Date run:**
**MCP_TOTAL:**  sec
**YAML_TOTAL:**  sec
**Time win:** Yes / No
**Context-cost win:** Yes / No
**Zero-shot correctness:** ___ / 5
**Recovery delight (S5):** Yes / No
**Inverse-skill win:** Yes / No

**Failure modes observed:**
-

**Notes from running:**
-

---

**VERDICT:** _(circle one)_ MCP-WINS / TIE / YAML-WINS

**Next action based on verdict:**
- MCP-WINS → proceed to F-15 GLM live spike (2h) per plan §4 pre-Task 1
- TIE → run one more pass with the friction points fixed in §D, then decide
- YAML-WINS → STOP. Re-open D2/D3 brainstorm. Likely: the right shape is a CLI tool with YAML config + read-only sqlite tail, with MCP as v0.2 add-on rather than v0.1 foundation. Or UC-1 (LiteLLM shim) becomes attractive again.

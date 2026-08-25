# /v1/responses —— codex 兼容层

> **这份契约不是照着文档写的，是抓真流量抓出来的。**
> 请求形状来自 codex-cli 0.148.0 实际发出的报文；响应事件序列是拿真 codex
> 当验收器一轮轮试出来的（它接受了就是对的，它报 `stream closed before
> response.completed` 就是错的）。每一条都有对应的实验。

## 为什么需要它

codex **0.137.0 起删掉了 `wire_api = "chat"`**，只认 Responses API：

```
Error loading config.toml: `wire_api = "chat"` is no longer supported.
How to fix: set `wire_api = "responses"` in your provider config.
```

实测一路回退到 0.138.0 都已经删除。网关只有 `/v1/chat/completions`，
所以 codex 和网关之间**没有能说的话** —— 它会绕开 `OPENAI_BASE_URL`
直连 `wss://api.openai.com/v1/responses` 然后 401。

结论：要么网关说 Responses，要么 codex 用不了。这个文件描述前者。

## 形态：一条翻译链路，不是一个新路由

现有的 `routeAndForward` 是**逐字节转发**：protocol 只决定用哪个 upstream
base_url，请求体原样透传。Responses 不一样 ——
入站 Responses → 翻成 Chat Completions → 转发 → 响应翻回 Responses。

复用 `internal/ir` 的中间表示，加第三套 `*FromResponses` / `*ToResponses`。

## 入站请求（codex → 网关）

`POST /v1/responses`，`Accept: text/event-stream`，`stream: true`。
**codex 只走流式**，非流式路径对它没有意义。

顶层字段（实测抓到的全集）：

| 字段 | 处理 |
|---|---|
| `model` | 照走现有 router.Resolve |
| `instructions` | 字符串 → Chat 的 `system` 消息，放在 messages 最前 |
| `input[]` | 见下表 |
| `tools[]` | `{type:"function", name, description, strict, parameters}` → Chat 的 `{type:"function", function:{name, description, parameters}}`。**注意 Responses 是扁平的，Chat 是嵌套的** |
| `tool_choice` | 原样 |
| `parallel_tool_calls` | 原样 |
| `stream` | 恒为 true |
| `store` / `prompt_cache_key` / `reasoning` / `include` / `client_metadata` | 丢弃（上游 Chat 端点不认） |

`input[]` 的三种条目：

| Responses 条目 | 翻成 Chat |
|---|---|
| `{type:"message", role, content:[{type:"input_text"\|"output_text", text}]}` | `{role, content}`；**`role:"developer"` 要映射成 `system`** |
| `{type:"function_call", call_id, name, arguments}` | `{role:"assistant", tool_calls:[{id:call_id, type:"function", function:{name, arguments}}]}` |
| `{type:"function_call_output", call_id, output}` | `{role:"tool", tool_call_id:call_id, content:output}` |

`arguments` 在两边都是**JSON 字符串**，不是对象 —— 不要解了再编。

### 已知坑（cc-switch 踩过，直接拿来当测试用例）

- **`messages: null`**（cc-switch issue #2806）：`input` 为空或全被过滤掉时，
  不能往上游发 `messages: null`，要发 `[]` 或直接 400。
- **`view_image` 的 base64 被当成 tool text 重放**（issue #5663）：
  图片类 content part 不能无脑塞进文本字段。

## 出站响应（网关 → codex）

SSE。每个事件都要带 `sequence_number`（从 0 递增）。

### 纯文本回答：9 个事件

```
response.created            {response:{id,object:"response",model,status:"in_progress",output:[]}}
response.in_progress        同上
response.output_item.added  {output_index:0, item:{type:"message",id,status:"in_progress",role:"assistant",content:[]}}
response.content_part.added {item_id, output_index:0, content_index:0, part:{type:"output_text",text:"",annotations:[]}}
response.output_text.delta  {item_id, output_index:0, content_index:0, delta:"<增量>"}      ← 每个 chunk 一条
response.output_text.done   {item_id, output_index:0, content_index:0, text:"<全文>"}
response.content_part.done  {item_id, output_index:0, content_index:0, part:{...,text:"<全文>"}}
response.output_item.done   {output_index:0, item:{...status:"completed", content:[{type:"output_text",text,annotations:[]}]}}
response.completed          {response:{...status:"completed", output:[item], usage:{input_tokens,output_tokens,total_tokens}}}
```

**`response.completed` 不发 = codex 报 `stream disconnected before completion:
stream closed before response.completed` 并重试 5 次。**

### 工具调用：5 个事件

```
response.created
response.output_item.added            {output_index:0, item:{type:"function_call",id,call_id,name,arguments:"",status:"in_progress"}}
response.function_call_arguments.delta {item_id, output_index:0, delta:"<arguments 增量>"}
response.function_call_arguments.done  {item_id, output_index:0, arguments:"<完整 JSON 字符串>"}
response.output_item.done             {output_index:0, item:{type:"function_call",id,call_id,name,arguments,status:"completed"}}
response.completed
```

`call_id` 必须和后续 `function_call_output` 里的对上 —— 上游 Chat 的
`tool_calls[].id` 直接拿来当 `call_id`。

### usage

Chat 的 `prompt_tokens`/`completion_tokens` → Responses 的
`input_tokens`/`output_tokens`/`total_tokens`，放在 `response.completed` 里。

**实现时的修正**：没有给 `usage.go` 加 `protocolResponses` 分支。因为
`dispatchResponses` 转发到上游走的就是 `protocolOpenAI`（翻译只发生在
入站和出站两端，上游收发的始终是 Chat 报文），所以直接对上游的原始 Chat
SSE 字节调用现成的 `applySSEChunkUsage(..., protocolOpenAI)` 就拿到了计费
数字——不需要再对着已经翻成 Responses 形状的输出字节重新解析一遍
`response.completed.usage`。少一条分支，同一份逻辑两处复用。

## 验证方法

不要用 curl 自己造报文验 —— 那只能证明"我写的和我以为的一致"。
**用真 codex 当验收器**：

```toml
# ~/.codex/config.toml
model = "charaboard/deepseek-v4-flash"
model_provider = "gw"

[model_providers.gw]
name = "internal gateway"
base_url = "http://<网关>/v1"
env_key = "OPENAI_API_KEY"
wire_api = "responses"
```

```bash
codex exec --skip-git-repo-check "say PONG"          # 纯文本链路
codex exec --skip-git-repo-check -s read-only "run echo"   # 工具调用回路
```

第二条会走完整的两轮：模型发 function_call → codex 执行 → 回传
`function_call_output` → 模型收尾。**两轮都过才算通。**

## 其它实测记录

- codex 会**先试 WebSocket**（`wss://.../v1/responses`），失败 5 次后才回落到
  HTTPS+SSE。回落是好用的，但每次会话前面有一段可见的重试噪音。
  要么网关也支持 WS 升级，要么接受这段噪音。
- codex 对未知模型名会警告
  `Model metadata for '<model>' not found. Defaulting to fallback metadata`，
  不影响功能。

# TypeSafe System One 接入

实现参考 ominigate 提交 `dd48d1ca23f960412a1f4612a48da5f4f5939570` 中的
`docs/typesafe/protocol-reference.md` 和 TypeSafe adapter。
网关入口为 `POST /v1/systemone`，使用现有网关 token 或团队 `lgw_` API key 鉴权。

## 为已有网关添加 TypeSafe

构建并重启使用新版本的网关：`make build`。启动时数据库自动从 v12 升到 v13，
保留 provider、API key 存储值、团队关联、别名和路由规则，新增 `typesafe_base_url`。

使用有 `mcp_admin` 或 `mcp_super` 权限的 MCP 连接调用 `add_provider`：

```json
{
  "name": "typesafe",
  "kind": "typesafe",
  "api_key": "<TypeSafe API key>",
  "typesafe_base_url": "https://api.typesafe.ai/v1",
  "is_default": false
}
```

然后调用 `set_model_alias`：

```json
{
  "alias": "evaluate",
  "provider_name": "typesafe",
  "upstream_model": "jev-latest",
  "mode": "static"
}
```

`list_providers` 会显示 `typesafe_base_url` 并掩码 API key。
已有聊天请求继续使用原来的默认 provider；评估请求显式使用 `evaluate`。
也可以通过 MCP `add_provider_wizard` prompt，传入 `kind=typesafe` 获取配置指引。

## 发起评估

`LLM_GATEWAY_TOKEN` 是网关凭据，TypeSafe API key 由网关在转发时替换到上游请求头。

```sh
curl --fail-with-body http://127.0.0.1:7421/v1/systemone \
  -H "Authorization: Bearer $LLM_GATEWAY_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "evaluate",
    "state": {"ticket": "账户被重复扣费，请退还多扣的钱。", "locale": "zh-CN"},
    "questions": {
      "refund_requested": {
        "type": "noul",
        "instructions": "用户是否明确要求退款？"
      },
      "owner": {
        "type": "choice",
        "instructions": "应该由哪个团队处理？",
        "criteria": {"billing": "收费、发票或退款", "technical": "产品故障"}
      },
      "urgency": {
        "type": "score",
        "instructions": "按紧急程度评估。",
        "criteria": ["常规", "紧急", "严重"]
      }
    }
  }'
```

响应保留上游实际 `model`、完整 `answers`、概率分布、`confidence`、`legend`、
小数 `score` 和扩展字段。别名只改写请求的 `model`，不改写响应的实际版本。
`usage.input_tokens` / `usage.output_tokens` 进入请求日志和团队用量统计。
大整数等不透明 JSON 数据在别名改写时保留精度。

网关本地校验：

- `state` 必须是字符串、对象或数组；`questions` 必须是非空对象。
- 每个问题必须提供 `instructions`，类型为字符串、对象或数组。
- `noul` 的可选 `criteria` 仅接受 `true` / `false` 键；`choice` 接受 1–255 个选项；`score` 接受 2–10 级量表。
- `stream: true`、非布尔 stream、聊天/工具/采样字段及其他未定义顶层字段返回 400。`stream: false` 可接受，但转发前移除。
- System One 与 Chat Completions、Messages、Responses 不互转；目标缺少对应协议地址时返回 501。

上游 401、422、429、529 等错误保留状态码、响应体和重试提示头。
401/422 不触发故障切换；429/529 可使用现有 `http_429` / `http_5xx` 策略切换到配置了
`typesafe_base_url` 的 provider。没有策略时直接返回上游错误，不自动重试同一上游。
成功响应缺少模型、answers 对象或有效 token 用量时返回 502，不计为成功用量。

成本使用 MCP `set_model_cost` 中配置的 provider/model 价格；请按实际账户价格设置。
未配置价格时成本统计为 0。OpenRouter 的 `usage.cost` 会透传，但不直接替代本地计费配置。

## OpenRouter 或自定义兼容端点

可以为任意 provider kind 设置 `typesafe_base_url`；协议由地址字段决定。
OpenRouter 使用 `https://openrouter.ai/api/v1`、OpenRouter API key，
并将别名的 `upstream_model` 设置为 `typesafe/jev-1.13` 等实际可用 ID。

网关配置的 base URL **包含 `/v1`**，只追加 `/systemone`。
这与 TypeSafe 官方 SDK 的 baseURL 约定不同：SDK 会自行追加 `/v1/systemone`。
若用官方 SDK 请求本地网关，SDK 的 baseURL 应为 `http://127.0.0.1:7421`，
apiKey 使用网关 token，model 使用已配置的别名。

## 独立 TypeSafe 实例与真实 API 测试

仅运行 TypeSafe 的实例可通过进程环境配置：

```sh
export LLM_GATEWAY_PROVIDER_KIND=typesafe
export LLM_GATEWAY_PROVIDER_NAME=typesafe
export LLM_GATEWAY_PROVIDER_API_KEY="$TYPESAFE_API_KEY"
export LLM_GATEWAY_PROVIDER_TYPESAFE_BASE_URL=https://api.typesafe.ai/v1
./bin/llm-gateway start
```

这种启动方式会把该 provider 设为全局默认，因此适合独立实例。
请求可直接使用 `model: "jev-latest"`。环境变量种子按名称幂等，不覆盖已有同名 provider 的凭据或 URL。

已有 `TYPESAFE_API_KEY` 进程环境变量时，可执行一次真实请求验证三种问题类型：

```sh
go test -tags live ./internal/provider -run '^TestLive_TypeSafe_SystemOne$' -v
```

可选 `TYPESAFE_BASE_URL`（包含 `/v1`）和 `TYPESAFE_MODEL` 覆盖测试目标。
该测试只显式运行时才调用付费上游；普通 `go test ./...` 使用本地 mock，不读取凭据文件。

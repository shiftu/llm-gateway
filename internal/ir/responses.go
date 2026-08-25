package ir

// Responses API ↔ Chat Completions 翻译。
//
// 为什么不走 IR 中间表示：IR 的 JSON 形状是 Anthropic 的（见 types.go 顶部注释），
// 而 Responses 和 Chat 同属 OpenAI 家族。绕一圈 Anthropic 会在 tool_call id、
// arguments 的字符串/对象之分上多两次有损转换，换不来任何好处。所以这里直接
// Responses ↔ Chat，只是把代码放在 ir 包里 —— 线格翻译该待的地方。
//
// 契约来源：docs/design/responses-api.md。那份文档是抓 codex-cli 0.148.0 的
// 真实报文 + 拿真 codex 当验收器试出来的，不是照文档抄的。

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Responses 的 input 条目类型。
const (
	RespItemMessage            = "message"
	RespItemFunctionCall       = "function_call"
	RespItemFunctionCallOutput = "function_call_output"
)

// ErrEmptyInput：input 为空（或全被过滤）时必须报错，不能往上游发
// `messages: null` —— cc-switch issue #2806 就是栽在这里。
var ErrEmptyInput = errors.New("responses: input 为空，无法构造 chat messages")

// ResponsesRequest 是 codex 发来的报文里我们**用得上**的部分。
// store / prompt_cache_key / reasoning / include / client_metadata 故意不收：
// 上游 Chat 端点不认，收了也只能丢。
type ResponsesRequest struct {
	Model             string          `json:"model"`
	Instructions      string          `json:"instructions,omitempty"`
	Input             []ResponsesItem `json:"input"`
	Tools             []ResponsesTool `json:"tools,omitempty"`
	ToolChoice        json.RawMessage `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	Stream            bool            `json:"stream,omitempty"`
	MaxOutputTokens   int             `json:"max_output_tokens,omitempty"`
	Temperature       *float32        `json:"temperature,omitempty"`
}

type ResponsesItem struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
	Role string `json:"role,omitempty"`
	// message
	Content []ResponsesPart `json:"content,omitempty"`
	// function_call
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	// function_call_output
	Output string `json:"output,omitempty"`
}

type ResponsesPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ResponsesTool 是**扁平**的；Chat 的是嵌套在 function 下的。这是两边最容易
// 弄反的一处。
type ResponsesTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// ---- Responses → Chat -----------------------------------------------------

// ChatRequestFromResponses 把 Responses 报文翻成 Chat Completions 报文。
func ChatRequestFromResponses(raw []byte) ([]byte, error) {
	var req ResponsesRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("responses: 解析请求失败: %w", err)
	}

	msgs := make([]map[string]any, 0, len(req.Input)+1)
	if req.Instructions != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": req.Instructions})
	}
	for _, it := range req.Input {
		m, ok := chatMessageFromItem(it)
		if ok {
			msgs = append(msgs, m)
		}
	}
	// 只有 instructions、没有任何 input 条目也算空 —— 上游拿一条 system
	// 是问不出东西的，早报错比让上游回一个莫名其妙的 400 好。
	if len(req.Input) == 0 || len(msgs) == 0 {
		return nil, ErrEmptyInput
	}

	out := map[string]any{
		"model":    req.Model,
		"messages": msgs,
		"stream":   true,
	}
	// 要 usage 必须显式开，否则 OpenAI 兼容端点的流里没有 usage，
	// 计费会静默变成 0。
	out["stream_options"] = map[string]any{"include_usage": true}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			if t.Type != "function" || t.Name == "" {
				continue
			}
			fn := map[string]any{"name": t.Name}
			if t.Description != "" {
				fn["description"] = t.Description
			}
			if t.Parameters != nil {
				fn["parameters"] = t.Parameters
			}
			tools = append(tools, map[string]any{"type": "function", "function": fn})
		}
		if len(tools) > 0 {
			out["tools"] = tools
			if len(req.ToolChoice) > 0 {
				out["tool_choice"] = req.ToolChoice
			}
			if req.ParallelToolCalls != nil {
				out["parallel_tool_calls"] = *req.ParallelToolCalls
			}
		}
	}
	if req.MaxOutputTokens > 0 {
		out["max_tokens"] = req.MaxOutputTokens
	}
	if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}
	return json.Marshal(out)
}

// chatMessageFromItem 翻一条 input 条目。返回 false 表示这条不产生消息。
func chatMessageFromItem(it ResponsesItem) (map[string]any, bool) {
	switch it.Type {
	case RespItemMessage:
		role := it.Role
		// developer 是 Responses 特有的角色，Chat 端点普遍不认。
		if role == "developer" {
			role = "system"
		}
		if role == "" {
			role = "user"
		}
		text := ""
		for _, p := range it.Content {
			// 只取文本 part。图片等非文本 part 直接跳过 ——
			// 把 base64 塞进文本字段会被当成 tool text 重放
			// （cc-switch issue #5663）。宁可少一块内容，不可脏一段上下文。
			switch p.Type {
			case "input_text", "output_text", "text":
				text += p.Text
			}
		}
		if text == "" {
			return nil, false
		}
		return map[string]any{"role": role, "content": text}, true

	case RespItemFunctionCall:
		if it.CallID == "" || it.Name == "" {
			return nil, false
		}
		args := it.Arguments
		if args == "" {
			args = "{}" // 上游普遍要求 arguments 是可解析的 JSON 字符串
		}
		return map[string]any{
			"role":    "assistant",
			"content": nil,
			"tool_calls": []map[string]any{{
				"id":       it.CallID,
				"type":     "function",
				"function": map[string]any{"name": it.Name, "arguments": args},
			}},
		}, true

	case RespItemFunctionCallOutput:
		if it.CallID == "" {
			return nil, false
		}
		return map[string]any{
			"role":         "tool",
			"tool_call_id": it.CallID,
			"content":      it.Output,
		}, true
	}
	return nil, false
}

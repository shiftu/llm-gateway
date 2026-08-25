package ir

// Chat Completions SSE → Responses SSE，有状态的一路翻译。
//
// 为什么有状态：Responses 一个"回答"要拼成一串固定骨架的事件
// （response.created → ... → response.completed），而上游 Chat 只是一串
// 松散的 delta。这个状态机把 delta 攒起来，在正确的时刻吐出正确的事件。
//
// 事件序列（纯文本 9 个、工具调用 5 个）来自 docs/design/responses-api.md，
// 是拿真 codex 当验收器一轮轮试出来的 —— 顺序和字段名不是随便能改的。
//
// 已知限制：只支持单个 output item（一段文本，或一次 function_call）。
// codex 目前的用法就是这样：要么整段话，要么一次工具调用，没有同一轮里
// 文本和工具调用交替、或者并行多个工具调用的情况。真出现那种报文，
// 这里会按"只认第一个 tool_call"处理，不会崩，但行为未经真机验证。

import (
	"bytes"
	"encoding/json"
)

// ResponsesStreamState 把一条 Chat SSE 流翻译成 Responses SSE 事件序列。
// 每个请求一个实例，不可并发复用。
type ResponsesStreamState struct {
	respID string
	model  string
	seq    int

	itemStarted bool
	kind        string // "message" | "function_call"
	itemID      string
	textBuf     bytes.Buffer

	callID  string
	fnName  string
	argsBuf bytes.Buffer

	finished  bool // *.done 事件已发，只差 response.completed
	responded bool // response.completed 已发 —— 之后的 Feed 全部丢弃
	finalItem map[string]any

	usageIn  int
	usageOut int
}

// NewResponsesStreamState 创建一个新的翻译状态机。respID 和 model 会出现在
// 每个事件的 response 对象里。
func NewResponsesStreamState(respID, model string) *ResponsesStreamState {
	return &ResponsesStreamState{respID: respID, model: model}
}

type chatStreamToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type chatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string                    `json:"content,omitempty"`
			ToolCalls []chatStreamToolCallDelta `json:"tool_calls,omitempty"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Start 返回开场的两个事件：response.created + response.in_progress。
// 调用方在收到上游 200 之后、转发第一个 delta 之前调用一次。
func (s *ResponsesStreamState) Start() [][]byte {
	resp := map[string]any{
		"id": s.respID, "object": "response", "model": s.model,
		"status": "in_progress", "output": []any{},
	}
	return [][]byte{
		s.emit("response.created", map[string]any{"response": resp}),
		s.emit("response.in_progress", map[string]any{"response": resp}),
	}
}

// Feed 处理一条上游 Chat SSE 的 `data: {...}` payload（不含 "data: " 前缀，
// 也不是 "[DONE]"），返回翻译出的 0 个或多个 Responses SSE 事件。
// response.completed 发出之后，后续调用一律返回 nil —— codex 不接受
// response.completed 之后还有事件。
func (s *ResponsesStreamState) Feed(data []byte) [][]byte {
	if s.responded {
		return nil
	}
	var chunk chatStreamChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		return nil
	}

	var events [][]byte
	if len(chunk.Choices) > 0 {
		c := chunk.Choices[0]
		events = append(events, s.feedDelta(c.Delta.Content, c.Delta.ToolCalls)...)
		if c.FinishReason != nil && *c.FinishReason != "" {
			events = append(events, s.finishItem()...)
		}
	}
	if chunk.Usage != nil {
		s.usageIn = chunk.Usage.PromptTokens
		s.usageOut = chunk.Usage.CompletionTokens
		if s.finished {
			events = append(events, s.complete())
		}
	}
	return events
}

// Finish 是收尾兜底：上游 SSE 流结束（[DONE] 或 body 读完）时调用一次。
// 如果 response.completed 因为上游没发独立的 usage chunk 而还没发出，
// 这里补发 —— **不能让 response.completed 缺席**，缺了 codex 会报
// "stream closed before response.completed" 并重试 5 次。
func (s *ResponsesStreamState) Finish() [][]byte {
	if s.responded {
		return nil
	}
	var events [][]byte
	if !s.finished {
		events = append(events, s.finishItem()...)
	}
	events = append(events, s.complete())
	return events
}

func (s *ResponsesStreamState) feedDelta(content string, toolCalls []chatStreamToolCallDelta) [][]byte {
	if len(toolCalls) > 0 {
		return s.feedToolCallDelta(toolCalls[0])
	}
	if content != "" {
		return s.feedTextDelta(content)
	}
	return nil
}

func (s *ResponsesStreamState) feedTextDelta(content string) [][]byte {
	var events [][]byte
	if !s.itemStarted {
		s.itemStarted = true
		s.kind = "message"
		s.itemID = s.respID + "_out0"
		events = append(events, s.emit("response.output_item.added", map[string]any{
			"output_index": 0,
			"item": map[string]any{
				"type": "message", "id": s.itemID, "status": "in_progress",
				"role": "assistant", "content": []any{},
			},
		}))
		events = append(events, s.emit("response.content_part.added", map[string]any{
			"item_id": s.itemID, "output_index": 0, "content_index": 0,
			"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
		}))
	}
	s.textBuf.WriteString(content)
	events = append(events, s.emit("response.output_text.delta", map[string]any{
		"item_id": s.itemID, "output_index": 0, "content_index": 0, "delta": content,
	}))
	return events
}

func (s *ResponsesStreamState) feedToolCallDelta(tc chatStreamToolCallDelta) [][]byte {
	var events [][]byte
	if !s.itemStarted {
		s.itemStarted = true
		s.kind = "function_call"
		s.itemID = s.respID + "_fc0"
		s.callID = tc.ID
		s.fnName = tc.Function.Name
		events = append(events, s.emit("response.output_item.added", map[string]any{
			"output_index": 0,
			"item": map[string]any{
				"type": "function_call", "id": s.itemID, "call_id": s.callID,
				"name": s.fnName, "arguments": "", "status": "in_progress",
			},
		}))
	}
	if tc.Function.Arguments != "" {
		s.argsBuf.WriteString(tc.Function.Arguments)
		events = append(events, s.emit("response.function_call_arguments.delta", map[string]any{
			"item_id": s.itemID, "output_index": 0, "delta": tc.Function.Arguments,
		}))
	}
	return events
}

// finishItem 收掉当前 output item，发 *.done 系列事件。幂等：已经 finish
// 过或者从没起过 item（空回答）都直接返回 nil。
func (s *ResponsesStreamState) finishItem() [][]byte {
	if s.finished {
		return nil
	}
	s.finished = true
	if !s.itemStarted {
		return nil
	}

	var events [][]byte
	switch s.kind {
	case "message":
		text := s.textBuf.String()
		events = append(events, s.emit("response.output_text.done", map[string]any{
			"item_id": s.itemID, "output_index": 0, "content_index": 0, "text": text,
		}))
		events = append(events, s.emit("response.content_part.done", map[string]any{
			"item_id": s.itemID, "output_index": 0, "content_index": 0,
			"part": map[string]any{"type": "output_text", "text": text, "annotations": []any{}},
		}))
		s.finalItem = map[string]any{
			"type": "message", "id": s.itemID, "status": "completed", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
		}
		events = append(events, s.emit("response.output_item.done", map[string]any{
			"output_index": 0, "item": s.finalItem,
		}))
	case "function_call":
		args := s.argsBuf.String()
		if args == "" {
			args = "{}" // 上游普遍要求 arguments 是可解析的 JSON 字符串
		}
		events = append(events, s.emit("response.function_call_arguments.done", map[string]any{
			"item_id": s.itemID, "output_index": 0, "arguments": args,
		}))
		s.finalItem = map[string]any{
			"type": "function_call", "id": s.itemID, "call_id": s.callID,
			"name": s.fnName, "arguments": args, "status": "completed",
		}
		events = append(events, s.emit("response.output_item.done", map[string]any{
			"output_index": 0, "item": s.finalItem,
		}))
	}
	return events
}

func (s *ResponsesStreamState) complete() []byte {
	s.responded = true
	output := []any{}
	if s.finalItem != nil {
		output = []any{s.finalItem}
	}
	resp := map[string]any{
		"id": s.respID, "object": "response", "model": s.model,
		"status": "completed", "output": output,
		"usage": map[string]any{
			"input_tokens": s.usageIn, "output_tokens": s.usageOut,
			"total_tokens": s.usageIn + s.usageOut,
		},
	}
	return s.emit("response.completed", map[string]any{"response": resp})
}

// Usage 返回目前累计到的 token 计数，供调用方计费用 —— 上游是纯 Chat
// 报文，这里直接暴露翻好的 Responses 计数，调用方不用再解一遍。
func (s *ResponsesStreamState) Usage() (input, output int) {
	return s.usageIn, s.usageOut
}

func (s *ResponsesStreamState) emit(eventType string, payload map[string]any) []byte {
	payload["type"] = eventType
	payload["sequence_number"] = s.seq
	s.seq++
	data, _ := json.Marshal(payload)
	var buf bytes.Buffer
	buf.WriteString("event: ")
	buf.WriteString(eventType)
	buf.WriteByte('\n')
	buf.WriteString("data: ")
	buf.Write(data)
	buf.WriteString("\n\n")
	return buf.Bytes()
}

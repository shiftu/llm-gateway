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
// 支持多个并行工具调用：每个 tool_call 是独立的 output item，有自己的
// output_index。文本输出（如果有）占用 output_index 0，后续工具调用依次递增。

import (
	"bytes"
	"encoding/json"
)

// toolCallState tracks the state of a single tool call in the stream.
type toolCallState struct {
	itemID  string
	callID  string
	fnName  string
	argsBuf bytes.Buffer
}

// ResponsesStreamState 把一条 Chat SSE 流翻译成 Responses SSE 事件序列。
// 每个请求一个实例，不可并发复用。
type ResponsesStreamState struct {
	respID string
	model  string
	seq    int

	// Message (text) output
	messageStarted bool
	messageID      string
	textBuf        bytes.Buffer

	// Multiple tool calls, keyed by index from upstream
	toolCalls     map[int]*toolCallState
	toolCallOrder []int // maintain insertion order for output_index

	finished   bool // *.done 事件已发，只差 response.completed
	responded  bool // response.completed 已发 —— 之后的 Feed 全部丢弃
	finalItems []map[string]any

	usageIn  int
	usageOut int
}

// NewResponsesStreamState 创建一个新的翻译状态机。respID 和 model 会出现在
// 每个事件的 response 对象里。
func NewResponsesStreamState(respID, model string) *ResponsesStreamState {
	return &ResponsesStreamState{
		respID:    respID,
		model:     model,
		toolCalls: make(map[int]*toolCallState),
	}
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
	var events [][]byte
	// Process all tool calls (may be multiple in parallel)
	for _, tc := range toolCalls {
		events = append(events, s.feedToolCallDelta(tc)...)
	}
	// Process text content (only if no tool calls in this chunk)
	if len(toolCalls) == 0 && content != "" {
		events = append(events, s.feedTextDelta(content)...)
	}
	return events
}

func (s *ResponsesStreamState) feedTextDelta(content string) [][]byte {
	var events [][]byte
	if !s.messageStarted {
		s.messageStarted = true
		s.messageID = s.respID + "_out0"
		events = append(events, s.emit("response.output_item.added", map[string]any{
			"output_index": 0,
			"item": map[string]any{
				"type": "message", "id": s.messageID, "status": "in_progress",
				"role": "assistant", "content": []any{},
			},
		}))
		events = append(events, s.emit("response.content_part.added", map[string]any{
			"item_id": s.messageID, "output_index": 0, "content_index": 0,
			"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
		}))
	}
	s.textBuf.WriteString(content)
	events = append(events, s.emit("response.output_text.delta", map[string]any{
		"item_id": s.messageID, "output_index": 0, "content_index": 0, "delta": content,
	}))
	return events
}

func (s *ResponsesStreamState) feedToolCallDelta(tc chatStreamToolCallDelta) [][]byte {
	var events [][]byte
	idx := tc.Index

	// Get or create tool call state for this index
	tcState, exists := s.toolCalls[idx]
	if !exists {
		tcState = &toolCallState{
			itemID: s.respID + "_fc" + string(rune('0'+len(s.toolCalls))),
			callID: tc.ID,
			fnName: tc.Function.Name,
		}
		s.toolCalls[idx] = tcState
		s.toolCallOrder = append(s.toolCallOrder, idx)

		// Calculate output_index: message (if any) is 0, tool calls start after
		outputIndex := len(s.toolCallOrder) - 1 // 0-based position in toolCallOrder
		if s.messageStarted {
			outputIndex++ // shift by 1 if message exists
		}

		events = append(events, s.emit("response.output_item.added", map[string]any{
			"output_index": outputIndex,
			"item": map[string]any{
				"type": "function_call", "id": tcState.itemID, "call_id": tcState.callID,
				"name": tcState.fnName, "arguments": "", "status": "in_progress",
			},
		}))
	}

	if tc.Function.Arguments != "" {
		tcState.argsBuf.WriteString(tc.Function.Arguments)
		// Calculate output_index for this tool call
		outputIndex := 0
		for i, orderIdx := range s.toolCallOrder {
			if orderIdx == idx {
				outputIndex = i
				if s.messageStarted {
					outputIndex++
				}
				break
			}
		}
		events = append(events, s.emit("response.function_call_arguments.delta", map[string]any{
			"item_id": tcState.itemID, "output_index": outputIndex, "delta": tc.Function.Arguments,
		}))
	}
	return events
}

// finishItem 收掉所有 output items，发 *.done 系列事件。幂等：已经 finish
// 过或者从没起过任何 item（空回答）都直接返回 nil。
func (s *ResponsesStreamState) finishItem() [][]byte {
	if s.finished {
		return nil
	}
	s.finished = true

	var events [][]byte

	// Finish message (text) if started
	if s.messageStarted {
		text := s.textBuf.String()
		events = append(events, s.emit("response.output_text.done", map[string]any{
			"item_id": s.messageID, "output_index": 0, "content_index": 0, "text": text,
		}))
		events = append(events, s.emit("response.content_part.done", map[string]any{
			"item_id": s.messageID, "output_index": 0, "content_index": 0,
			"part": map[string]any{"type": "output_text", "text": text, "annotations": []any{}},
		}))
		msgItem := map[string]any{
			"type": "message", "id": s.messageID, "status": "completed", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
		}
		events = append(events, s.emit("response.output_item.done", map[string]any{
			"output_index": 0, "item": msgItem,
		}))
		s.finalItems = append(s.finalItems, msgItem)
	}

	// Finish all tool calls in order
	for i, idx := range s.toolCallOrder {
		tcState := s.toolCalls[idx]
		args := tcState.argsBuf.String()
		if args == "" {
			args = "{}" // 上游普遍要求 arguments 是可解析的 JSON 字符串
		}

		// Calculate output_index for this tool call
		outputIndex := i
		if s.messageStarted {
			outputIndex++
		}

		events = append(events, s.emit("response.function_call_arguments.done", map[string]any{
			"item_id": tcState.itemID, "output_index": outputIndex, "arguments": args,
		}))
		fcItem := map[string]any{
			"type": "function_call", "id": tcState.itemID, "call_id": tcState.callID,
			"name": tcState.fnName, "arguments": args, "status": "completed",
		}
		events = append(events, s.emit("response.output_item.done", map[string]any{
			"output_index": outputIndex, "item": fcItem,
		}))
		s.finalItems = append(s.finalItems, fcItem)
	}

	return events
}

func (s *ResponsesStreamState) complete() []byte {
	s.responded = true
	output := []any{}
	if len(s.finalItems) > 0 {
		for _, item := range s.finalItems {
			output = append(output, item)
		}
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

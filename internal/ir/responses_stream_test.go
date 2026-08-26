package ir

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// parseEvents 把 emit() 吐出的 [][]byte 拆成 (type, 完整 payload map) 便于断言。
func parseEvents(t *testing.T, chunks [][]byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, c := range chunks {
		lines := strings.SplitN(string(c), "\n", 3)
		if len(lines) < 2 {
			t.Fatalf("malformed SSE frame: %q", c)
		}
		if !strings.HasPrefix(lines[0], "event: ") {
			t.Fatalf("frame missing event: line: %q", c)
		}
		if !strings.HasPrefix(lines[1], "data: ") {
			t.Fatalf("frame missing data: line: %q", c)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[1], "data: ")), &payload); err != nil {
			t.Fatalf("data line not valid JSON: %v (%q)", err, c)
		}
		if payload["type"] != strings.TrimPrefix(lines[0], "event: ") {
			t.Fatalf("event: line %q disagrees with data.type %v", lines[0], payload["type"])
		}
		out = append(out, payload)
	}
	return out
}

func eventTypes(events []map[string]any) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e["type"].(string)
	}
	return out
}

func TestResponsesStream_PureText_NineEvents(t *testing.T) {
	s := NewResponsesStreamState("resp_1", "test-model")

	var all [][]byte
	all = append(all, s.Start()...)
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{"content":"Hel"}}]}`))...)
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{"content":"lo"}}]}`))...)
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`))...)
	all = append(all, s.Feed([]byte(`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2}}`))...)

	events := parseEvents(t, all)
	got := eventTypes(events)
	want := []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	}
	if len(got) != len(want) {
		t.Fatalf("event count = %d, want %d\ngot: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// sequence_number must be strictly increasing from 0.
	for i, e := range events {
		if int(e["sequence_number"].(float64)) != i {
			t.Errorf("event[%d].sequence_number = %v, want %d", i, e["sequence_number"], i)
		}
	}

	done := events[6]
	if done["text"] != "Hello" {
		t.Errorf("response.output_text.done.text = %v, want %q", done["text"], "Hello")
	}

	completed := events[9]
	respObj := completed["response"].(map[string]any)
	if respObj["status"] != "completed" {
		t.Errorf("response.completed.response.status = %v, want completed", respObj["status"])
	}
	usage := respObj["usage"].(map[string]any)
	if usage["input_tokens"] != float64(10) || usage["output_tokens"] != float64(2) || usage["total_tokens"] != float64(12) {
		t.Errorf("usage = %v, want input=10 output=2 total=12", usage)
	}
}

func TestResponsesStream_ToolCall_FiveEvents(t *testing.T) {
	s := NewResponsesStreamState("resp_2", "test-model")

	var all [][]byte
	all = append(all, s.Start()...)
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"bash","arguments":""}}]}}]}`))...)
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"cmd\":"}}]}}]}`))...)
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"echo TOOLS-OK\"}"}}]}}]}`))...)
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`))...)
	all = append(all, s.Feed([]byte(`{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":1}}`))...)

	// Start() 产出的两个事件不算在 doc 的"5 个事件"工具调用序列里 ——
	// 那 5 个事件是从 output_item.added 开始数的。
	all = all[2:]

	events := parseEvents(t, all)
	got := eventTypes(events)
	want := []string{
		"response.output_item.added",
		"response.function_call_arguments.delta",
		"response.function_call_arguments.delta",
		"response.function_call_arguments.done",
		"response.output_item.done",
		"response.completed",
	}
	if len(got) != len(want) {
		t.Fatalf("event count = %d, want %d\ngot: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	added := events[0]["item"].(map[string]any)
	if added["call_id"] != "call_abc" || added["name"] != "bash" {
		t.Errorf("output_item.added.item = %v, want call_id=call_abc name=bash", added)
	}

	argsDone := events[3]
	if argsDone["arguments"] != `{"cmd":"echo TOOLS-OK"}` {
		t.Errorf("function_call_arguments.done.arguments = %v", argsDone["arguments"])
	}

	itemDone := events[4]["item"].(map[string]any)
	if itemDone["status"] != "completed" || itemDone["call_id"] != "call_abc" {
		t.Errorf("output_item.done.item = %v", itemDone)
	}
}

func TestResponsesStream_MultipleToolCalls(t *testing.T) {
	// 测试多个并行工具调用：两个 tool_call 同时出现，各自有独立的 output_index
	s := NewResponsesStreamState("resp_multi", "test-model")

	var all [][]byte
	all = append(all, s.Start()...)
	// 第一个 chunk: 两个 tool_call 同时出现（index 0 和 1）
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{"tool_calls":[
		{"index":0,"id":"call_1","type":"function","function":{"name":"bash","arguments":""}},
		{"index":1,"id":"call_2","type":"function","function":{"name":"read","arguments":""}}
	]}}]}`))...)
	// 第二个 chunk: 两个 tool_call 的 arguments delta
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{"tool_calls":[
		{"index":0,"function":{"arguments":"{\"cmd\":"}},
		{"index":1,"function":{"arguments":"{\"file\":"}}
	]}}]}`))...)
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{"tool_calls":[
		{"index":0,"function":{"arguments":"\"echo hi\"}"}},
		{"index":1,"function":{"arguments":"\"test.txt\"}"}}
	]}}]}`))...)
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`))...)
	all = append(all, s.Feed([]byte(`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))...)

	events := parseEvents(t, all)
	got := eventTypes(events)

	// 期望的事件序列：
	// 1. response.created
	// 2. response.in_progress
	// 3. response.output_item.added (tool call 0)
	// 4. response.output_item.added (tool call 1)
	// 5-6. response.function_call_arguments.delta (tool call 0, 两次)
	// 7-8. response.function_call_arguments.delta (tool call 1, 两次)
	// 9. response.function_call_arguments.done (tool call 0)
	// 10. response.output_item.done (tool call 0)
	// 11. response.function_call_arguments.done (tool call 1)
	// 12. response.output_item.done (tool call 1)
	// 13. response.completed

	// 验证有两个 output_item.added
	addedCount := 0
	for _, e := range events {
		if e["type"] == "response.output_item.added" {
			addedCount++
		}
	}
	if addedCount != 2 {
		t.Errorf("expected 2 output_item.added events, got %d", addedCount)
	}

	// 验证有两个 output_item.done
	doneCount := 0
	for _, e := range events {
		if e["type"] == "response.output_item.done" {
			doneCount++
		}
	}
	if doneCount != 2 {
		t.Errorf("expected 2 output_item.done events, got %d", doneCount)
	}

	// 验证 output_index 正确：第一个 tool call 是 0，第二个是 1
	for _, e := range events {
		if e["type"] == "response.output_item.added" {
			item := e["item"].(map[string]any)
			callID := item["call_id"].(string)
			outputIndex := int(e["output_index"].(float64))
			if callID == "call_1" && outputIndex != 0 {
				t.Errorf("call_1 should have output_index 0, got %d", outputIndex)
			}
			if callID == "call_2" && outputIndex != 1 {
				t.Errorf("call_2 should have output_index 1, got %d", outputIndex)
			}
		}
	}

	// 验证 response.completed 的 output 数组包含两个 item
	completed := events[len(events)-1]
	if completed["type"] != "response.completed" {
		t.Fatalf("last event should be response.completed, got %v", completed["type"])
	}
	resp := completed["response"].(map[string]any)
	output := resp["output"].([]any)
	if len(output) != 2 {
		t.Errorf("response.completed.output should have 2 items, got %d", len(output))
	}

	// 验证事件类型序列（跳过前两个 created/in_progress）
	wantTypes := []string{
		"response.output_item.added",
		"response.output_item.added",
		"response.function_call_arguments.delta",
		"response.function_call_arguments.delta",
		"response.function_call_arguments.delta",
		"response.function_call_arguments.delta",
		"response.function_call_arguments.done",
		"response.output_item.done",
		"response.function_call_arguments.done",
		"response.output_item.done",
		"response.completed",
	}
	gotTypes := got[2:] // 跳过 created 和 in_progress
	if len(gotTypes) != len(wantTypes) {
		t.Fatalf("event type count mismatch: got %d, want %d\ngot: %v", len(gotTypes), len(wantTypes), gotTypes)
	}
	for i := range wantTypes {
		if gotTypes[i] != wantTypes[i] {
			t.Errorf("event[%d] = %q, want %q", i+2, gotTypes[i], wantTypes[i])
		}
	}
}

func TestResponsesStream_MessageThenToolCalls(t *testing.T) {
	// 测试文本消息后跟工具调用：文本占用 output_index 0，工具调用从 1 开始
	s := NewResponsesStreamState("resp_mixed", "test-model")

	var all [][]byte
	all = append(all, s.Start()...)
	// 先发送文本
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{"content":"Let me "}}]}`))...)
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{"content":"help you."}}]}`))...)
	// 然后发送工具调用
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"bash","arguments":""}}]}}]}`))...)
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"cmd\":\"echo hi\"}"}}]}}]}`))...)
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`))...)
	all = append(all, s.Feed([]byte(`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))...)

	events := parseEvents(t, all)

	// 验证 message 的 output_index 是 0
	for _, e := range events {
		if e["type"] == "response.output_item.added" {
			item := e["item"].(map[string]any)
			if item["type"] == "message" {
				outputIndex := int(e["output_index"].(float64))
				if outputIndex != 0 {
					t.Errorf("message should have output_index 0, got %d", outputIndex)
				}
			}
			if item["type"] == "function_call" {
				outputIndex := int(e["output_index"].(float64))
				if outputIndex != 1 {
					t.Errorf("function_call should have output_index 1 (after message), got %d", outputIndex)
				}
			}
		}
	}

	// 验证 response.completed 的 output 数组包含两个 item
	completed := events[len(events)-1]
	resp := completed["response"].(map[string]any)
	output := resp["output"].([]any)
	if len(output) != 2 {
		t.Errorf("response.completed.output should have 2 items (message + function_call), got %d", len(output))
	}
}

func TestResponsesStream_ToolCallsThenMessage(t *testing.T) {
	// 反向顺序：工具调用先到，文本后到 —— output_index 不应假设文本永远是 0。
	s := NewResponsesStreamState("resp_rev", "test-model")

	var all [][]byte
	all = append(all, s.Start()...)
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"bash","arguments":"{}"}}]}}]}`))...)
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{"content":"done"}}]}`))...)
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`))...)
	all = append(all, s.Feed([]byte(`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))...)

	events := parseEvents(t, all)

	seenIndices := map[string]int{}
	for _, e := range events {
		if e["type"] != "response.output_item.added" {
			continue
		}
		item := e["item"].(map[string]any)
		seenIndices[item["type"].(string)] = int(e["output_index"].(float64))
	}
	if seenIndices["function_call"] != 0 {
		t.Errorf("function_call (arrived first) should have output_index 0, got %d", seenIndices["function_call"])
	}
	if seenIndices["message"] != 1 {
		t.Errorf("message (arrived second) should have output_index 1, got %d", seenIndices["message"])
	}

	completed := events[len(events)-1]
	resp := completed["response"].(map[string]any)
	output := resp["output"].([]any)
	if len(output) != 2 {
		t.Fatalf("response.completed.output should have 2 items, got %d", len(output))
	}
	if output[0].(map[string]any)["type"] != "function_call" || output[1].(map[string]any)["type"] != "message" {
		t.Errorf("output items should preserve arrival order [function_call, message], got %v", output)
	}
}

func TestResponsesStream_TenParallelToolCalls_UniqueItemIDs(t *testing.T) {
	// >9 个并行工具调用时，itemID 生成不能产生非数字字符或重复值。
	s := NewResponsesStreamState("resp_many", "test-model")
	s.Start()

	var deltas []byte
	deltas = append(deltas, `{"choices":[{"delta":{"tool_calls":[`...)
	for i := 0; i < 10; i++ {
		if i > 0 {
			deltas = append(deltas, ',')
		}
		deltas = append(deltas, []byte(`{"index":`+strconv.Itoa(i)+`,"id":"call_`+strconv.Itoa(i)+`","type":"function","function":{"name":"f","arguments":"{}"}}`)...)
	}
	deltas = append(deltas, `]}}]}`...)

	events := parseEvents(t, s.Feed(deltas))
	seen := map[string]bool{}
	for _, e := range events {
		if e["type"] != "response.output_item.added" {
			continue
		}
		id := e["item"].(map[string]any)["id"].(string)
		if seen[id] {
			t.Errorf("duplicate itemID %q", id)
		}
		seen[id] = true
	}
	if len(seen) != 10 {
		t.Errorf("expected 10 unique itemIDs, got %d", len(seen))
	}
}

func TestResponsesStream_Finish_FallsBackWhenUsageNeverArrives(t *testing.T) {
	s := NewResponsesStreamState("resp_3", "test-model")
	var all [][]byte
	all = append(all, s.Start()...)
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{"content":"hi"}}]}`))...)
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`))...)
	// no usage chunk ever arrives — [DONE] observed directly.
	all = append(all, s.Finish()...)

	events := parseEvents(t, all)
	last := events[len(events)-1]
	if last["type"] != "response.completed" {
		t.Fatalf("last event = %v, want response.completed", last["type"])
	}
}

func TestResponsesStream_ResponsesCompletedNeverDuplicated(t *testing.T) {
	s := NewResponsesStreamState("resp_4", "test-model")
	s.Start()
	s.Feed([]byte(`{"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	if extra := s.Feed([]byte(`{"choices":[{"delta":{"content":"more"}}]}`)); extra != nil {
		t.Errorf("Feed after response.completed returned %v, want nil", extra)
	}
	if extra := s.Finish(); extra != nil {
		t.Errorf("Finish after response.completed returned %v, want nil", extra)
	}
}

func TestResponsesStream_EmptyResponse_StillCompletes(t *testing.T) {
	s := NewResponsesStreamState("resp_5", "test-model")
	var all [][]byte
	all = append(all, s.Start()...)
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`))...)
	all = append(all, s.Finish()...)

	events := parseEvents(t, all)
	got := eventTypes(events)
	// No content ever arrived, so no output_item.* events — just the
	// created/in_progress bookends plus a bare response.completed.
	want := []string{"response.created", "response.in_progress", "response.completed"}
	if len(got) != len(want) {
		t.Fatalf("event count = %d, want %d\ngot: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResponsesStream_Failed(t *testing.T) {
	s := NewResponsesStreamState("resp_fail", "test-model")
	s.Start()
	s.Feed([]byte(`{"choices":[{"delta":{"content":"partial"}}]}`))
	s.MarkFailed("upstream connection reset")
	events := parseEvents(t, s.Finish())

	last := events[len(events)-1]
	if last["type"] != "response.failed" {
		t.Fatalf("last event = %v, want response.failed", last["type"])
	}
	resp := last["response"].(map[string]any)
	if resp["status"] != "failed" {
		t.Errorf("status = %v, want failed", resp["status"])
	}
	errObj := resp["error"].(map[string]any)
	if errObj["code"] != "upstream_error" {
		t.Errorf("error.code = %v, want upstream_error", errObj["code"])
	}
	if errObj["message"] != "upstream connection reset" {
		t.Errorf("error.message = %v, want 'upstream connection reset'", errObj["message"])
	}
}

func TestResponsesStream_ReasoningTokens(t *testing.T) {
	// Test that reasoning_tokens are captured and included in total_tokens
	s := NewResponsesStreamState("resp_reason", "test-model")
	var all [][]byte
	all = append(all, s.Start()...)
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{"content":"thinking..."}}]}`))...)
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`))...)
	// Usage with reasoning tokens in completion_tokens_details (OpenAI format)
	all = append(all, s.Feed([]byte(`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":20,"completion_tokens_details":{"reasoning_tokens":5}}}`))...)

	events := parseEvents(t, all)
	completed := events[len(events)-1]
	if completed["type"] != "response.completed" {
		t.Fatalf("last event = %v, want response.completed", completed["type"])
	}
	resp := completed["response"].(map[string]any)
	usage := resp["usage"].(map[string]any)
	if usage["input_tokens"] != float64(10) {
		t.Errorf("input_tokens = %v, want 10", usage["input_tokens"])
	}
	if usage["output_tokens"] != float64(20) {
		t.Errorf("output_tokens = %v, want 20", usage["output_tokens"])
	}
	// total_tokens should include reasoning: 10 + 20 + 5 = 35
	if usage["total_tokens"] != float64(35) {
		t.Errorf("total_tokens = %v, want 35 (10 + 20 + 5 reasoning)", usage["total_tokens"])
	}

	// Verify Usage() method returns reasoning tokens
	in, out, reasoning := s.Usage()
	if in != 10 || out != 20 || reasoning != 5 {
		t.Errorf("Usage() = (%d, %d, %d), want (10, 20, 5)", in, out, reasoning)
	}
}

func TestResponsesStream_ReasoningTokens_DirectField(t *testing.T) {
	// Test reasoning tokens in direct field (DeepSeek format)
	s := NewResponsesStreamState("resp_reason2", "test-model")
	var all [][]byte
	all = append(all, s.Start()...)
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{"content":"hi"}}]}`))...)
	all = append(all, s.Feed([]byte(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`))...)
	// Usage with reasoning_tokens as direct field (DeepSeek format)
	all = append(all, s.Feed([]byte(`{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":10,"reasoning_tokens":3}}`))...)

	events := parseEvents(t, all)
	completed := events[len(events)-1]
	resp := completed["response"].(map[string]any)
	usage := resp["usage"].(map[string]any)
	// total_tokens should include reasoning: 5 + 10 + 3 = 18
	if usage["total_tokens"] != float64(18) {
		t.Errorf("total_tokens = %v, want 18 (5 + 10 + 3 reasoning)", usage["total_tokens"])
	}
}

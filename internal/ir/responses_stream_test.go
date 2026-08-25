package ir

import (
	"encoding/json"
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

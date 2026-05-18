package ir

import (
	"encoding/json"
	"testing"
)

// --- ToolsFromOpenAI ---

func TestToolsFromOpenAI_Single(t *testing.T) {
	raw := []json.RawMessage{
		json.RawMessage(`{"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}}}`),
	}
	tools := ToolsFromOpenAI(raw)
	if len(tools) != 1 {
		t.Fatalf("want 1 tool, got %d", len(tools))
	}
	if tools[0].Name != "get_weather" {
		t.Errorf("Name: want get_weather, got %q", tools[0].Name)
	}
	if tools[0].Description != "Get weather" {
		t.Errorf("Description: want 'Get weather', got %q", tools[0].Description)
	}
	if tools[0].InputSchema["type"] != "object" {
		t.Errorf("InputSchema type: want object, got %v", tools[0].InputSchema["type"])
	}
}

func TestToolsFromOpenAI_Multiple(t *testing.T) {
	raw := []json.RawMessage{
		json.RawMessage(`{"type":"function","function":{"name":"tool_a","description":"A"}}`),
		json.RawMessage(`{"type":"function","function":{"name":"tool_b","description":"B"}}`),
	}
	tools := ToolsFromOpenAI(raw)
	if len(tools) != 2 {
		t.Fatalf("want 2 tools, got %d", len(tools))
	}
	if tools[0].Name != "tool_a" || tools[1].Name != "tool_b" {
		t.Errorf("names: got %q, %q", tools[0].Name, tools[1].Name)
	}
}

func TestToolsFromOpenAI_Empty(t *testing.T) {
	if got := ToolsFromOpenAI(nil); got != nil {
		t.Errorf("nil input: want nil, got %v", got)
	}
	if got := ToolsFromOpenAI([]json.RawMessage{}); got != nil {
		t.Errorf("empty input: want nil, got %v", got)
	}
}

func TestToolsFromOpenAI_InvalidJSON(t *testing.T) {
	raw := []json.RawMessage{
		json.RawMessage(`not-valid-json`),
	}
	if got := ToolsFromOpenAI(raw); got != nil {
		t.Errorf("invalid JSON: want nil, got %v", got)
	}
}

func TestToolsFromOpenAI_MissingFunctionKey(t *testing.T) {
	// type=function but no "function" object — skip entries with empty name
	raw := []json.RawMessage{
		json.RawMessage(`{"type":"function"}`),
	}
	tools := ToolsFromOpenAI(raw)
	if tools != nil {
		t.Fatalf("want nil (empty-name entry skipped), got %v", tools)
	}
}

// --- ToolsToOpenAI ---

func TestToolsToOpenAI_Nil(t *testing.T) {
	if got := ToolsToOpenAI(nil); got != nil {
		t.Errorf("nil input: want nil, got %v", got)
	}
}

func TestToolsToOpenAI_RoundTrip(t *testing.T) {
	raw := []json.RawMessage{
		json.RawMessage(`{"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}}}`),
	}
	tools := ToolsFromOpenAI(raw)
	out := ToolsToOpenAI(tools)
	if len(out) != 1 {
		t.Fatalf("want 1, got %d", len(out))
	}
	if out[0]["type"] != "function" {
		t.Errorf("type: want function, got %v", out[0]["type"])
	}
	fn, ok := out[0]["function"].(map[string]any)
	if !ok {
		t.Fatalf("function field not a map: %T", out[0]["function"])
	}
	if fn["name"] != "get_weather" {
		t.Errorf("function.name: want get_weather, got %v", fn["name"])
	}
	if fn["description"] != "Get weather" {
		t.Errorf("function.description: want 'Get weather', got %v", fn["description"])
	}
	schema, ok := fn["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("function.parameters not a map: %T", fn["parameters"])
	}
	if schema["type"] != "object" {
		t.Errorf("parameters.type: want object, got %v", schema["type"])
	}
}

// --- ToolUsesFromOpenAI ---

func TestToolUsesFromOpenAI_Single(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"tool_calls":[{"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":"{\"location\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}]}`)
	uses := ToolUsesFromOpenAI(body)
	if len(uses) != 1 {
		t.Fatalf("want 1, got %d", len(uses))
	}
	if uses[0].ID != "call_abc" {
		t.Errorf("ID: want call_abc, got %q", uses[0].ID)
	}
	if uses[0].Name != "get_weather" {
		t.Errorf("Name: want get_weather, got %q", uses[0].Name)
	}
	if uses[0].Input["location"] != "Paris" {
		t.Errorf("Input[location]: want Paris, got %v", uses[0].Input["location"])
	}
}

func TestToolUsesFromOpenAI_Multiple(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"tool_calls":[{"id":"c1","type":"function","function":{"name":"tool_a","arguments":"{}"}},{"id":"c2","type":"function","function":{"name":"tool_b","arguments":"{\"x\":1}"}}]},"finish_reason":"tool_calls"}]}`)
	uses := ToolUsesFromOpenAI(body)
	if len(uses) != 2 {
		t.Fatalf("want 2, got %d", len(uses))
	}
	if uses[0].Name != "tool_a" || uses[1].Name != "tool_b" {
		t.Errorf("names: %q %q", uses[0].Name, uses[1].Name)
	}
}

func TestToolUsesFromOpenAI_NoToolCalls(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"hello"},"finish_reason":"stop"}]}`)
	if got := ToolUsesFromOpenAI(body); got != nil {
		t.Errorf("no tool_calls: want nil, got %v", got)
	}
}

func TestToolUsesFromOpenAI_InvalidJSON(t *testing.T) {
	if got := ToolUsesFromOpenAI([]byte(`not json`)); got != nil {
		t.Errorf("invalid json: want nil, got %v", got)
	}
}

func TestToolUsesFromOpenAI_EmptyChoices(t *testing.T) {
	if got := ToolUsesFromOpenAI([]byte(`{"choices":[]}`)); got != nil {
		t.Errorf("empty choices: want nil, got %v", got)
	}
}

// --- ToolUsesToOpenAI ---

func TestToolUsesToOpenAI_ArgumentsIsString(t *testing.T) {
	uses := []ToolUse{
		{ID: "call_abc", Name: "get_weather", Input: map[string]any{"location": "Paris"}},
	}
	out := ToolUsesToOpenAI(uses)
	if len(out) != 1 {
		t.Fatalf("want 1, got %d", len(out))
	}
	if out[0]["id"] != "call_abc" {
		t.Errorf("id: want call_abc, got %v", out[0]["id"])
	}
	if out[0]["type"] != "function" {
		t.Errorf("type: want function, got %v", out[0]["type"])
	}
	fn, ok := out[0]["function"].(map[string]any)
	if !ok {
		t.Fatalf("function not a map: %T", out[0]["function"])
	}
	if fn["name"] != "get_weather" {
		t.Errorf("function.name: want get_weather, got %v", fn["name"])
	}
	// arguments must be a JSON string, not a map — this is the OpenAI wire format
	args, ok := fn["arguments"].(string)
	if !ok {
		t.Fatalf("arguments must be string, got %T", fn["arguments"])
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(args), &parsed); err != nil {
		t.Fatalf("arguments not valid JSON string: %v", err)
	}
	if parsed["location"] != "Paris" {
		t.Errorf("arguments[location]: want Paris, got %v", parsed["location"])
	}
}

func TestToolUsesToOpenAI_Nil(t *testing.T) {
	if got := ToolUsesToOpenAI(nil); got != nil {
		t.Errorf("nil input: want nil, got %v", got)
	}
}

// --- ToolUsesFromAnthropic ---

func TestToolUsesFromAnthropic_ToolUseBlocks(t *testing.T) {
	body := []byte(`{"content":[{"type":"text","text":"Let me check"},{"type":"tool_use","id":"toolu_01","name":"get_weather","input":{"location":"Paris"}}],"stop_reason":"tool_use"}`)
	uses := ToolUsesFromAnthropic(body)
	if len(uses) != 1 {
		t.Fatalf("want 1, got %d", len(uses))
	}
	if uses[0].ID != "toolu_01" {
		t.Errorf("ID: want toolu_01, got %q", uses[0].ID)
	}
	if uses[0].Name != "get_weather" {
		t.Errorf("Name: want get_weather, got %q", uses[0].Name)
	}
	if uses[0].Input["location"] != "Paris" {
		t.Errorf("Input[location]: want Paris, got %v", uses[0].Input["location"])
	}
}

func TestToolUsesFromAnthropic_TextBlocksIgnored(t *testing.T) {
	body := []byte(`{"content":[{"type":"text","text":"hello"},{"type":"thinking","thinking":"reasoning"}],"stop_reason":"end_turn"}`)
	if got := ToolUsesFromAnthropic(body); got != nil {
		t.Errorf("no tool_use blocks: want nil, got %v", got)
	}
}

func TestToolUsesFromAnthropic_Multiple(t *testing.T) {
	body := []byte(`{"content":[{"type":"tool_use","id":"t1","name":"a","input":{}},{"type":"tool_use","id":"t2","name":"b","input":{"k":"v"}}],"stop_reason":"tool_use"}`)
	uses := ToolUsesFromAnthropic(body)
	if len(uses) != 2 {
		t.Fatalf("want 2, got %d", len(uses))
	}
}

func TestToolUsesFromAnthropic_InvalidJSON(t *testing.T) {
	if got := ToolUsesFromAnthropic([]byte(`bad`)); got != nil {
		t.Errorf("invalid json: want nil, got %v", got)
	}
}

// --- ToolUsesToAnthropic ---

func TestToolUsesToAnthropic_Structure(t *testing.T) {
	uses := []ToolUse{
		{ID: "toolu_01", Name: "get_weather", Input: map[string]any{"location": "Paris"}},
	}
	out := ToolUsesToAnthropic(uses)
	if len(out) != 1 {
		t.Fatalf("want 1, got %d", len(out))
	}
	if out[0]["type"] != "tool_use" {
		t.Errorf("type: want tool_use, got %v", out[0]["type"])
	}
	if out[0]["id"] != "toolu_01" {
		t.Errorf("id: want toolu_01, got %v", out[0]["id"])
	}
	if out[0]["name"] != "get_weather" {
		t.Errorf("name: want get_weather, got %v", out[0]["name"])
	}
	// input must be a map, not a string
	input, ok := out[0]["input"].(map[string]any)
	if !ok {
		t.Fatalf("input must be map, got %T", out[0]["input"])
	}
	if input["location"] != "Paris" {
		t.Errorf("input[location]: want Paris, got %v", input["location"])
	}
}

func TestToolUsesToAnthropic_Nil(t *testing.T) {
	if got := ToolUsesToAnthropic(nil); got != nil {
		t.Errorf("nil input: want nil, got %v", got)
	}
}

// --- ToolResultsFromOpenAI / ToolResultsToOpenAI ---

func TestToolResultsFromOpenAI_Basic(t *testing.T) {
	messages := []map[string]any{
		{"role": "tool", "tool_call_id": "call_abc", "content": "Sunny, 22°C"},
	}
	results := ToolResultsFromOpenAI(messages)
	if len(results) != 1 {
		t.Fatalf("want 1, got %d", len(results))
	}
	if results[0].ToolUseID != "call_abc" {
		t.Errorf("ToolUseID: want call_abc, got %q", results[0].ToolUseID)
	}
	if results[0].Content != "Sunny, 22°C" {
		t.Errorf("Content: want 'Sunny, 22°C', got %q", results[0].Content)
	}
	if results[0].IsError {
		t.Errorf("IsError: want false")
	}
}

func TestToolResultsFromOpenAI_SkipsNonToolRole(t *testing.T) {
	messages := []map[string]any{
		{"role": "user", "content": "hello"},
		{"role": "tool", "tool_call_id": "c1", "content": "result"},
	}
	results := ToolResultsFromOpenAI(messages)
	if len(results) != 1 {
		t.Fatalf("want 1 (skipping non-tool role), got %d", len(results))
	}
	if results[0].ToolUseID != "c1" {
		t.Errorf("ToolUseID: want c1, got %q", results[0].ToolUseID)
	}
}

func TestToolResultsFromOpenAI_Nil(t *testing.T) {
	if got := ToolResultsFromOpenAI(nil); got != nil {
		t.Errorf("nil input: want nil, got %v", got)
	}
}

func TestToolResultsToOpenAI_RoundTrip(t *testing.T) {
	messages := []map[string]any{
		{"role": "tool", "tool_call_id": "call_abc", "content": "Sunny, 22°C"},
	}
	results := ToolResultsFromOpenAI(messages)
	out := ToolResultsToOpenAI(results)
	if len(out) != 1 {
		t.Fatalf("want 1, got %d", len(out))
	}
	if out[0]["role"] != "tool" {
		t.Errorf("role: want tool, got %v", out[0]["role"])
	}
	if out[0]["tool_call_id"] != "call_abc" {
		t.Errorf("tool_call_id: want call_abc, got %v", out[0]["tool_call_id"])
	}
	if out[0]["content"] != "Sunny, 22°C" {
		t.Errorf("content: want 'Sunny, 22°C', got %v", out[0]["content"])
	}
}

func TestToolResultsToOpenAI_Nil(t *testing.T) {
	if got := ToolResultsToOpenAI(nil); got != nil {
		t.Errorf("nil input: want nil, got %v", got)
	}
}

// --- ToolResultsFromAnthropic / ToolResultsToAnthropic ---

func TestToolResultsFromAnthropic_Basic(t *testing.T) {
	content := []map[string]any{
		{"type": "tool_result", "tool_use_id": "toolu_01", "content": "Sunny, 22°C"},
	}
	results := ToolResultsFromAnthropic(content)
	if len(results) != 1 {
		t.Fatalf("want 1, got %d", len(results))
	}
	if results[0].ToolUseID != "toolu_01" {
		t.Errorf("ToolUseID: want toolu_01, got %q", results[0].ToolUseID)
	}
	if results[0].Content != "Sunny, 22°C" {
		t.Errorf("Content: want 'Sunny, 22°C', got %q", results[0].Content)
	}
}

func TestToolResultsFromAnthropic_SkipsNonToolResult(t *testing.T) {
	content := []map[string]any{
		{"type": "text", "text": "hello"},
		{"type": "tool_result", "tool_use_id": "t1", "content": "ok"},
	}
	results := ToolResultsFromAnthropic(content)
	if len(results) != 1 {
		t.Fatalf("want 1 (skipping text block), got %d", len(results))
	}
}

func TestToolResultsFromAnthropic_Nil(t *testing.T) {
	if got := ToolResultsFromAnthropic(nil); got != nil {
		t.Errorf("nil input: want nil, got %v", got)
	}
}

func TestToolResultsToAnthropic_RoundTrip(t *testing.T) {
	content := []map[string]any{
		{"type": "tool_result", "tool_use_id": "toolu_01", "content": "Sunny, 22°C"},
	}
	results := ToolResultsFromAnthropic(content)
	out := ToolResultsToAnthropic(results)
	if len(out) != 1 {
		t.Fatalf("want 1, got %d", len(out))
	}
	if out[0]["type"] != "tool_result" {
		t.Errorf("type: want tool_result, got %v", out[0]["type"])
	}
	if out[0]["tool_use_id"] != "toolu_01" {
		t.Errorf("tool_use_id: want toolu_01, got %v", out[0]["tool_use_id"])
	}
	if out[0]["content"] != "Sunny, 22°C" {
		t.Errorf("content: want 'Sunny, 22°C', got %v", out[0]["content"])
	}
}

func TestToolResultsToAnthropic_Nil(t *testing.T) {
	if got := ToolResultsToAnthropic(nil); got != nil {
		t.Errorf("nil input: want nil, got %v", got)
	}
}

// --- Cross-protocol consistency ---

func TestCrossProtocol_OpenAIToAnthropic_ToolUses(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"tool_calls":[{"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":"{\"location\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}]}`)
	uses := ToolUsesFromOpenAI(body)
	out := ToolUsesToAnthropic(uses)
	if len(out) != 1 {
		t.Fatalf("want 1, got %d", len(out))
	}
	if out[0]["name"] != "get_weather" {
		t.Errorf("name: want get_weather, got %v", out[0]["name"])
	}
	input, ok := out[0]["input"].(map[string]any)
	if !ok {
		t.Fatalf("input must be map, got %T", out[0]["input"])
	}
	if input["location"] != "Paris" {
		t.Errorf("input[location]: want Paris, got %v", input["location"])
	}
}

func TestCrossProtocol_AnthropicToOpenAI_ToolUses(t *testing.T) {
	body := []byte(`{"content":[{"type":"tool_use","id":"toolu_01","name":"get_weather","input":{"location":"Paris"}}],"stop_reason":"tool_use"}`)
	uses := ToolUsesFromAnthropic(body)
	out := ToolUsesToOpenAI(uses)
	if len(out) != 1 {
		t.Fatalf("want 1, got %d", len(out))
	}
	fn, ok := out[0]["function"].(map[string]any)
	if !ok {
		t.Fatalf("function not a map: %T", out[0]["function"])
	}
	if fn["name"] != "get_weather" {
		t.Errorf("name: want get_weather, got %v", fn["name"])
	}
	args, ok := fn["arguments"].(string)
	if !ok {
		t.Fatalf("arguments must be string, got %T", fn["arguments"])
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(args), &parsed); err != nil {
		t.Fatalf("arguments not valid JSON: %v", err)
	}
	if parsed["location"] != "Paris" {
		t.Errorf("arguments[location]: want Paris, got %v", parsed["location"])
	}
}

// --- is_error round-trip for Anthropic ---

func TestToolResultsAnthropic_IsErrorRoundTrip(t *testing.T) {
	// ToAnthropic: IsError:true should emit is_error:true in the output map
	input := []ToolResult{{ToolUseID: "t1", Content: "err", IsError: true}}
	out := ToolResultsToAnthropic(input)
	if len(out) != 1 {
		t.Fatalf("want 1, got %d", len(out))
	}
	if out[0]["is_error"] != true {
		t.Errorf("is_error: want true, got %v", out[0]["is_error"])
	}

	// FromAnthropic: is_error:true in content block should set ToolResult.IsError
	content := []map[string]any{
		{"type": "tool_result", "tool_use_id": "t1", "content": "err", "is_error": true},
	}
	results := ToolResultsFromAnthropic(content)
	if len(results) != 1 {
		t.Fatalf("want 1, got %d", len(results))
	}
	if !results[0].IsError {
		t.Errorf("IsError: want true, got false")
	}
}

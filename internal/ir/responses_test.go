package ir

import (
	"encoding/json"
	"errors"
	"testing"
)

func decodeChatBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("output is not valid JSON: %v (%s)", err, raw)
	}
	return m
}

func TestChatRequestFromResponses_SimpleTextTurn(t *testing.T) {
	req := `{
		"model": "gpt-test",
		"instructions": "you are a helpful assistant",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"say PONG"}]}
		],
		"stream": true
	}`
	out, err := ChatRequestFromResponses([]byte(req))
	if err != nil {
		t.Fatalf("ChatRequestFromResponses: %v", err)
	}
	body := decodeChatBody(t, out)

	if body["model"] != "gpt-test" {
		t.Errorf("model = %v, want gpt-test", body["model"])
	}
	if body["stream"] != true {
		t.Errorf("stream = %v, want true", body["stream"])
	}
	so, ok := body["stream_options"].(map[string]any)
	if !ok || so["include_usage"] != true {
		t.Errorf("stream_options.include_usage = %v, want true (else billing goes silently to 0)", body["stream_options"])
	}

	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Fatalf("messages = %v, want 2 entries (system + user)", body["messages"])
	}
	sys := msgs[0].(map[string]any)
	if sys["role"] != "system" || sys["content"] != "you are a helpful assistant" {
		t.Errorf("messages[0] = %v, want system/instructions", sys)
	}
	user := msgs[1].(map[string]any)
	if user["role"] != "user" || user["content"] != "say PONG" {
		t.Errorf("messages[1] = %v, want user/'say PONG'", user)
	}
}

func TestChatRequestFromResponses_DeveloperRoleMapsToSystem(t *testing.T) {
	req := `{"model":"m","input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"be terse"}]}]}`
	out, err := ChatRequestFromResponses([]byte(req))
	if err != nil {
		t.Fatalf("ChatRequestFromResponses: %v", err)
	}
	body := decodeChatBody(t, out)
	msgs := body["messages"].([]any)
	first := msgs[0].(map[string]any)
	if first["role"] != "system" {
		t.Errorf("developer role mapped to %v, want system (Chat 端点普遍不认 developer)", first["role"])
	}
}

func TestChatRequestFromResponses_EmptyInput_ErrEmptyInput(t *testing.T) {
	// cc-switch issue #2806: input 为空时绝不能让上游收到 messages: null.
	cases := []string{
		`{"model":"m","input":[]}`,
		`{"model":"m","input":[{"type":"message","role":"user","content":[]}]}`,
		`{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:..."}]}]}`,
	}
	for _, req := range cases {
		_, err := ChatRequestFromResponses([]byte(req))
		if !errors.Is(err, ErrEmptyInput) {
			t.Errorf("req %s: err = %v, want ErrEmptyInput", req, err)
		}
	}
}

func TestChatRequestFromResponses_ImagePartSkippedNotStuffedIntoText(t *testing.T) {
	// cc-switch issue #5663: base64 image parts must not get concatenated
	// into the text field and replayed as tool text.
	req := `{"model":"m","input":[{"type":"message","role":"user","content":[
		{"type":"input_image","image_url":"data:image/png;base64,AAAA"},
		{"type":"input_text","text":"describe this"}
	]}]}`
	out, err := ChatRequestFromResponses([]byte(req))
	if err != nil {
		t.Fatalf("ChatRequestFromResponses: %v", err)
	}
	body := decodeChatBody(t, out)
	msgs := body["messages"].([]any)
	user := msgs[0].(map[string]any)
	if user["content"] != "describe this" {
		t.Errorf("content = %q, want just the text part (image part must be skipped, not stuffed in)", user["content"])
	}
}

func TestChatRequestFromResponses_FunctionCallRoundTrip(t *testing.T) {
	req := `{"model":"m","input":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"run echo"}]},
		{"type":"function_call","call_id":"call_1","name":"bash","arguments":"{\"cmd\":\"echo hi\"}"},
		{"type":"function_call_output","call_id":"call_1","output":"hi\n"}
	]}`
	out, err := ChatRequestFromResponses([]byte(req))
	if err != nil {
		t.Fatalf("ChatRequestFromResponses: %v", err)
	}
	body := decodeChatBody(t, out)
	msgs := body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %v, want 3 entries", msgs)
	}

	assistant := msgs[1].(map[string]any)
	if assistant["role"] != "assistant" {
		t.Errorf("messages[1].role = %v, want assistant", assistant["role"])
	}
	tcs, ok := assistant["tool_calls"].([]any)
	if !ok || len(tcs) != 1 {
		t.Fatalf("messages[1].tool_calls = %v, want 1 entry", assistant["tool_calls"])
	}
	tc := tcs[0].(map[string]any)
	if tc["id"] != "call_1" {
		t.Errorf("tool_calls[0].id = %v, want call_1", tc["id"])
	}
	fn := tc["function"].(map[string]any)
	if fn["arguments"] != `{"cmd":"echo hi"}` {
		t.Errorf("tool_calls[0].function.arguments = %v, want the raw JSON string unchanged", fn["arguments"])
	}
	if _, isString := fn["arguments"].(string); !isString {
		t.Errorf("arguments must stay a JSON string, not be decoded into an object")
	}

	toolMsg := msgs[2].(map[string]any)
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "call_1" || toolMsg["content"] != "hi\n" {
		t.Errorf("messages[2] = %v, want tool/call_1/'hi\\n'", toolMsg)
	}
}

func TestChatRequestFromResponses_ToolsFlattened(t *testing.T) {
	// Responses 的 tools[] 是扁平的（type/name/description/parameters 同级），
	// Chat 的是嵌套在 function 下的 —— 这是最容易弄反的一处。
	req := `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
		"tools":[{"type":"function","name":"bash","description":"run a shell command","parameters":{"type":"object"}}]}`
	out, err := ChatRequestFromResponses([]byte(req))
	if err != nil {
		t.Fatalf("ChatRequestFromResponses: %v", err)
	}
	body := decodeChatBody(t, out)
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v, want 1 entry", body["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "function" {
		t.Errorf("tools[0].type = %v, want function", tool["type"])
	}
	fn, ok := tool["function"].(map[string]any)
	if !ok {
		t.Fatalf("tools[0].function missing — flattened Responses shape wasn't nested for Chat: %v", tool)
	}
	if fn["name"] != "bash" || fn["description"] != "run a shell command" {
		t.Errorf("tools[0].function = %v, want name=bash description set", fn)
	}
}

func TestChatRequestFromResponses_MaxOutputTokensAndTemperature(t *testing.T) {
	temp := float32(0.4)
	req := ResponsesRequest{
		Model:           "m",
		Input:           []ResponsesItem{{Type: RespItemMessage, Role: "user", Content: []ResponsesPart{{Type: "input_text", Text: "hi"}}}},
		MaxOutputTokens: 256,
		Temperature:     &temp,
	}
	raw, _ := json.Marshal(req)
	out, err := ChatRequestFromResponses(raw)
	if err != nil {
		t.Fatalf("ChatRequestFromResponses: %v", err)
	}
	body := decodeChatBody(t, out)
	if body["max_tokens"] != float64(256) {
		t.Errorf("max_tokens = %v, want 256", body["max_tokens"])
	}
	if body["temperature"] != float64(0.4) {
		t.Errorf("temperature = %v, want 0.4", body["temperature"])
	}
}

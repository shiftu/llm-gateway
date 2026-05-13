package ir

import (
	"encoding/json"
	"testing"
)

// types_test.go round-trips the IR types against real DeepSeek fixtures from
// the 2026-05-13 protocol spike (see docs/spikes/2026-05-13-deepseek-protocols.md).
//
// Design decision per plan R2: IR JSON tags follow the Anthropic Messages
// wire format because Anthropic is the more expressive superset. This means
// `json.Unmarshal(anthropic_body, &ir.Request{})` works without any
// translation; OpenAI wire format requires a custom marshaller, which lives
// in Task 2's provider adapter, not in this package.

// Captured verbatim from spike F3 / Anthropic-blocking smoke output.
const deepseekAnthropicBlockingResponse = `{
  "id": "msg_a1",
  "type": "message",
  "role": "assistant",
  "model": "deepseek-v4-flash",
  "content": [
    {"type": "thinking", "thinking": "We need to respond.", "signature": "sig123"},
    {"type": "text", "text": "Hello there world."}
  ],
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": {
    "input_tokens": 10,
    "cache_creation_input_tokens": 0,
    "cache_read_input_tokens": 0,
    "output_tokens": 177
  }
}`

func TestResponse_AnthropicFixture_Unmarshal(t *testing.T) {
	var r Response
	if err := json.Unmarshal([]byte(deepseekAnthropicBlockingResponse), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if r.ID != "msg_a1" {
		t.Errorf("ID: want msg_a1, got %q", r.ID)
	}
	if r.Model != "deepseek-v4-flash" {
		t.Errorf("Model: want deepseek-v4-flash, got %q", r.Model)
	}
	if len(r.Content) != 2 {
		t.Fatalf("Content: want 2 blocks, got %d", len(r.Content))
	}
	if r.Content[0].Type != BlockTypeThinking {
		t.Errorf("Content[0].Type: want %q, got %q", BlockTypeThinking, r.Content[0].Type)
	}
	if r.Content[0].Thinking != "We need to respond." {
		t.Errorf("Content[0].Thinking: got %q", r.Content[0].Thinking)
	}
	if r.Content[0].Signature != "sig123" {
		t.Errorf("Content[0].Signature: got %q", r.Content[0].Signature)
	}
	if r.Content[1].Type != BlockTypeText {
		t.Errorf("Content[1].Type: want %q, got %q", BlockTypeText, r.Content[1].Type)
	}
	if r.Content[1].Text != "Hello there world." {
		t.Errorf("Content[1].Text: got %q", r.Content[1].Text)
	}
	if r.StopReason != StopReasonEndTurn {
		t.Errorf("StopReason: want %q, got %q", StopReasonEndTurn, r.StopReason)
	}
	if r.Usage.InputTokens != 10 {
		t.Errorf("Usage.InputTokens: want 10, got %d", r.Usage.InputTokens)
	}
	if r.Usage.OutputTokens != 177 {
		t.Errorf("Usage.OutputTokens: want 177, got %d", r.Usage.OutputTokens)
	}
}

// Captured from spike F3 / Anthropic-streaming smoke output (first 4 events).
const deepseekAnthropicStreamEvents = `{"type":"message_start","message":{"id":"msg_s1","type":"message","role":"assistant","model":"deepseek-v4-flash","usage":{"input_tokens":6,"output_tokens":0}}}
{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}
{"type":"ping"}
{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"We are"}}
{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"hi"}}
{"type":"content_block_stop","index":1}
{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}
{"type":"message_stop"}`

func TestStreamChunk_AnthropicEvents_Unmarshal(t *testing.T) {
	lines := splitLines(deepseekAnthropicStreamEvents)
	if len(lines) != 8 {
		t.Fatalf("expected 8 fixture lines, got %d", len(lines))
	}

	expectedTypes := []string{
		StreamEventMessageStart,
		StreamEventContentBlockStart,
		StreamEventPing,
		StreamEventContentBlockDelta,
		StreamEventContentBlockDelta,
		StreamEventContentBlockStop,
		StreamEventMessageDelta,
		StreamEventMessageStop,
	}

	for i, line := range lines {
		var c StreamChunk
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatalf("line %d unmarshal: %v\n%s", i, err, line)
		}
		if c.Type != expectedTypes[i] {
			t.Errorf("line %d Type: want %q, got %q", i, expectedTypes[i], c.Type)
		}
	}

	// Spot check the substantive fields on chunks we know carry data.
	var firstDelta StreamChunk
	_ = json.Unmarshal([]byte(lines[3]), &firstDelta)
	if firstDelta.BlockIndex != 0 || firstDelta.Delta.Type != "thinking_delta" || firstDelta.Delta.Thinking != "We are" {
		t.Errorf("thinking delta: %+v", firstDelta)
	}

	var textDelta StreamChunk
	_ = json.Unmarshal([]byte(lines[4]), &textDelta)
	if textDelta.BlockIndex != 1 || textDelta.Delta.Type != "text_delta" || textDelta.Delta.Text != "hi" {
		t.Errorf("text delta: %+v", textDelta)
	}

	var msgDelta StreamChunk
	_ = json.Unmarshal([]byte(lines[6]), &msgDelta)
	if msgDelta.Delta.StopReason != StopReasonEndTurn {
		t.Errorf("message_delta stop_reason: want %q, got %q", StopReasonEndTurn, msgDelta.Delta.StopReason)
	}
	if msgDelta.Usage == nil || msgDelta.Usage.OutputTokens != 42 {
		t.Errorf("message_delta usage: %+v", msgDelta.Usage)
	}
}

// Anthropic Messages POST body shape — top-level system field, multi-block
// content per message.
const anthropicRequestBody = `{
  "model": "deepseek-v4-flash",
  "system": "You are concise.",
  "messages": [
    {"role": "user", "content": [{"type": "text", "text": "hi"}]}
  ],
  "max_tokens": 100,
  "stream": false
}`

func TestRequest_AnthropicFixture_Unmarshal(t *testing.T) {
	var req Request
	if err := json.Unmarshal([]byte(anthropicRequestBody), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if req.Model != "deepseek-v4-flash" {
		t.Errorf("Model: %q", req.Model)
	}
	if req.System != "You are concise." {
		t.Errorf("System (top-level): %q", req.System)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("Messages: want 1, got %d", len(req.Messages))
	}
	if req.Messages[0].Role != "user" {
		t.Errorf("Messages[0].Role: %q", req.Messages[0].Role)
	}
	if len(req.Messages[0].Content) != 1 || req.Messages[0].Content[0].Type != BlockTypeText {
		t.Errorf("Messages[0].Content: %+v", req.Messages[0].Content)
	}
	if req.Messages[0].Content[0].Text != "hi" {
		t.Errorf("Messages[0].Content[0].Text: %q", req.Messages[0].Content[0].Text)
	}
	if req.MaxTokens != 100 {
		t.Errorf("MaxTokens: %d", req.MaxTokens)
	}
	if req.Stream {
		t.Errorf("Stream: want false")
	}
}

// MessageString accepts either a plain string or an array of content blocks
// for the `content` field of a Message — Anthropic accepts both shapes.
// Plain-string variant must be normalised to a single text content block on
// the way in.
func TestMessage_PlainStringContent_Normalised(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"plain string"}],"model":"x"}`
	var req Request
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(req.Messages) != 1 || len(req.Messages[0].Content) != 1 {
		t.Fatalf("want 1 message with 1 content block, got %+v", req.Messages)
	}
	if req.Messages[0].Content[0].Type != BlockTypeText || req.Messages[0].Content[0].Text != "plain string" {
		t.Errorf("plain-string content not normalised: %+v", req.Messages[0].Content[0])
	}
}

// splitLines is a tiny helper that splits on '\n' and trims empty trailing.
func splitLines(s string) []string {
	out := []string{}
	cur := ""
	for _, r := range s {
		if r == '\n' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

package ir

import (
	"encoding/json"
	"reflect"
	"testing"
)

// ── Reasoning.Empty ──────────────────────────────────────────────────────────

func TestReasoning_Empty(t *testing.T) {
	tests := []struct {
		name string
		r    Reasoning
		want bool
	}{
		{"zero value", Reasoning{}, true},
		{"content only", Reasoning{Content: "hello"}, false},
		{"tokens only", Reasoning{Tokens: 10}, false},
		{"blocks only", Reasoning{Blocks: []ContentBlock{{Type: BlockTypeThinking, Thinking: "t"}}}, false},
		{"all set", Reasoning{Content: "x", Tokens: 5, Blocks: []ContentBlock{{Type: BlockTypeThinking}}}, false},
		{"empty content empty blocks zero tokens", Reasoning{Content: "", Tokens: 0, Blocks: nil}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.r.Empty()
			if got != tt.want {
				t.Errorf("Empty() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ── ExtractReasoningOpenAI ───────────────────────────────────────────────────

func TestExtractReasoningOpenAI(t *testing.T) {
	t.Run("reasoning_content in message only", func(t *testing.T) {
		body := []byte(`{
			"choices": [{"message": {"reasoning_content": "I think step by step...", "content": "The answer is 42."}}],
			"usage": {}
		}`)
		r := ExtractReasoningOpenAI(body)
		if r.Content != "I think step by step..." {
			t.Errorf("Content: want %q, got %q", "I think step by step...", r.Content)
		}
		if r.Tokens != 0 {
			t.Errorf("Tokens: want 0, got %d", r.Tokens)
		}
	})

	t.Run("reasoning_tokens via completion_tokens_details", func(t *testing.T) {
		body := []byte(`{
			"choices": [{"message": {"content": "answer"}}],
			"usage": {"completion_tokens_details": {"reasoning_tokens": 150}}
		}`)
		r := ExtractReasoningOpenAI(body)
		if r.Tokens != 150 {
			t.Errorf("Tokens: want 150, got %d", r.Tokens)
		}
		if r.Content != "" {
			t.Errorf("Content: want empty, got %q", r.Content)
		}
	})

	t.Run("reasoning_tokens directly on usage (DeepSeek)", func(t *testing.T) {
		body := []byte(`{
			"choices": [{"message": {"reasoning_content": "thinking...", "content": "result"}}],
			"usage": {"reasoning_tokens": 150}
		}`)
		r := ExtractReasoningOpenAI(body)
		if r.Content != "thinking..." {
			t.Errorf("Content: want %q, got %q", "thinking...", r.Content)
		}
		if r.Tokens != 150 {
			t.Errorf("Tokens: want 150, got %d", r.Tokens)
		}
	})

	t.Run("both reasoning_content and completion_tokens_details", func(t *testing.T) {
		body := []byte(`{
			"choices": [{"message": {"reasoning_content": "step by step", "content": "final"}}],
			"usage": {"completion_tokens_details": {"reasoning_tokens": 200}}
		}`)
		r := ExtractReasoningOpenAI(body)
		if r.Content != "step by step" {
			t.Errorf("Content: want %q, got %q", "step by step", r.Content)
		}
		if r.Tokens != 200 {
			t.Errorf("Tokens: want 200, got %d", r.Tokens)
		}
	})

	t.Run("neither reasoning content nor tokens", func(t *testing.T) {
		body := []byte(`{
			"choices": [{"message": {"content": "plain answer"}}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 5}
		}`)
		r := ExtractReasoningOpenAI(body)
		if !r.Empty() {
			t.Errorf("want empty Reasoning, got %+v", r)
		}
	})

	t.Run("invalid JSON returns zero value", func(t *testing.T) {
		r := ExtractReasoningOpenAI([]byte(`not json`))
		if !r.Empty() {
			t.Errorf("want empty Reasoning on bad JSON, got %+v", r)
		}
	})

	t.Run("empty choices returns zero value", func(t *testing.T) {
		body := []byte(`{"choices": [], "usage": {}}`)
		r := ExtractReasoningOpenAI(body)
		if !r.Empty() {
			t.Errorf("want empty Reasoning on empty choices, got %+v", r)
		}
	})
}

// ── ExtractReasoningAnthropic ────────────────────────────────────────────────

func TestExtractReasoningAnthropic(t *testing.T) {
	t.Run("thinking blocks present", func(t *testing.T) {
		body := []byte(`{
			"content": [
				{"type": "thinking", "thinking": "I reason through this.", "signature": "sig123"},
				{"type": "text", "text": "The answer."}
			]
		}`)
		r := ExtractReasoningAnthropic(body)
		if r.Content != "I reason through this." {
			t.Errorf("Content: want %q, got %q", "I reason through this.", r.Content)
		}
		if len(r.Blocks) != 1 {
			t.Fatalf("Blocks: want 1, got %d", len(r.Blocks))
		}
		if r.Blocks[0].Type != BlockTypeThinking {
			t.Errorf("Blocks[0].Type: want %q, got %q", BlockTypeThinking, r.Blocks[0].Type)
		}
		if r.Blocks[0].Thinking != "I reason through this." {
			t.Errorf("Blocks[0].Thinking: want %q, got %q", "I reason through this.", r.Blocks[0].Thinking)
		}
		if r.Blocks[0].Signature != "sig123" {
			t.Errorf("Blocks[0].Signature: want %q, got %q", "sig123", r.Blocks[0].Signature)
		}
	})

	t.Run("no thinking blocks", func(t *testing.T) {
		body := []byte(`{
			"content": [
				{"type": "text", "text": "Just text."}
			]
		}`)
		r := ExtractReasoningAnthropic(body)
		if !r.Empty() {
			t.Errorf("want empty Reasoning, got %+v", r)
		}
	})

	t.Run("multiple thinking blocks concatenated", func(t *testing.T) {
		body := []byte(`{
			"content": [
				{"type": "thinking", "thinking": "First thought.", "signature": "s1"},
				{"type": "thinking", "thinking": " Second thought.", "signature": "s2"},
				{"type": "text", "text": "Done."}
			]
		}`)
		r := ExtractReasoningAnthropic(body)
		if r.Content != "First thought. Second thought." {
			t.Errorf("Content: want %q, got %q", "First thought. Second thought.", r.Content)
		}
		if len(r.Blocks) != 2 {
			t.Errorf("Blocks: want 2, got %d", len(r.Blocks))
		}
	})

	t.Run("empty content array", func(t *testing.T) {
		body := []byte(`{"content": []}`)
		r := ExtractReasoningAnthropic(body)
		if !r.Empty() {
			t.Errorf("want empty Reasoning, got %+v", r)
		}
	})

	t.Run("invalid JSON returns zero value", func(t *testing.T) {
		r := ExtractReasoningAnthropic([]byte(`{bad}`))
		if !r.Empty() {
			t.Errorf("want empty Reasoning on bad JSON, got %+v", r)
		}
	})

	t.Run("usage reasoning tokens included", func(t *testing.T) {
		body := []byte(`{
			"content": [
				{"type": "thinking", "thinking": "some thinking", "signature": "sigX"}
			],
			"usage": {"input_tokens": 10, "output_tokens": 20, "reasoning_tokens": 80}
		}`)
		r := ExtractReasoningAnthropic(body)
		if r.Tokens != 80 {
			t.Errorf("Tokens: want 80, got %d", r.Tokens)
		}
	})
}

// ── ExtractReasoningDeltaOpenAI ───────────────────────────────────────────────

func TestExtractReasoningDeltaOpenAI(t *testing.T) {
	t.Run("reasoning_content delta chunk", func(t *testing.T) {
		chunk := []byte(`{"choices":[{"delta":{"reasoning_content":"step 1..."}}]}`)
		content, tokens, ok := ExtractReasoningDeltaOpenAI(chunk)
		if !ok {
			t.Fatal("ok: want true")
		}
		if content != "step 1..." {
			t.Errorf("content: want %q, got %q", "step 1...", content)
		}
		if tokens != 0 {
			t.Errorf("tokens: want 0, got %d", tokens)
		}
	})

	t.Run("usage chunk with completion_tokens_details.reasoning_tokens", func(t *testing.T) {
		chunk := []byte(`{"usage":{"completion_tokens_details":{"reasoning_tokens":150}}}`)
		content, tokens, ok := ExtractReasoningDeltaOpenAI(chunk)
		if !ok {
			t.Fatal("ok: want true")
		}
		if content != "" {
			t.Errorf("content: want empty, got %q", content)
		}
		if tokens != 150 {
			t.Errorf("tokens: want 150, got %d", tokens)
		}
	})

	t.Run("usage chunk with reasoning_tokens directly", func(t *testing.T) {
		chunk := []byte(`{"usage":{"reasoning_tokens":75}}`)
		_, tokens, ok := ExtractReasoningDeltaOpenAI(chunk)
		if !ok {
			t.Fatal("ok: want true")
		}
		if tokens != 75 {
			t.Errorf("tokens: want 75, got %d", tokens)
		}
	})

	t.Run("non-reasoning delta (content only)", func(t *testing.T) {
		chunk := []byte(`{"choices":[{"delta":{"content":"hello world"}}]}`)
		content, tokens, ok := ExtractReasoningDeltaOpenAI(chunk)
		if ok {
			t.Error("ok: want false for non-reasoning chunk")
		}
		if content != "" || tokens != 0 {
			t.Errorf("want zero content/tokens, got content=%q tokens=%d", content, tokens)
		}
	})

	t.Run("invalid JSON returns false", func(t *testing.T) {
		_, _, ok := ExtractReasoningDeltaOpenAI([]byte(`invalid`))
		if ok {
			t.Error("ok: want false for invalid JSON")
		}
	})

	t.Run("empty choices returns false", func(t *testing.T) {
		chunk := []byte(`{"choices":[]}`)
		_, _, ok := ExtractReasoningDeltaOpenAI(chunk)
		if ok {
			t.Error("ok: want false for empty choices")
		}
	})
}

// ── ExtractReasoningDeltaAnthropic ────────────────────────────────────────────

func TestExtractReasoningDeltaAnthropic(t *testing.T) {
	t.Run("content_block_start with thinking type", func(t *testing.T) {
		chunk := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`)
		block, ok := ExtractReasoningDeltaAnthropic(chunk)
		if !ok {
			t.Fatal("ok: want true")
		}
		if block.Type != BlockTypeThinking {
			t.Errorf("Type: want %q, got %q", BlockTypeThinking, block.Type)
		}
		if block.Thinking != "" {
			t.Errorf("Thinking: want empty, got %q", block.Thinking)
		}
	})

	t.Run("content_block_delta with thinking_delta type", func(t *testing.T) {
		chunk := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"step 1..."}}`)
		block, ok := ExtractReasoningDeltaAnthropic(chunk)
		if !ok {
			t.Fatal("ok: want true")
		}
		if block.Type != BlockTypeThinking {
			t.Errorf("Type: want %q, got %q", BlockTypeThinking, block.Type)
		}
		if block.Thinking != "step 1..." {
			t.Errorf("Thinking: want %q, got %q", "step 1...", block.Thinking)
		}
	})

	t.Run("content_block_delta with text_delta type returns false", func(t *testing.T) {
		chunk := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`)
		_, ok := ExtractReasoningDeltaAnthropic(chunk)
		if ok {
			t.Error("ok: want false for text_delta")
		}
	})

	t.Run("content_block_start with text type returns false", func(t *testing.T) {
		chunk := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		_, ok := ExtractReasoningDeltaAnthropic(chunk)
		if ok {
			t.Error("ok: want false for text content_block_start")
		}
	})

	t.Run("message_start returns false", func(t *testing.T) {
		chunk := []byte(`{"type":"message_start","message":{"id":"m1","model":"x"}}`)
		_, ok := ExtractReasoningDeltaAnthropic(chunk)
		if ok {
			t.Error("ok: want false for message_start")
		}
	})

	t.Run("invalid JSON returns false", func(t *testing.T) {
		_, ok := ExtractReasoningDeltaAnthropic([]byte(`{nope}`))
		if ok {
			t.Error("ok: want false for invalid JSON")
		}
	})
}

// ── EmitReasoningOpenAI ───────────────────────────────────────────────────────

func TestEmitReasoningOpenAI(t *testing.T) {
	t.Run("injects reasoning_content key", func(t *testing.T) {
		r := Reasoning{Content: "my reasoning", Tokens: 42}
		dest := map[string]any{"content": "the answer"}
		EmitReasoningOpenAI(r, dest)
		got, ok := dest["reasoning_content"]
		if !ok {
			t.Fatal("reasoning_content key missing from dest")
		}
		if got != "my reasoning" {
			t.Errorf("reasoning_content: want %q, got %v", "my reasoning", got)
		}
		// original content key still present
		if dest["content"] != "the answer" {
			t.Errorf("content key modified unexpectedly: %v", dest["content"])
		}
	})

	t.Run("no-op on empty Reasoning", func(t *testing.T) {
		r := Reasoning{}
		dest := map[string]any{"content": "answer"}
		EmitReasoningOpenAI(r, dest)
		if _, ok := dest["reasoning_content"]; ok {
			t.Error("reasoning_content should not be injected for empty Reasoning")
		}
	})

	t.Run("no-op on nil dest is safe", func(t *testing.T) {
		// Should not panic; just return early
		r := Reasoning{Content: "x"}
		defer func() {
			if rec := recover(); rec != nil {
				t.Errorf("panicked: %v", rec)
			}
		}()
		EmitReasoningOpenAI(r, nil)
	})
}

// ── EmitReasoningAnthropicBlocks ──────────────────────────────────────────────

func TestEmitReasoningAnthropicBlocks(t *testing.T) {
	t.Run("returns thinking blocks from Reasoning", func(t *testing.T) {
		blocks := []ContentBlock{
			{Type: BlockTypeThinking, Thinking: "thought 1", Signature: "sig1"},
			{Type: BlockTypeThinking, Thinking: "thought 2", Signature: "sig2"},
		}
		r := Reasoning{Content: "thought 1thought 2", Tokens: 20, Blocks: blocks}
		got := EmitReasoningAnthropicBlocks(r)
		if !reflect.DeepEqual(got, blocks) {
			t.Errorf("blocks mismatch:\ngot  %+v\nwant %+v", got, blocks)
		}
	})

	t.Run("nil on empty Reasoning", func(t *testing.T) {
		got := EmitReasoningAnthropicBlocks(Reasoning{})
		if got != nil {
			t.Errorf("want nil, got %+v", got)
		}
	})

	t.Run("nil when Blocks empty but Content set", func(t *testing.T) {
		// Content-only Reasoning has no blocks to emit (flat OpenAI format)
		r := Reasoning{Content: "some thinking"}
		got := EmitReasoningAnthropicBlocks(r)
		// With no Blocks set, returns nil
		if got != nil {
			t.Errorf("want nil when Blocks is nil, got %+v", got)
		}
	})
}

// ── round-trip sanity: JSON marshal/unmarshal of Reasoning.Blocks ────────────

func TestReasoning_BlocksJSONRoundTrip(t *testing.T) {
	r := Reasoning{
		Content: "my thinking",
		Tokens:  55,
		Blocks: []ContentBlock{
			{Type: BlockTypeThinking, Thinking: "my thinking", Signature: "sigABC"},
		},
	}
	data, err := json.Marshal(r.Blocks)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var blocks []ContentBlock
	if err := json.Unmarshal(data, &blocks); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Thinking != "my thinking" || blocks[0].Signature != "sigABC" {
		t.Errorf("round-trip mismatch: %+v", blocks)
	}
}

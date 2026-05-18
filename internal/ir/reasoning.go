package ir

import "encoding/json"

// Reasoning holds the extracted reasoning/thinking content from a provider
// response. Content is the flat concatenated text (suitable for OpenAI
// emission); Blocks are the structured thinking blocks (suitable for Anthropic
// emission); Tokens is the reported reasoning token count.
type Reasoning struct {
	Content string         // flat concatenated reasoning text (for OpenAI emission)
	Tokens  int            // reasoning token count
	Blocks  []ContentBlock // structured thinking blocks (for Anthropic emission)
}

// Empty reports whether there is no reasoning content.
func (r Reasoning) Empty() bool {
	return r.Content == "" && len(r.Blocks) == 0 && r.Tokens == 0
}

// ── OpenAI / DeepSeek blocking ────────────────────────────────────────────────

// openAIBlockingResponse is the minimal shape we care about for reasoning
// extraction from an OpenAI-format blocking response.
type openAIBlockingResponse struct {
	Choices []struct {
		Message struct {
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		ReasoningTokens          int `json:"reasoning_tokens"`
		CompletionTokensDetails  struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

// ExtractReasoningOpenAI parses an OpenAI-format blocking response body and
// returns the reasoning content. Returns zero Reasoning when none is present.
func ExtractReasoningOpenAI(body []byte) Reasoning {
	var resp openAIBlockingResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return Reasoning{}
	}

	var content string
	if len(resp.Choices) > 0 {
		content = resp.Choices[0].Message.ReasoningContent
	}

	// Prefer completion_tokens_details.reasoning_tokens; fall back to
	// top-level usage.reasoning_tokens (DeepSeek direct format).
	tokens := resp.Usage.CompletionTokensDetails.ReasoningTokens
	if tokens == 0 {
		tokens = resp.Usage.ReasoningTokens
	}

	if content == "" && tokens == 0 {
		return Reasoning{}
	}
	return Reasoning{Content: content, Tokens: tokens}
}

// ── Anthropic blocking ────────────────────────────────────────────────────────

// anthropicBlockingResponse is the minimal shape for reasoning extraction from
// an Anthropic-format blocking response.
type anthropicBlockingResponse struct {
	Content []ContentBlock `json:"content"`
	Usage   struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"usage"`
}

// ExtractReasoningAnthropic parses an Anthropic-format blocking response body
// and returns the reasoning content extracted from thinking content blocks.
func ExtractReasoningAnthropic(body []byte) Reasoning {
	var resp anthropicBlockingResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return Reasoning{}
	}

	var blocks []ContentBlock
	var content string
	for _, b := range resp.Content {
		if b.Type == BlockTypeThinking {
			blocks = append(blocks, b)
			content += b.Thinking
		}
	}

	if len(blocks) == 0 && resp.Usage.ReasoningTokens == 0 {
		return Reasoning{}
	}
	return Reasoning{
		Content: content,
		Tokens:  resp.Usage.ReasoningTokens,
		Blocks:  blocks,
	}
}

// ── OpenAI / DeepSeek streaming ───────────────────────────────────────────────

// openAIStreamChunk is the minimal shape for extracting reasoning from a single
// OpenAI SSE data chunk.
type openAIStreamChunk struct {
	Choices []struct {
		Delta struct {
			ReasoningContent string `json:"reasoning_content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		ReasoningTokens         int `json:"reasoning_tokens"`
		CompletionTokensDetails *struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

// ExtractReasoningDeltaOpenAI extracts the reasoning_content delta from a
// single OpenAI SSE data chunk. Returns ("", 0, false) when not a reasoning
// chunk. tokens is non-zero only on the final usage chunk.
func ExtractReasoningDeltaOpenAI(chunk []byte) (content string, tokens int, ok bool) {
	var c openAIStreamChunk
	if err := json.Unmarshal(chunk, &c); err != nil {
		return "", 0, false
	}

	// Check for reasoning delta in choices.
	if len(c.Choices) > 0 && c.Choices[0].Delta.ReasoningContent != "" {
		return c.Choices[0].Delta.ReasoningContent, 0, true
	}

	// Check for usage chunk with reasoning token count.
	if c.Usage != nil {
		t := 0
		if c.Usage.CompletionTokensDetails != nil {
			t = c.Usage.CompletionTokensDetails.ReasoningTokens
		}
		if t == 0 {
			t = c.Usage.ReasoningTokens
		}
		if t > 0 {
			return "", t, true
		}
	}

	return "", 0, false
}

// ── Anthropic streaming ───────────────────────────────────────────────────────

// anthropicStreamChunk is the minimal shape for extracting thinking deltas
// from a single Anthropic SSE data chunk.
type anthropicStreamChunk struct {
	Type         string `json:"type"`
	ContentBlock *struct {
		Type      string `json:"type"`
		Thinking  string `json:"thinking"`
		Signature string `json:"signature"`
	} `json:"content_block"`
	Delta *struct {
		Type     string `json:"type"`
		Thinking string `json:"thinking"`
	} `json:"delta"`
}

// ExtractReasoningDeltaAnthropic extracts a thinking delta from a single
// Anthropic SSE data chunk. Returns (ContentBlock{}, false) when not a
// thinking chunk.
// For content_block_start with type=thinking, returns a block with empty Thinking.
// For content_block_delta with type=thinking_delta, returns the delta text.
func ExtractReasoningDeltaAnthropic(chunk []byte) (ContentBlock, bool) {
	var c anthropicStreamChunk
	if err := json.Unmarshal(chunk, &c); err != nil {
		return ContentBlock{}, false
	}

	switch c.Type {
	case StreamEventContentBlockStart:
		if c.ContentBlock != nil && c.ContentBlock.Type == BlockTypeThinking {
			return ContentBlock{
				Type:      BlockTypeThinking,
				Thinking:  c.ContentBlock.Thinking,
				Signature: c.ContentBlock.Signature,
			}, true
		}
	case StreamEventContentBlockDelta:
		if c.Delta != nil && c.Delta.Type == StreamDeltaTypeThinking {
			return ContentBlock{
				Type:     BlockTypeThinking,
				Thinking: c.Delta.Thinking,
			}, true
		}
	}

	return ContentBlock{}, false
}

// ── Emitters ──────────────────────────────────────────────────────────────────

// EmitReasoningOpenAI injects reasoning content into an OpenAI-format message
// map (the "message" object inside choices[0]). Mutates dest in place.
// No-op when r.Empty() or dest is nil.
func EmitReasoningOpenAI(r Reasoning, dest map[string]any) {
	if r.Empty() || dest == nil {
		return
	}
	dest["reasoning_content"] = r.Content
}

// EmitReasoningAnthropicBlocks returns the thinking content blocks for use in
// an Anthropic-format response's content array. Returns nil when r.Empty() or
// when r.Blocks is nil.
func EmitReasoningAnthropicBlocks(r Reasoning) []ContentBlock {
	if r.Empty() || len(r.Blocks) == 0 {
		return nil
	}
	return r.Blocks
}

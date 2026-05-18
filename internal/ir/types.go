// Package ir defines the gateway's internal request/response/stream
// representation. Per plan R2 the IR JSON tags follow the Anthropic Messages
// wire format because Anthropic is the more expressive superset of the two
// supported inbound protocols (Anthropic Messages + OpenAI Chat Completions).
//
// Consequence: Anthropic-shaped JSON parses directly into IR types with
// `json.Unmarshal`, no parser needed. OpenAI-shaped JSON requires explicit
// conversion in the provider adapter layer (Task 2/3) — those conversions
// also handle reasoning_content ↔ thinking block field rename and
// prompt_tokens ↔ input_tokens usage field rename.
//
// IR is a data carrier, not a validator. Wire-format validation lives at the
// HTTP handler boundary (Task 6 / 6.5).
package ir

import "encoding/json"

// Block-type discriminator strings. Match Anthropic Messages content-block
// `type` field exactly so they round-trip through JSON without translation.
const (
	BlockTypeText       = "text"
	BlockTypeThinking   = "thinking"
	BlockTypeToolUse    = "tool_use"
	BlockTypeToolResult = "tool_result"
)

// StopReason values. Anthropic-canonical. OpenAI's `finish_reason` maps
// 1:1 in the provider adapter (stop→end_turn, length→max_tokens, etc.).
const (
	StopReasonEndTurn      = "end_turn"
	StopReasonMaxTokens    = "max_tokens"
	StopReasonStopSequence = "stop_sequence"
	StopReasonToolUse      = "tool_use"
)

// Stream event types. Match Anthropic Messages SSE event names exactly so
// pass-through (R2) emits bytes verbatim. OpenAI streams collapse most of
// these into a single flat `delta` chunk in the adapter.
const (
	StreamEventMessageStart      = "message_start"
	StreamEventContentBlockStart = "content_block_start"
	StreamEventContentBlockDelta = "content_block_delta"
	StreamEventContentBlockStop  = "content_block_stop"
	StreamEventMessageDelta      = "message_delta"
	StreamEventMessageStop       = "message_stop"
	StreamEventPing              = "ping"
	StreamEventError             = "error"
)

// Stream delta types (sub-discriminator inside content_block_delta events).
const (
	StreamDeltaTypeText     = "text_delta"
	StreamDeltaTypeThinking = "thinking_delta"
	StreamDeltaTypeToolUse  = "input_json_delta"
)

// Request is the gateway's normalised inbound request shape. Both OpenAI
// Chat Completions and Anthropic Messages POST bodies parse into this.
type Request struct {
	Model       string         `json:"model"`
	System      string         `json:"system,omitempty"`
	Messages    []Message      `json:"messages"`
	Tools       []Tool         `json:"tools,omitempty"`
	Stream      bool           `json:"stream,omitempty"`
	MaxTokens   int            `json:"max_tokens,omitempty"`
	Temperature *float32       `json:"temperature,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// Message wraps a conversation turn. Wire-level `content` accepts either a
// plain string or a typed-block array; UnmarshalJSON normalises both into
// the array form.
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// UnmarshalJSON normalises plain-string `content` into a single
// BlockTypeText block. This matches both OpenAI's flat `content: "text"`
// convention and Anthropic's shorthand for user messages.
func (m *Message) UnmarshalJSON(data []byte) error {
	var aux struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	m.Role = aux.Role
	if len(aux.Content) == 0 {
		return nil
	}

	// Try plain string first — most common shorthand for user messages.
	var s string
	if err := json.Unmarshal(aux.Content, &s); err == nil {
		m.Content = []ContentBlock{{Type: BlockTypeText, Text: s}}
		return nil
	}

	return json.Unmarshal(aux.Content, &m.Content)
}

// ContentBlock is a single typed block inside Message.Content or
// Response.Content. Per spike F1: thinking blocks carry the model's hidden
// chain-of-thought plus an opaque `signature` Anthropic uses for integrity.
type ContentBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text,omitempty"`
	Thinking     string        `json:"thinking,omitempty"`
	Signature    string        `json:"signature,omitempty"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
	ToolUse      *ToolUse      `json:"-"` // exposed via type=tool_use; flattened on (un)marshal in adapter
	ToolResult   *ToolResult   `json:"-"` // exposed via type=tool_result; flattened in adapter
}

// Tool, ToolUse, ToolResult mirror Anthropic Messages shapes. Tool-use
// pass-through is v0.1 scope; richer cross-protocol tool semantics deferred
// per plan Q10.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

type ToolUse struct {
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input,omitempty"`
}

type ToolResult struct {
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

// Response is the blocking inbound response shape. Fields align with
// Anthropic Messages JSON. ID and Model surface to request_logs for
// observability (plan F-19, Task 6).
type Response struct {
	ID           string         `json:"id"`
	Type         string         `json:"type,omitempty"`
	Role         string         `json:"role,omitempty"`
	Model        string         `json:"model"`
	Content      []ContentBlock `json:"content"`
	StopReason   string         `json:"stop_reason,omitempty"`
	StopSequence *string        `json:"stop_sequence,omitempty"`
	Usage        Usage          `json:"usage"`
}

// Usage is the union of OpenAI + Anthropic token-accounting fields per
// spike F6. Adapters drop fields that don't apply to their wire format.
type Usage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	ReasoningTokens     int `json:"reasoning_tokens,omitempty"`
	CacheReadTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationTokens int `json:"cache_creation_input_tokens,omitempty"`
}

// StreamChunk carries any one Anthropic Messages SSE event. The Type field
// discriminates which other fields are populated. OpenAI streams are
// translated into this shape by the openai adapter (Task 2/6).
type StreamChunk struct {
	Type         string        `json:"type"`
	BlockIndex   int           `json:"index,omitempty"`
	Delta        StreamDelta   `json:"delta,omitempty"`
	ContentBlock *ContentBlock `json:"content_block,omitempty"`
	Message      *Response     `json:"message,omitempty"`
	Usage        *Usage        `json:"usage,omitempty"`
}

// StreamDelta is the sub-payload of content_block_delta and message_delta
// events. Type discriminates text vs thinking vs stop-reason variants.
type StreamDelta struct {
	Type       string `json:"type,omitempty"`
	Text       string `json:"text,omitempty"`
	Thinking   string `json:"thinking,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
}

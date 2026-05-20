package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/panda/llm-gateway/internal/store"
)

// capturedUsage holds token counts extracted from an upstream response.
// All fields are zero when the response body is unparseable or omits usage.
type capturedUsage struct {
	InputTokens     int
	OutputTokens    int
	ReasoningTokens int
}

// parseBlockingUsage extracts token counts from a complete JSON response body.
// Returns zero-value on any parse failure — callers log zeros rather than errors.
func parseBlockingUsage(body []byte, protocol string) capturedUsage {
	switch protocol {
	case protocolOpenAI:
		var resp struct {
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				ReasoningTokens  int `json:"reasoning_tokens"` // DeepSeek direct field
				Details          struct {
					ReasoningTokens int `json:"reasoning_tokens"`
				} `json:"completion_tokens_details"`
			} `json:"usage"`
		}
		if json.Unmarshal(body, &resp) != nil {
			return capturedUsage{}
		}
		rt := resp.Usage.ReasoningTokens
		if rt == 0 {
			rt = resp.Usage.Details.ReasoningTokens
		}
		return capturedUsage{
			InputTokens:     resp.Usage.PromptTokens,
			OutputTokens:    resp.Usage.CompletionTokens,
			ReasoningTokens: rt,
		}
	case protocolAnthropic:
		var resp struct {
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(body, &resp) != nil {
			return capturedUsage{}
		}
		return capturedUsage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
		}
	}
	return capturedUsage{}
}

// applySSEChunkUsage merges token counts from a single parsed SSE data payload
// into u. Each field is updated independently so split-usage providers (those
// that report prompt and completion tokens in separate chunks) do not clobber
// each other's values.
func applySSEChunkUsage(u *capturedUsage, data []byte, protocol string) {
	switch protocol {
	case protocolAnthropic:
		var chunk struct {
			Type    string `json:"type"`
			Message struct {
				Usage struct {
					InputTokens int `json:"input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Usage *struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(data, &chunk) != nil {
			return
		}
		switch chunk.Type {
		case "message_start":
			u.InputTokens = chunk.Message.Usage.InputTokens
		case "message_delta":
			if chunk.Usage != nil {
				u.OutputTokens = chunk.Usage.OutputTokens
			}
		}
	case protocolOpenAI:
		var chunk struct {
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				ReasoningTokens  int `json:"reasoning_tokens"`
				Details          struct {
					ReasoningTokens int `json:"reasoning_tokens"`
				} `json:"completion_tokens_details"`
			} `json:"usage"`
		}
		if json.Unmarshal(data, &chunk) != nil || chunk.Usage == nil {
			return
		}
		if chunk.Usage.PromptTokens > 0 {
			u.InputTokens = chunk.Usage.PromptTokens
		}
		if chunk.Usage.CompletionTokens > 0 {
			u.OutputTokens = chunk.Usage.CompletionTokens
		}
		rt := chunk.Usage.ReasoningTokens
		if rt == 0 {
			rt = chunk.Usage.Details.ReasoningTokens
		}
		if rt > 0 {
			u.ReasoningTokens = rt
		}
	}
}

// parseSSEUsage scans a complete SSE byte stream and accumulates token counts.
// Used in tests and non-streaming analysis paths; streaming requests should
// call applySSEChunkUsage inline to avoid buffering the full response body.
func parseSSEUsage(sse []byte, protocol string) capturedUsage {
	var u capturedUsage
	sc := bufio.NewScanner(bytes.NewReader(sse))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		applySSEChunkUsage(&u, []byte(data), protocol)
	}
	return u
}

// calcCostMicros returns the estimated cost in microdollars (1 USD = 1,000,000
// micros) using the most recent model_costs row with effective_from ≤ now.
// Returns 0 when no pricing row exists — missing cost is not a fatal condition.
func calcCostMicros(st *store.Store, providerName, model string, u capturedUsage) int64 {
	mc, err := st.GetModelCost(providerName, model, time.Now())
	if errors.Is(err, store.ErrNotFound) || err != nil {
		return 0
	}
	in := int64(float64(u.InputTokens) / 1000.0 * mc.USDPerInput1k * 1_000_000)
	out := int64(float64(u.OutputTokens) / 1000.0 * mc.USDPerOutput1k * 1_000_000)
	var reasoning int64
	if u.ReasoningTokens > 0 {
		rate := mc.USDPerOutput1k // fallback: reasoning billed at output rate
		if mc.USDPerReasoning1k != nil {
			rate = *mc.USDPerReasoning1k
		}
		reasoning = int64(float64(u.ReasoningTokens) / 1000.0 * rate * 1_000_000)
	}
	return in + out + reasoning
}

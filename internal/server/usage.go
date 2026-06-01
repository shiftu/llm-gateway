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
	// CachedTokens is the portion of InputTokens that hit the upstream
	// prompt cache. 0 when the provider does not report cached_tokens or
	// the model lacks prompt-caching support. Always <= InputTokens after
	// calcCostMicros clamps.
	CachedTokens int
}

// parseBlockingUsage extracts token counts from a complete JSON response body.
// Returns zero-value on any parse failure — callers log zeros rather than errors.
func parseBlockingUsage(body []byte, protocol string) capturedUsage {
	switch protocol {
	case protocolOpenAI:
		var resp struct {
			Usage struct {
				PromptTokens        int `json:"prompt_tokens"`
				CompletionTokens    int `json:"completion_tokens"`
				ReasoningTokens     int `json:"reasoning_tokens"` // DeepSeek direct field
				PromptTokensDetails struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
				Details struct {
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
			CachedTokens:    resp.Usage.PromptTokensDetails.CachedTokens,
		}
	case protocolAnthropic:
		var resp struct {
			Usage struct {
				InputTokens         int `json:"input_tokens"`
				OutputTokens        int `json:"output_tokens"`
				CacheReadInputToks  int `json:"cache_read_input_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(body, &resp) != nil {
			return capturedUsage{}
		}
		return capturedUsage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
			CachedTokens: resp.Usage.CacheReadInputToks,
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
					InputTokens        int `json:"input_tokens"`
					CacheReadInputToks int `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Usage *struct {
				OutputTokens       int `json:"output_tokens"`
				CacheReadInputToks int `json:"cache_read_input_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(data, &chunk) != nil {
			return
		}
		switch chunk.Type {
		case "message_start":
			u.InputTokens = chunk.Message.Usage.InputTokens
			if chunk.Message.Usage.CacheReadInputToks > 0 {
				u.CachedTokens = chunk.Message.Usage.CacheReadInputToks
			}
		case "message_delta":
			if chunk.Usage != nil {
				u.OutputTokens = chunk.Usage.OutputTokens
				if chunk.Usage.CacheReadInputToks > 0 {
					u.CachedTokens = chunk.Usage.CacheReadInputToks
				}
			}
		}
	case protocolOpenAI:
		var chunk struct {
			Usage *struct {
				PromptTokens        int `json:"prompt_tokens"`
				CompletionTokens    int `json:"completion_tokens"`
				ReasoningTokens     int `json:"reasoning_tokens"`
				PromptTokensDetails struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
				Details struct {
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
		if chunk.Usage.PromptTokensDetails.CachedTokens > 0 {
			u.CachedTokens = chunk.Usage.PromptTokensDetails.CachedTokens
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
//
// Cache pricing: when CachedTokens > 0 the input bill is split — cached
// tokens use USDPerCached1k (typically ~10% of input rate), the remaining
// (InputTokens - CachedTokens) bill at the full input rate. If the model has
// no USDPerCached1k configured, cached tokens fall back to the input rate so
// the total is unchanged from the pre-v11 formula.
func calcCostMicros(st *store.Store, providerName, model string, u capturedUsage) int64 {
	mc, err := st.GetModelCost(providerName, model, time.Now())
	if errors.Is(err, store.ErrNotFound) || err != nil {
		return 0
	}
	cached := u.CachedTokens
	if cached > u.InputTokens {
		cached = u.InputTokens // defensive: upstream sometimes reports cached > prompt
	}
	uncached := u.InputTokens - cached

	in := int64(float64(uncached) / 1000.0 * mc.USDPerInput1k * 1_000_000)
	cachedRate := mc.USDPerInput1k
	if mc.USDPerCached1k != nil {
		cachedRate = *mc.USDPerCached1k
	}
	inCached := int64(float64(cached) / 1000.0 * cachedRate * 1_000_000)
	out := int64(float64(u.OutputTokens) / 1000.0 * mc.USDPerOutput1k * 1_000_000)
	var reasoning int64
	if u.ReasoningTokens > 0 {
		rate := mc.USDPerOutput1k // fallback: reasoning billed at output rate
		if mc.USDPerReasoning1k != nil {
			rate = *mc.USDPerReasoning1k
		}
		reasoning = int64(float64(u.ReasoningTokens) / 1000.0 * rate * 1_000_000)
	}
	return in + inCached + out + reasoning
}

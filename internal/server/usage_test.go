package server

import (
	"testing"
	"time"

	"github.com/panda/llm-gateway/internal/store"
)

// --- parseBlockingUsage ---

func TestParseBlockingUsage_OpenAI(t *testing.T) {
	body := []byte(`{
		"id":"chatcmpl-1",
		"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}
	}`)
	u := parseBlockingUsage(body, protocolOpenAI)
	if u.InputTokens != 10 || u.OutputTokens != 20 || u.ReasoningTokens != 0 {
		t.Errorf("got %+v, want {10 20 0}", u)
	}
}

func TestParseBlockingUsage_OpenAI_ReasoningDirect(t *testing.T) {
	body := []byte(`{"usage":{"prompt_tokens":5,"completion_tokens":15,"reasoning_tokens":8}}`)
	u := parseBlockingUsage(body, protocolOpenAI)
	if u.ReasoningTokens != 8 {
		t.Errorf("expected reasoning_tokens=8, got %d", u.ReasoningTokens)
	}
}

func TestParseBlockingUsage_OpenAI_ReasoningInDetails(t *testing.T) {
	body := []byte(`{"usage":{"prompt_tokens":5,"completion_tokens":15,"completion_tokens_details":{"reasoning_tokens":12}}}`)
	u := parseBlockingUsage(body, protocolOpenAI)
	if u.ReasoningTokens != 12 {
		t.Errorf("expected reasoning_tokens=12 from details, got %d", u.ReasoningTokens)
	}
}

func TestParseBlockingUsage_Anthropic(t *testing.T) {
	body := []byte(`{
		"id":"msg-1","type":"message","role":"assistant",
		"content":[{"type":"text","text":"hi"}],
		"usage":{"input_tokens":7,"output_tokens":14}
	}`)
	u := parseBlockingUsage(body, protocolAnthropic)
	if u.InputTokens != 7 || u.OutputTokens != 14 {
		t.Errorf("got %+v, want {7 14 0}", u)
	}
}

func TestParseBlockingUsage_BadJSON_ReturnsZero(t *testing.T) {
	u := parseBlockingUsage([]byte(`not json`), protocolOpenAI)
	if u.InputTokens != 0 || u.OutputTokens != 0 {
		t.Errorf("bad JSON should return zero usage, got %+v", u)
	}
}

func TestParseBlockingUsage_UnknownProtocol_ReturnsZero(t *testing.T) {
	body := []byte(`{"usage":{"prompt_tokens":99}}`)
	u := parseBlockingUsage(body, "unknown")
	if u.InputTokens != 0 {
		t.Errorf("unknown protocol should return zero usage, got %+v", u)
	}
}

// --- parseSSEUsage ---

func TestParseSSEUsage_Anthropic(t *testing.T) {
	sse := []byte(
		"event: message_start\n" +
			`data: {"type":"message_start","message":{"id":"msg-1","usage":{"input_tokens":25}}}` + "\n\n" +
			"event: content_block_start\n" +
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
			"event: message_delta\n" +
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":40}}` + "\n\n" +
			"event: message_stop\n" +
			`data: {"type":"message_stop"}` + "\n\n",
	)
	u := parseSSEUsage(sse, protocolAnthropic)
	if u.InputTokens != 25 || u.OutputTokens != 40 {
		t.Errorf("got %+v, want {25 40 0}", u)
	}
}

func TestParseSSEUsage_OpenAI(t *testing.T) {
	sse := []byte(
		"data: " + `{"choices":[{"delta":{"content":"he"}}],"usage":null}` + "\n\n" +
			"data: " + `{"choices":[{"delta":{"content":"llo"}}],"usage":null}` + "\n\n" +
			"data: " + `{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":3}}` + "\n\n" +
			"data: [DONE]\n\n",
	)
	u := parseSSEUsage(sse, protocolOpenAI)
	if u.InputTokens != 8 || u.OutputTokens != 3 {
		t.Errorf("got %+v, want {8 3 0}", u)
	}
}

func TestParseSSEUsage_OpenAI_WithReasoning(t *testing.T) {
	sse := []byte(
		"data: " + `{"choices":[{"delta":{"content":"x"}}],"usage":null}` + "\n\n" +
			"data: " + `{"choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":50,"reasoning_tokens":30}}` + "\n\n" +
			"data: [DONE]\n\n",
	)
	u := parseSSEUsage(sse, protocolOpenAI)
	if u.InputTokens != 10 || u.OutputTokens != 50 || u.ReasoningTokens != 30 {
		t.Errorf("got %+v, want {10 50 30}", u)
	}
}

func TestParseSSEUsage_StopsAtDone(t *testing.T) {
	sse := []byte(
		"data: " + `{"type":"message_start","message":{"usage":{"input_tokens":5}}}` + "\n\n" +
			"data: [DONE]\n\n" +
			// lines after [DONE] must be ignored
			"data: " + `{"type":"message_delta","usage":{"output_tokens":999}}` + "\n\n",
	)
	u := parseSSEUsage(sse, protocolAnthropic)
	if u.OutputTokens == 999 {
		t.Error("output_tokens after [DONE] should be ignored")
	}
}

func TestParseSSEUsage_EmptyStream_ReturnsZero(t *testing.T) {
	u := parseSSEUsage([]byte(""), protocolAnthropic)
	if u.InputTokens != 0 || u.OutputTokens != 0 {
		t.Errorf("empty stream should return zero usage, got %+v", u)
	}
}

// --- calcCostMicros ---

func openUsageTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCalcCostMicros_BasicRate(t *testing.T) {
	st := openUsageTestStore(t)
	_ = st.SetModelCost(store.ModelCost{
		Provider:       "deepseek",
		Model:          "deepseek-v4-flash",
		USDPerInput1k:  0.001,
		USDPerOutput1k: 0.002,
		EffectiveFrom:  time.Now().Add(-time.Hour),
	})
	u := capturedUsage{InputTokens: 1000, OutputTokens: 2000}
	got := calcCostMicros(st, "deepseek", "deepseek-v4-flash", u)
	// 1000 input × $0.001/1k = $0.001 = 1000 micros
	// 2000 output × $0.002/1k = $0.004 = 4000 micros
	// total = 5000 micros
	if got != 5000 {
		t.Errorf("expected 5000 micros, got %d", got)
	}
}

func TestCalcCostMicros_NoPricing_ReturnsZero(t *testing.T) {
	st := openUsageTestStore(t)
	u := capturedUsage{InputTokens: 100, OutputTokens: 200}
	got := calcCostMicros(st, "unknown-provider", "unknown-model", u)
	if got != 0 {
		t.Errorf("missing pricing should return 0, got %d", got)
	}
}

func TestCalcCostMicros_ReasoningSpecialRate(t *testing.T) {
	st := openUsageTestStore(t)
	reasonRate := 0.010
	_ = st.SetModelCost(store.ModelCost{
		Provider:          "deepseek",
		Model:             "deepseek-v4-flash",
		USDPerInput1k:     0.001,
		USDPerOutput1k:    0.002,
		USDPerReasoning1k: &reasonRate,
		EffectiveFrom:     time.Now().Add(-time.Hour),
	})
	u := capturedUsage{InputTokens: 0, OutputTokens: 0, ReasoningTokens: 1000}
	got := calcCostMicros(st, "deepseek", "deepseek-v4-flash", u)
	// 1000 reasoning × $0.010/1k = $0.010 = 10000 micros
	if got != 10000 {
		t.Errorf("expected 10000 micros for reasoning at special rate, got %d", got)
	}
}

func TestCalcCostMicros_ReasoningFallsBackToOutputRate(t *testing.T) {
	st := openUsageTestStore(t)
	_ = st.SetModelCost(store.ModelCost{
		Provider:       "glm",
		Model:          "glm-4-plus",
		USDPerInput1k:  0.001,
		USDPerOutput1k: 0.004,
		EffectiveFrom:  time.Now().Add(-time.Hour),
	})
	u := capturedUsage{InputTokens: 0, OutputTokens: 0, ReasoningTokens: 1000}
	got := calcCostMicros(st, "glm", "glm-4-plus", u)
	// 1000 reasoning × $0.004/1k (output fallback) = $0.004 = 4000 micros
	if got != 4000 {
		t.Errorf("expected 4000 micros (fallback to output rate), got %d", got)
	}
}

func TestCalcCostMicros_ZeroTokens_ReturnsZero(t *testing.T) {
	st := openUsageTestStore(t)
	_ = st.SetModelCost(store.ModelCost{
		Provider:       "deepseek",
		Model:          "deepseek-v4-flash",
		USDPerInput1k:  0.001,
		USDPerOutput1k: 0.002,
		EffectiveFrom:  time.Now().Add(-time.Hour),
	})
	got := calcCostMicros(st, "deepseek", "deepseek-v4-flash", capturedUsage{})
	if got != 0 {
		t.Errorf("zero tokens should yield zero cost, got %d", got)
	}
}

// --- cached_tokens parsing (v11) ---

func TestParseBlockingUsage_OpenAI_CachedTokens(t *testing.T) {
	body := []byte(`{"usage":{"prompt_tokens":1000,"completion_tokens":50,"prompt_tokens_details":{"cached_tokens":800}}}`)
	u := parseBlockingUsage(body, protocolOpenAI)
	if u.InputTokens != 1000 || u.OutputTokens != 50 || u.CachedTokens != 800 {
		t.Errorf("got %+v, want {Input:1000 Output:50 Cached:800}", u)
	}
}

func TestParseBlockingUsage_Anthropic_CacheRead(t *testing.T) {
	body := []byte(`{"usage":{"input_tokens":1200,"output_tokens":80,"cache_read_input_tokens":900}}`)
	u := parseBlockingUsage(body, protocolAnthropic)
	if u.InputTokens != 1200 || u.OutputTokens != 80 || u.CachedTokens != 900 {
		t.Errorf("got %+v, want {Input:1200 Output:80 Cached:900}", u)
	}
}

func TestParseSSEUsage_OpenAI_CachedTokens(t *testing.T) {
	sse := []byte(
		"data: " + `{"choices":[{"delta":{"content":"x"}}],"usage":null}` + "\n\n" +
			"data: " + `{"choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":500,"completion_tokens":40,"prompt_tokens_details":{"cached_tokens":300}}}` + "\n\n" +
			"data: [DONE]\n\n",
	)
	u := parseSSEUsage(sse, protocolOpenAI)
	if u.InputTokens != 500 || u.OutputTokens != 40 || u.CachedTokens != 300 {
		t.Errorf("got %+v, want {Input:500 Output:40 Cached:300}", u)
	}
}

func TestParseSSEUsage_Anthropic_CacheReadInMessageStart(t *testing.T) {
	sse := []byte(
		`data: {"type":"message_start","message":{"usage":{"input_tokens":600,"cache_read_input_tokens":450}}}` + "\n\n" +
			`data: {"type":"message_delta","usage":{"output_tokens":30}}` + "\n\n",
	)
	u := parseSSEUsage(sse, protocolAnthropic)
	if u.InputTokens != 600 || u.OutputTokens != 30 || u.CachedTokens != 450 {
		t.Errorf("got %+v, want {Input:600 Output:30 Cached:450}", u)
	}
}

// --- calcCostMicros cache pricing (v11) ---

func TestCalcCostMicros_CachedAtDiscountedRate(t *testing.T) {
	st := openUsageTestStore(t)
	cachedRate := 0.0001
	_ = st.SetModelCost(store.ModelCost{
		Provider:       "charaboard-gemini35-flash",
		Model:          "Google: Gemini 3.5 Flash",
		USDPerInput1k:  0.001,
		USDPerOutput1k: 0.002,
		USDPerCached1k: &cachedRate,
		EffectiveFrom:  time.Now().Add(-time.Hour),
	})
	u := capturedUsage{InputTokens: 1000, OutputTokens: 0, CachedTokens: 800}
	got := calcCostMicros(st, "charaboard-gemini35-flash", "Google: Gemini 3.5 Flash", u)
	// uncached: 200 × 0.001 / 1000 = $0.0002 = 200 micros
	// cached:   800 × 0.0001 / 1000 = $0.00008 = 80 micros
	// total = 280 micros
	if got != 280 {
		t.Errorf("expected 280 micros, got %d", got)
	}
}

func TestCalcCostMicros_CachedFallsBackToInputRate(t *testing.T) {
	st := openUsageTestStore(t)
	_ = st.SetModelCost(store.ModelCost{
		Provider:       "deepseek",
		Model:          "deepseek-v4-flash",
		USDPerInput1k:  0.001,
		USDPerOutput1k: 0.002,
		EffectiveFrom:  time.Now().Add(-time.Hour),
	})
	u := capturedUsage{InputTokens: 1000, OutputTokens: 0, CachedTokens: 500}
	got := calcCostMicros(st, "deepseek", "deepseek-v4-flash", u)
	// No USDPerCached1k → cached billed at input rate, same as the pre-v11 formula
	// 1000 × 0.001 / 1000 = 1000 micros
	if got != 1000 {
		t.Errorf("expected 1000 micros (cached falls back to input rate), got %d", got)
	}
}

func TestCalcCostMicros_CachedClampedToInputTokens(t *testing.T) {
	st := openUsageTestStore(t)
	cachedRate := 0.0001
	_ = st.SetModelCost(store.ModelCost{
		Provider:       "charaboard-gpt52",
		Model:          "OpenAI: GPT-5.2 Chat",
		USDPerInput1k:  0.001,
		USDPerOutput1k: 0.002,
		USDPerCached1k: &cachedRate,
		EffectiveFrom:  time.Now().Add(-time.Hour),
	})
	// Defensive case: upstream reports cached=999 with input=500.
	// Clamp to input → all 500 priced at cached rate, uncached=0.
	u := capturedUsage{InputTokens: 500, OutputTokens: 0, CachedTokens: 999}
	got := calcCostMicros(st, "charaboard-gpt52", "OpenAI: GPT-5.2 Chat", u)
	// 500 × 0.0001 / 1000 = 50 micros (no negative uncached charge)
	if got != 50 {
		t.Errorf("expected 50 micros (cached clamped), got %d", got)
	}
}

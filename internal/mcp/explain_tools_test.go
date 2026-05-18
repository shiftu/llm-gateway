package mcp

import (
	"strings"
	"testing"

	"github.com/panda/llm-gateway/internal/store"
)

// TestExplainRouteTrace_Cognitive: a request logged with a cognitive route_trace
// returns a formatted breakdown showing candidates, scores, and the winner.
func TestExplainRouteTrace_Cognitive(t *testing.T) {
	st := openTestStore(t)

	// Store a log with a realistic cognitive trace JSON.
	trace := `{"Weights":{"Cost":0.4,"Latency":0.3,"Quality":0.2,"Health":0.1},` +
		`"Candidates":[` +
		`{"ProviderName":"glm","Inputs":{"InputCostUSD1k":0.001,"OutputCostUSD1k":0.002,"P99LatencyMs":100,"Quality":1,"Healthy":true},"Breakdown":{"CostScore":333.333,"LatencyScore":0.01,"QualityScore":0.2,"HealthScore":0.1,"Total":133.477}},` +
		`{"ProviderName":"deepseek","Inputs":{"InputCostUSD1k":0.01,"OutputCostUSD1k":0.02,"P99LatencyMs":100,"Quality":1,"Healthy":true},"Breakdown":{"CostScore":33.333,"LatencyScore":0.01,"QualityScore":0.2,"HealthScore":0.1,"Total":13.477}}` +
		`]}`
	id, err := st.LogRequest(store.RequestLog{
		ClientModel:   "smart",
		ResolvedModel: "smart-v3",
		ProviderName:  "glm",
		Status:        "ok",
		RouteTrace:    trace,
	})
	if err != nil {
		t.Fatalf("LogRequest: %v", err)
	}

	h := explainRouteTraceHandler(st)
	res, err := h(ctxWithScope("mcp_auditor"), callTool(map[string]any{"request_id": float64(id)}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", textOf(t, res))
	}

	body := textOf(t, res)
	for _, want := range []string{"glm", "deepseek", "winner", "cost", "total"} {
		if !strings.Contains(strings.ToLower(body), want) {
			t.Errorf("output should mention %q; got:\n%s", want, body)
		}
	}
}

// TestExplainRouteTrace_Static: a request that used static routing has an empty
// route_trace — the tool must return a clear "static routing" message rather
// than failing or returning empty output.
func TestExplainRouteTrace_Static(t *testing.T) {
	st := openTestStore(t)

	id, err := st.LogRequest(store.RequestLog{
		ClientModel:   "fast",
		ResolvedModel: "deepseek-v3",
		ProviderName:  "deepseek",
		Status:        "ok",
	})
	if err != nil {
		t.Fatalf("LogRequest: %v", err)
	}

	h := explainRouteTraceHandler(st)
	res, err := h(ctxWithScope("mcp_auditor"), callTool(map[string]any{"request_id": float64(id)}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %v", textOf(t, res))
	}

	body := textOf(t, res)
	if !strings.Contains(strings.ToLower(body), "static") {
		t.Errorf("expected 'static' in output for static route; got %q", body)
	}
}

// TestExplainRouteTrace_NotFound: an unknown request_id returns a tool-level
// error — no panic, no silent empty result.
func TestExplainRouteTrace_NotFound(t *testing.T) {
	st := openTestStore(t)

	h := explainRouteTraceHandler(st)
	res, err := h(ctxWithScope("mcp_auditor"), callTool(map[string]any{"request_id": int64(9999)}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected tool error for unknown request_id, got: %v", textOf(t, res))
	}
}

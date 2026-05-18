package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/panda/llm-gateway/internal/router"
	"github.com/panda/llm-gateway/internal/store"
)

// TestServer_CognitiveRoute_RecordsRouteTrace: a request resolved via a
// cognitive alias must persist a non-empty route_trace in request_logs.
// This is the persistence contract for the explain_route_trace MCP tool.
func TestServer_CognitiveRoute_RecordsRouteTrace(t *testing.T) {
	upstream := &mockUpstream{
		respCT:   "application/json",
		respBody: `{"id":"up-1","choices":[{"message":{"content":"ok"}}]}`,
	}
	upSrv := httptest.NewServer(upstream)
	defer upSrv.Close()

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	for _, name := range []string{"cheap", "pricey"} {
		if err := st.AddProvider(store.Provider{
			Name: name, Kind: "deepseek",
			OpenAIBaseURL: upSrv.URL, APIKey: "k-" + name,
		}); err != nil {
			t.Fatalf("AddProvider(%s): %v", name, err)
		}
	}
	if err := st.SetAliasWithMode("smart", "cheap", "smart-v3", nil, nil, "cognitive"); err != nil {
		t.Fatalf("SetAliasWithMode: %v", err)
	}

	srv := NewServer("tok", st)
	// Inject a scorer so the cognitive path actually runs.
	srv.router = router.NewWithScorer(st, func(p store.Provider) router.ScoreInputs {
		if p.Name == "cheap" {
			return router.ScoreInputs{InputCostUSD1k: 0.001, OutputCostUSD1k: 0.001, P99LatencyMs: 50, Quality: 1, Healthy: true}
		}
		return router.ScoreInputs{InputCostUSD1k: 0.01, OutputCostUSD1k: 0.01, P99LatencyMs: 100, Quality: 1, Healthy: true}
	})

	gw := httptest.NewServer(srv.Handler())
	defer gw.Close()

	req, _ := http.NewRequest("POST", gw.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"smart","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: want 200, got %d", resp.StatusCode)
	}

	// Fetch the stored log via TailLogs (for ID) then GetRequestLog (for RouteTrace).
	logs, err := st.TailLogs(1)
	if err != nil || len(logs) == 0 {
		t.Fatalf("TailLogs: %v (got %d rows)", err, len(logs))
	}
	full, err := st.GetRequestLog(logs[0].ID)
	if err != nil {
		t.Fatalf("GetRequestLog: %v", err)
	}
	if full.RouteTrace == "" {
		t.Errorf("RouteTrace: want non-empty JSON trace for cognitive route, got empty")
	}
}

// TestServer_StaticRoute_NoRouteTrace: a static alias must NOT store a trace
// (empty string), preserving backwards-compatible log behaviour.
func TestServer_StaticRoute_NoRouteTrace(t *testing.T) {
	upstream := &mockUpstream{
		respCT:   "application/json",
		respBody: `{"id":"up-2","choices":[{"message":{"content":"ok"}}]}`,
	}
	upSrv := httptest.NewServer(upstream)
	defer upSrv.Close()

	st := newStoreWithDefaultProvider(t, "deepseek", storeOpts{openaiURL: upSrv.URL})
	srv := NewServer("tok", st)

	gw := httptest.NewServer(srv.Handler())
	defer gw.Close()

	req, _ := http.NewRequest("POST", gw.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	logs, err := st.TailLogs(1)
	if err != nil || len(logs) == 0 {
		t.Fatalf("TailLogs: %v (got %d rows)", err, len(logs))
	}
	full, err := st.GetRequestLog(logs[0].ID)
	if err != nil {
		t.Fatalf("GetRequestLog: %v", err)
	}
	if full.RouteTrace != "" {
		t.Errorf("RouteTrace: want empty for static route, got %q", full.RouteTrace)
	}
}

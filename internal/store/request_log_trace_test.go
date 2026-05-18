package store

import (
	"testing"
)

// TestRequestLog_RouteTracePersists: a cognitive route's trace JSON survives
// a LogRequest → GetRequestLog round-trip. This is the persistence contract
// that the explain_route_trace MCP tool depends on.
func TestRequestLog_RouteTracePersists(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	trace := `{"Weights":{"Cost":0.4,"Latency":0.3},"Candidates":[{"ProviderName":"glm"}]}`
	id, err := st.LogRequest(RequestLog{
		ClientModel:   "smart",
		ResolvedModel: "smart-v3",
		ProviderName:  "glm",
		Status:        "ok",
		RouteTrace:    trace,
	})
	if err != nil {
		t.Fatalf("LogRequest: %v", err)
	}

	got, err := st.GetRequestLog(id)
	if err != nil {
		t.Fatalf("GetRequestLog: %v", err)
	}
	if got.RouteTrace != trace {
		t.Errorf("RouteTrace: want %q, got %q", trace, got.RouteTrace)
	}
}

// TestRequestLog_EmptyRouteTrace: static routes store an empty trace, and
// GetRequestLog returns an empty string (not a SQL NULL artifact).
func TestRequestLog_EmptyRouteTrace(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	id, err := st.LogRequest(RequestLog{
		ClientModel:   "fast",
		ResolvedModel: "deepseek-v3",
		ProviderName:  "deepseek",
		Status:        "ok",
	})
	if err != nil {
		t.Fatalf("LogRequest: %v", err)
	}

	got, err := st.GetRequestLog(id)
	if err != nil {
		t.Fatalf("GetRequestLog: %v", err)
	}
	if got.RouteTrace != "" {
		t.Errorf("RouteTrace: want empty, got %q", got.RouteTrace)
	}
}

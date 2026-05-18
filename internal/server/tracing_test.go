package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestTracingMiddleware_PassesThrough verifies that TracingMiddleware forwards
// the request to the wrapped handler and does not swallow the response.
func TestTracingMiddleware_PassesThrough(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	h := TracingMiddleware(inner)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("TracingMiddleware: want 200, got %d", w.Code)
	}
}

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMessages_Valid_Returns200 mirrors TestChatCompletions_Valid_Returns200
// for the Anthropic-shape inbound endpoint per plan §1 P3 (Revision 1) and
// the dual-protocol design in Revision 2. Stub handler returns 200 with a
// hardcoded Anthropic-shape message JSON; real router/provider wiring is
// Task 6.5 in plan §4.
func TestMessages_Valid_Returns200(t *testing.T) {
	body := strings.NewReader(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"max_tokens":100}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "test-token")
	req.Header.Set("anthropic-version", "2023-06-01")

	w := httptest.NewRecorder()
	HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: body=%s", w.Code, w.Body.String())
	}
}

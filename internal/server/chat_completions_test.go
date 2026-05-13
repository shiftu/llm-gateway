package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestChatCompletions_Valid_Returns200 is the RED step of TDD for the OpenAI
// chat-completions stub handler. Passing this test means: a syntactically
// valid OpenAI-shape POST with a Bearer token returns HTTP 200.
//
// Stub behavior only — no upstream provider, no router, no SSE. Task 1
// scope per plan §4: "minimal handler that echoes back a stub completion."
func TestChatCompletions_Valid_Returns200(t *testing.T) {
	body := strings.NewReader(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")

	w := httptest.NewRecorder()
	HandleChatCompletions(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: body=%s", w.Code, w.Body.String())
	}
}

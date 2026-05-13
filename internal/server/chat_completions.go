// Package server hosts the inbound HTTP handlers — both OpenAI Chat Completions
// (/v1/chat/completions) and Anthropic Messages (/v1/messages) per plan §1 P3
// and Revision 1.
//
// Task 1 scope: stub handlers only. No router, no provider dispatch, no
// streaming. Just enough for the bearer-auth middleware and JSON-shape
// validation to be testable. Real upstream wiring lands in Task 6 / 6.5.
package server

import (
	"encoding/json"
	"net/http"
)

// HandleChatCompletions is the OpenAI-shape inbound endpoint stub. Plan
// §4 Task 1 spec: returns a hardcoded 200 chat-completion JSON to prove the
// pipe is wired. No upstream call. Replaced in Task 6 with router + provider
// dispatch.
func HandleChatCompletions(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{
		"id":      "stub-0001",
		"object":  "chat.completion",
		"created": 0,
		"model":   "stub",
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": "stub response from llm-gateway",
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]int{
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"total_tokens":      0,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

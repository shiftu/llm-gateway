package server

import (
	"encoding/json"
	"net/http"
)

// HandleMessages is the Anthropic Messages API stub handler — the second
// inbound protocol per plan §1 P3 Revision 1. Same Task-1 scope as
// HandleChatCompletions: a 200 with a hardcoded Anthropic-shape response,
// no upstream call. Real wiring is Task 6.5.
//
// Shape mirrors what DeepSeek's Anthropic endpoint returns (see
// docs/spikes/2026-05-13-deepseek-protocols.md F1): top-level message
// object with `content[]` of typed blocks, `stop_reason`, and Anthropic-
// style `usage` (input_tokens / output_tokens).
func HandleMessages(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{
		"id":    "msg_stub_0001",
		"type":  "message",
		"role":  "assistant",
		"model": "stub",
		"content": []map[string]any{
			{
				"type": "text",
				"text": "stub response from llm-gateway",
			},
		},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage": map[string]int{
			"input_tokens":  0,
			"output_tokens": 0,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

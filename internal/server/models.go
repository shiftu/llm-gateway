package server

import (
	"encoding/json"
	"net/http"
	"time"

	authpkg "github.com/panda/llm-gateway/internal/auth"
	"github.com/panda/llm-gateway/internal/store"
)

// handleModels implements GET /v1/models returning an OpenAI-compatible model
// list built from registered aliases.
//
// Visibility rules:
//   - Global aliases (team_id IS NULL) are always included.
//   - Team-scoped aliases are added when the request carries an lgw_ API key;
//     they override global aliases that share the same name so callers never
//     see duplicate IDs.
//
// Stub mode (no store) returns an empty list rather than an error so that
// SDK model-discovery calls don't break offline dev.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if s.store == nil {
		writeModelList(w, nil, nil)
		return
	}

	globals, err := s.store.ListAliases()
	if err != nil {
		writeStructuredError(w, http.StatusInternalServerError, "internal_error",
			err.Error(), "retry; check gateway logs if it persists")
		return
	}

	// Team-scoped aliases override globals with the same name.
	var merged []store.Alias
	seen := make(map[string]bool)
	if ak, ok := authpkg.APIKeyFromContext(r.Context()); ok && ak.TeamID != "" {
		team, err := s.store.ListAliasesForTeam(ak.TeamID)
		if err == nil {
			for _, a := range team {
				seen[a.Alias] = true
				merged = append(merged, a)
			}
		}
	}
	for _, a := range globals {
		if !seen[a.Alias] {
			merged = append(merged, a)
		}
	}

	costs, _ := s.store.ListLatestModelCosts() // best-effort; nil map is safe
	writeModelList(w, merged, costs)
}

type modelPricing struct {
	Prompt     float64  `json:"prompt"`
	Completion float64  `json:"completion"`
	Reasoning  *float64 `json:"reasoning,omitempty"`
}

type modelEntry struct {
	ID                  string        `json:"id"`
	Object              string        `json:"object"`
	Created             int64         `json:"created"`
	OwnedBy             string        `json:"owned_by"`
	ContextLength       *int64        `json:"context_length,omitempty"`
	MaxCompletionTokens *int64        `json:"max_completion_tokens,omitempty"`
	Pricing             *modelPricing `json:"pricing,omitempty"`
}

func writeModelList(w http.ResponseWriter, aliases []store.Alias, costs map[string]store.ModelCostEntry) {
	data := make([]modelEntry, 0, len(aliases))
	for _, a := range aliases {
		ts := a.CreatedAt.Unix()
		if ts <= 0 {
			ts = time.Now().Unix()
		}
		entry := modelEntry{
			ID:                  a.Alias,
			Object:              "model",
			Created:             ts,
			OwnedBy:             a.ProviderName,
			ContextLength:       a.ContextLength,
			MaxCompletionTokens: a.MaxCompletionTokens,
		}
		if costs != nil {
			key := a.ProviderName + ":" + a.UpstreamModel
			if c, ok := costs[key]; ok {
				entry.Pricing = &modelPricing{
					Prompt:     c.USDPerInput1K,
					Completion: c.USDPerOutput1K,
					Reasoning:  c.USDPerReasoning1K,
				}
			}
		}
		data = append(data, entry)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   data,
	})
}

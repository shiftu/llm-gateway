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
		writeModelList(w, nil)
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

	writeModelList(w, merged)
}

type modelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

func writeModelList(w http.ResponseWriter, aliases []store.Alias) {
	data := make([]modelEntry, 0, len(aliases))
	for _, a := range aliases {
		ts := a.CreatedAt.Unix()
		if ts <= 0 {
			ts = time.Now().Unix()
		}
		data = append(data, modelEntry{
			ID:      a.Alias,
			Object:  "model",
			Created: ts,
			OwnedBy: a.ProviderName,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   data,
	})
}

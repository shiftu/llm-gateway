package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	authpkg "github.com/panda/llm-gateway/internal/auth"
	"github.com/panda/llm-gateway/internal/store"
)

// openModelsStore opens an in-memory store pre-loaded with two providers.
func openModelsStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, p := range []store.Provider{
		{Name: "deepseek", Kind: "deepseek", OpenAIBaseURL: "https://api.deepseek.com", APIKey: "k"},
		{Name: "glm", Kind: "glm", OpenAIBaseURL: "https://open.bigmodel.cn", APIKey: "k"},
	} {
		if err := s.AddProvider(p); err != nil {
			t.Fatalf("AddProvider: %v", err)
		}
	}
	return s
}

func getModels(t *testing.T, srv *Server, ak *store.APIKey) modelsResponse {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	if ak != nil {
		r = r.WithContext(authpkg.NewContext(r.Context(), *ak))
	}
	w := httptest.NewRecorder()
	srv.handleModels(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("handleModels: status %d body %s", w.Code, w.Body.String())
	}
	var resp modelsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

type modelsResponse struct {
	Object string        `json:"object"`
	Data   []modelEntry  `json:"data"`
}

func modelIDs(resp modelsResponse) map[string]string {
	out := make(map[string]string, len(resp.Data))
	for _, m := range resp.Data {
		out[m.ID] = m.OwnedBy
	}
	return out
}

// --- stub mode ---

func TestHandleModels_StubMode_EmptyList(t *testing.T) {
	srv := NewServer("tok", nil)
	resp := getModels(t, srv, nil)
	if resp.Object != "list" {
		t.Errorf("object: want list, got %q", resp.Object)
	}
	if len(resp.Data) != 0 {
		t.Errorf("stub mode should return empty data, got %d entries", len(resp.Data))
	}
}

// --- global aliases ---

func TestHandleModels_GlobalAliases(t *testing.T) {
	st := openModelsStore(t)
	_ = st.SetAlias("fast", "deepseek", "deepseek-v4-flash", nil, nil)
	_ = st.SetAlias("smart", "glm", "glm-4-plus", nil, nil)

	srv := NewServer("tok", st)
	resp := getModels(t, srv, nil)
	ids := modelIDs(resp)

	if ids["fast"] != "deepseek" {
		t.Errorf("fast: want owned_by=deepseek, got %q", ids["fast"])
	}
	if ids["smart"] != "glm" {
		t.Errorf("smart: want owned_by=glm, got %q", ids["smart"])
	}
	if len(resp.Data) != 2 {
		t.Errorf("want 2 models, got %d", len(resp.Data))
	}
}

func TestHandleModels_ModelEntryShape(t *testing.T) {
	st := openModelsStore(t)
	_ = st.SetAlias("fast", "deepseek", "deepseek-v4-flash", nil, nil)

	srv := NewServer("tok", st)
	resp := getModels(t, srv, nil)
	if len(resp.Data) != 1 {
		t.Fatalf("want 1 model, got %d", len(resp.Data))
	}
	m := resp.Data[0]
	if m.Object != "model" {
		t.Errorf("object: want model, got %q", m.Object)
	}
	if m.Created <= 0 {
		t.Errorf("created must be positive unix timestamp, got %d", m.Created)
	}
}

// --- team alias visibility ---

func TestHandleModels_TeamAliasesIncludedForTeamKey(t *testing.T) {
	st := openModelsStore(t)
	_ = st.SetAlias("fast", "deepseek", "global-model", nil, nil)

	team, _ := st.AddTeam("acme", "Acme")
	_ = st.SetAliasForTeam("turbo", team.ID, "glm", "glm-turbo", nil, nil)

	ak, _, _ := st.IssueAPIKey(team.ID, "inbound", "test")
	ak.TeamID = team.ID

	srv := NewServer("tok", st)
	resp := getModels(t, srv, &ak)
	ids := modelIDs(resp)

	if _, ok := ids["turbo"]; !ok {
		t.Errorf("team alias 'turbo' should be visible to team key")
	}
	if _, ok := ids["fast"]; !ok {
		t.Errorf("global alias 'fast' should still be visible")
	}
}

func TestHandleModels_TeamAliasOverridesGlobal(t *testing.T) {
	st := openModelsStore(t)
	_ = st.SetAlias("fast", "deepseek", "global-model", nil, nil)

	team, _ := st.AddTeam("acme2", "Acme2")
	_ = st.SetAliasForTeam("fast", team.ID, "glm", "team-model", nil, nil)

	ak, _, _ := st.IssueAPIKey(team.ID, "inbound", "test")
	ak.TeamID = team.ID

	srv := NewServer("tok", st)
	resp := getModels(t, srv, &ak)
	ids := modelIDs(resp)

	// Team alias overrides global — only one "fast" entry, owned by glm
	if ids["fast"] != "glm" {
		t.Errorf("team alias should override global: want owned_by=glm, got %q", ids["fast"])
	}
	count := 0
	for _, m := range resp.Data {
		if m.ID == "fast" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("duplicate 'fast' entries: want 1, got %d", count)
	}
}

func TestHandleModels_NoTeamContext_OnlyGlobals(t *testing.T) {
	st := openModelsStore(t)
	_ = st.SetAlias("fast", "deepseek", "global-model", nil, nil)

	team, _ := st.AddTeam("acme3", "Acme3")
	_ = st.SetAliasForTeam("secret", team.ID, "glm", "team-only-model", nil, nil)

	srv := NewServer("tok", st)
	// No API key in context (legacy token path)
	resp := getModels(t, srv, nil)
	ids := modelIDs(resp)

	if _, ok := ids["secret"]; ok {
		t.Errorf("team alias 'secret' must not be visible without team context")
	}
	if _, ok := ids["fast"]; !ok {
		t.Errorf("global alias 'fast' should be visible")
	}
}

// --- method guard ---

func TestHandleModels_NonGET_Returns405(t *testing.T) {
	srv := NewServer("tok", nil)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		r := httptest.NewRequest(method, "/v1/models", nil)
		w := httptest.NewRecorder()
		srv.handleModels(w, r)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: want 405, got %d", method, w.Code)
		}
	}
}

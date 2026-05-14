package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/panda/llm-gateway/internal/store"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// RegisterTenancyTools adds the 8 team + API-key + quota management tools to s.
// Team/key issuance require mcp_super; list/get require mcp_admin.
func RegisterTenancyTools(s *mcpserver.MCPServer, st *store.Store) {
	s.AddTool(
		mcplib.NewTool("add_team",
			mcplib.WithDescription("Create a new tenant team. The slug must be lowercase alphanumeric/hyphen/underscore (2–64 chars)."),
			mcplib.WithString("slug", mcplib.Required(), mcplib.Description("URL-safe team identifier, e.g. 'acme' or 'ml-research'")),
			mcplib.WithString("name", mcplib.Required(), mcplib.Description("Human-readable team name, e.g. 'Acme Corp'")),
		),
		addTeamHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("list_teams",
			mcplib.WithDescription("List all registered teams ordered by slug."),
		),
		listTeamsHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("issue_api_key",
			mcplib.WithDescription("Issue a new API key for a team. The plaintext token is returned ONCE — store it immediately."),
			mcplib.WithString("team_slug", mcplib.Required(), mcplib.Description("Slug of the team to issue the key for")),
			mcplib.WithString("scope", mcplib.Required(), mcplib.Description("Key scope: inbound | mcp_super | mcp_admin | mcp_auditor | mcp_billing")),
			mcplib.WithString("label", mcplib.Description("Optional human-readable label for this key, e.g. 'claude-code-prod'")),
		),
		issueAPIKeyHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("revoke_api_key",
			mcplib.WithDescription("Revoke an API key by its ID. The key becomes invalid immediately."),
			mcplib.WithString("key_id", mcplib.Required(), mcplib.Description("API key ID (ak_xxxx) to revoke")),
		),
		revokeAPIKeyHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("list_api_keys",
			mcplib.WithDescription("List all API keys for a team. Hashes are never returned."),
			mcplib.WithString("team_slug", mcplib.Required(), mcplib.Description("Team slug to list keys for")),
		),
		listAPIKeysHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("set_quota",
			mcplib.WithDescription("Create or replace a quota limit. At least one of max_requests, max_tokens, max_usd_micros is required."),
			mcplib.WithString("scope_kind", mcplib.Required(), mcplib.Description("'team' or 'key'")),
			mcplib.WithString("scope_id", mcplib.Required(), mcplib.Description("Team slug (if scope_kind=team) or API key ID ak_xxxx (if scope_kind=key)")),
			mcplib.WithString("window", mcplib.Required(), mcplib.Description("Billing window: month | day | minute")),
			mcplib.WithNumber("max_requests", mcplib.Description("Max requests in window (omit = unlimited)")),
			mcplib.WithNumber("max_tokens", mcplib.Description("Max total tokens in window (omit = unlimited)")),
			mcplib.WithNumber("max_usd_micros", mcplib.Description("Max spend in microdollars in window, e.g. 1000000 = $1.00 (omit = unlimited)")),
		),
		setQuotaHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("get_quota",
			mcplib.WithDescription("Get a single quota entry by scope + window."),
			mcplib.WithString("scope_kind", mcplib.Required(), mcplib.Description("'team' or 'key'")),
			mcplib.WithString("scope_id", mcplib.Required(), mcplib.Description("Team slug or API key ID ak_xxxx")),
			mcplib.WithString("window", mcplib.Required(), mcplib.Description("month | day | minute")),
		),
		getQuotaHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("list_quotas",
			mcplib.WithDescription("List all quota entries for a team or key across all windows."),
			mcplib.WithString("scope_kind", mcplib.Required(), mcplib.Description("'team' or 'key'")),
			mcplib.WithString("scope_id", mcplib.Required(), mcplib.Description("Team slug or API key ID ak_xxxx")),
		),
		listQuotasHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("list_request_logs",
			mcplib.WithDescription("Tail recent request logs. Optionally filter by team or API key. Returns newest-first."),
			mcplib.WithString("team_slug", mcplib.Description("Filter to a specific team (optional)")),
			mcplib.WithString("key_id", mcplib.Description("Filter to a specific API key ID ak_xxxx (optional)")),
			mcplib.WithNumber("limit", mcplib.Description("Max rows to return (1–200, default 50)")),
		),
		listRequestLogsHandler(st),
	)
}

// --- handler factories ---

func addTeamHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "add_team"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		slug := req.GetString("slug", "")
		name := req.GetString("name", "")
		if slug == "" || name == "" {
			return mcplib.NewToolResultError("required: slug, name"), nil
		}
		team, err := st.AddTeam(slug, name)
		if err != nil {
			if errors.Is(err, store.ErrDuplicate) {
				return mcplib.NewToolResultError("team already exists: " + slug), nil
			}
			return mcplib.NewToolResultError("add_team failed: " + err.Error()), nil
		}
		out, _ := json.Marshal(map[string]any{
			"ok": true, "id": team.ID, "slug": team.Slug, "name": team.Name,
		})
		return mcplib.NewToolResultText(string(out)), nil
	}
}

func listTeamsHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "list_teams"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		teams, err := st.ListTeams()
		if err != nil {
			return mcplib.NewToolResultError("list_teams failed: " + err.Error()), nil
		}
		type row struct {
			ID        string `json:"id"`
			Slug      string `json:"slug"`
			Name      string `json:"name"`
			CreatedAt string `json:"created_at"`
		}
		out := make([]row, len(teams))
		for i, t := range teams {
			out[i] = row{ID: t.ID, Slug: t.Slug, Name: t.Name, CreatedAt: t.CreatedAt.UTC().Format(time.RFC3339)}
		}
		b, _ := json.Marshal(out)
		return mcplib.NewToolResultText(string(b)), nil
	}
}

func issueAPIKeyHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "issue_api_key"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		teamSlug := req.GetString("team_slug", "")
		scope := req.GetString("scope", "")
		label := req.GetString("label", "")
		if teamSlug == "" || scope == "" {
			return mcplib.NewToolResultError("required: team_slug, scope"), nil
		}
		team, err := st.GetTeamBySlug(teamSlug)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return mcplib.NewToolResultError("team not found: " + teamSlug), nil
			}
			return mcplib.NewToolResultError("issue_api_key: lookup team: " + err.Error()), nil
		}
		ak, plaintext, err := st.IssueAPIKey(team.ID, scope, label)
		if err != nil {
			return mcplib.NewToolResultError("issue_api_key failed: " + err.Error()), nil
		}
		out, _ := json.Marshal(map[string]any{
			"ok":      true,
			"key_id":  ak.ID,
			"token":   plaintext,
			"prefix":  ak.Prefix,
			"scope":   ak.Scope,
			"team_id": ak.TeamID,
			"warning": "This token will not be shown again. Store it securely now.",
		})
		return mcplib.NewToolResultText(string(out)), nil
	}
}

func revokeAPIKeyHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "revoke_api_key"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		keyID := req.GetString("key_id", "")
		if keyID == "" {
			return mcplib.NewToolResultError("required: key_id"), nil
		}
		if err := st.RevokeAPIKey(keyID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return mcplib.NewToolResultError("api key not found: " + keyID), nil
			}
			return mcplib.NewToolResultError("revoke_api_key failed: " + err.Error()), nil
		}
		out, _ := json.Marshal(map[string]any{"ok": true, "revoked": keyID})
		return mcplib.NewToolResultText(string(out)), nil
	}
}

func listAPIKeysHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "list_api_keys"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		teamSlug := req.GetString("team_slug", "")
		if teamSlug == "" {
			return mcplib.NewToolResultError("required: team_slug"), nil
		}
		team, err := st.GetTeamBySlug(teamSlug)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return mcplib.NewToolResultError("team not found: " + teamSlug), nil
			}
			return mcplib.NewToolResultError("list_api_keys: lookup team: " + err.Error()), nil
		}
		keys, err := st.ListAPIKeysByTeam(team.ID)
		if err != nil {
			return mcplib.NewToolResultError("list_api_keys failed: " + err.Error()), nil
		}
		type row struct {
			ID              string  `json:"id"`
			TeamID          string  `json:"team_id"`
			Prefix          string  `json:"prefix"`
			CreatedForLabel string  `json:"label,omitempty"`
			Scope           string  `json:"scope"`
			RevokedAt       *string `json:"revoked_at,omitempty"`
			CreatedAt       string  `json:"created_at"`
		}
		out := make([]row, len(keys))
		for i, k := range keys {
			r := row{
				ID:              k.ID,
				TeamID:          k.TeamID,
				Prefix:          k.Prefix,
				CreatedForLabel: k.CreatedForLabel,
				Scope:           k.Scope,
				CreatedAt:       k.CreatedAt.UTC().Format(time.RFC3339),
			}
			if k.RevokedAt != nil {
				s := k.RevokedAt.UTC().Format(time.RFC3339)
				r.RevokedAt = &s
			}
			out[i] = r
		}
		b, _ := json.Marshal(out)
		return mcplib.NewToolResultText(string(b)), nil
	}
}

// resolveQuotaScopeID converts scope_id to the internal DB ID:
// - scope_kind="team" → team slug → team.ID
// - scope_kind="key"  → key ID passed through as-is (ak_xxxx)
func resolveQuotaScopeID(st *store.Store, scopeKind, scopeID string) (string, error) {
	if scopeKind == "team" {
		team, err := st.GetTeamBySlug(scopeID)
		if err != nil {
			return "", fmt.Errorf("team not found: %s", scopeID)
		}
		return team.ID, nil
	}
	return scopeID, nil
}

func setQuotaHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "set_quota"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		scopeKind := req.GetString("scope_kind", "")
		scopeID := req.GetString("scope_id", "")
		window := req.GetString("window", "")
		if scopeKind == "" || scopeID == "" || window == "" {
			return mcplib.NewToolResultError("required: scope_kind, scope_id, window"), nil
		}
		resolvedID, err := resolveQuotaScopeID(st, scopeKind, scopeID)
		if err != nil {
			return mcplib.NewToolResultError("set_quota: " + err.Error()), nil
		}
		q := store.Quota{ScopeKind: scopeKind, ScopeID: resolvedID, Window: window}
		if v, ok := req.GetArguments()["max_requests"]; ok && v != nil {
			n := int64(v.(float64))
			q.MaxRequests = &n
		}
		if v, ok := req.GetArguments()["max_tokens"]; ok && v != nil {
			n := int64(v.(float64))
			q.MaxTokens = &n
		}
		if v, ok := req.GetArguments()["max_usd_micros"]; ok && v != nil {
			n := int64(v.(float64))
			q.MaxUSDMicros = &n
		}
		if q.MaxRequests == nil && q.MaxTokens == nil && q.MaxUSDMicros == nil {
			return mcplib.NewToolResultError("at least one of max_requests, max_tokens, max_usd_micros is required"), nil
		}
		if err := st.SetQuota(q); err != nil {
			return mcplib.NewToolResultError("set_quota failed: " + err.Error()), nil
		}
		out, _ := json.Marshal(map[string]any{
			"ok": true, "scope_kind": scopeKind, "scope_id": scopeID, "window": window,
		})
		return mcplib.NewToolResultText(string(out)), nil
	}
}

func getQuotaHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "get_quota"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		scopeKind := req.GetString("scope_kind", "")
		scopeID := req.GetString("scope_id", "")
		window := req.GetString("window", "")
		if scopeKind == "" || scopeID == "" || window == "" {
			return mcplib.NewToolResultError("required: scope_kind, scope_id, window"), nil
		}
		resolvedID, err := resolveQuotaScopeID(st, scopeKind, scopeID)
		if err != nil {
			return mcplib.NewToolResultError("get_quota: " + err.Error()), nil
		}
		q, err := st.GetQuota(scopeKind, resolvedID, window)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return mcplib.NewToolResultError(fmt.Sprintf("quota not found: %s/%s/%s", scopeKind, scopeID, window)), nil
			}
			return mcplib.NewToolResultError("get_quota failed: " + err.Error()), nil
		}
		b, _ := json.Marshal(quotaRow(q))
		return mcplib.NewToolResultText(string(b)), nil
	}
}

func listQuotasHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "list_quotas"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		scopeKind := req.GetString("scope_kind", "")
		scopeID := req.GetString("scope_id", "")
		if scopeKind == "" || scopeID == "" {
			return mcplib.NewToolResultError("required: scope_kind, scope_id"), nil
		}
		resolvedID, err := resolveQuotaScopeID(st, scopeKind, scopeID)
		if err != nil {
			return mcplib.NewToolResultError("list_quotas: " + err.Error()), nil
		}
		quotas, err := st.ListQuotasForScope(scopeKind, resolvedID)
		if err != nil {
			return mcplib.NewToolResultError("list_quotas failed: " + err.Error()), nil
		}
		rows := make([]any, len(quotas))
		for i, q := range quotas {
			rows[i] = quotaRow(q)
		}
		b, _ := json.Marshal(rows)
		return mcplib.NewToolResultText(string(b)), nil
	}
}

// quotaRow converts a Quota to a JSON-serialisable map, omitting nil limits.
func listRequestLogsHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "list_request_logs"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		teamSlug := req.GetString("team_slug", "")
		keyID := req.GetString("key_id", "")
		limit := 50
		if v, ok := req.GetArguments()["limit"]; ok && v != nil {
			limit = int(v.(float64))
		}

		f := store.ListRequestLogsFilter{APIKeyID: keyID, Limit: limit}
		if teamSlug != "" {
			team, err := st.GetTeamBySlug(teamSlug)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return mcplib.NewToolResultError("team not found: " + teamSlug), nil
				}
				return mcplib.NewToolResultError("list_request_logs: " + err.Error()), nil
			}
			f.TeamID = team.ID
		}

		logs, err := st.ListRequestLogs(f)
		if err != nil {
			return mcplib.NewToolResultError("list_request_logs failed: " + err.Error()), nil
		}

		type row struct {
			ID               int64  `json:"id"`
			Ts               string `json:"ts"`
			ClientModel      string `json:"client_model"`
			ResolvedModel    string `json:"resolved_model,omitempty"`
			ProviderName     string `json:"provider_name"`
			PromptTokens     int    `json:"prompt_tokens"`
			CompletionTokens int    `json:"completion_tokens"`
			TotalTokens      int    `json:"total_tokens"`
			LatencyMs        int    `json:"latency_ms"`
			Status           string `json:"status"`
			ErrorMsg         string `json:"error_msg,omitempty"`
			PromptExcerpt    string `json:"prompt_excerpt,omitempty"`
			APIKeyID         string `json:"api_key_id,omitempty"`
			TeamID           string `json:"team_id,omitempty"`
		}
		out := make([]row, len(logs))
		for i, l := range logs {
			out[i] = row{
				ID:               l.ID,
				Ts:               l.Ts.UTC().Format(time.RFC3339),
				ClientModel:      l.ClientModel,
				ResolvedModel:    l.ResolvedModel,
				ProviderName:     l.ProviderName,
				PromptTokens:     l.PromptTokens,
				CompletionTokens: l.CompletionTokens,
				TotalTokens:      l.TotalTokens,
				LatencyMs:        l.LatencyMs,
				Status:           l.Status,
				ErrorMsg:         l.ErrorMsg,
				PromptExcerpt:    l.PromptExcerpt,
				APIKeyID:         l.APIKeyID,
				TeamID:           l.TeamID,
			}
		}
		b, _ := json.Marshal(out)
		return mcplib.NewToolResultText(string(b)), nil
	}
}

func quotaRow(q store.Quota) map[string]any {
	m := map[string]any{
		"scope_kind": q.ScopeKind,
		"scope_id":   q.ScopeID,
		"window":     q.Window,
	}
	if q.MaxRequests != nil {
		m["max_requests"] = *q.MaxRequests
	}
	if q.MaxTokens != nil {
		m["max_tokens"] = *q.MaxTokens
	}
	if q.MaxUSDMicros != nil {
		m["max_usd_micros"] = *q.MaxUSDMicros
	}
	return m
}

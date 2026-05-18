package mcp

import (
	"context"
	"encoding/json"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/panda/llm-gateway/internal/store"
)

// RegisterFallbackTools adds set_fallback_policy, remove_fallback_policy, and
// list_fallback_policies so agents can configure v0.3 T4 fallback chains.
func RegisterFallbackTools(s *mcpserver.MCPServer, st *store.Store) {
	s.AddTool(
		mcplib.NewTool("set_fallback_policy",
			mcplib.WithDescription("Upsert a fallback policy for a trigger condition. team_id='' creates a global policy; a non-empty team_id scopes it to that team (team policy wins over global)."),
			mcplib.WithString("team_id", mcplib.Description("Team ID or '' for global")),
			mcplib.WithString("trigger", mcplib.Required(), mcplib.Description("Trigger: quota_exhausted | http_5xx | http_429 | latency_exceeded")),
			mcplib.WithNumber("threshold_ms", mcplib.Description("Latency threshold in ms (only for trigger=latency_exceeded)")),
			mcplib.WithString("action", mcplib.Required(), mcplib.Description("Action: next_best | specific_provider")),
			mcplib.WithString("target_provider", mcplib.Description("Provider name for action=specific_provider")),
			mcplib.WithNumber("max_chain_depth", mcplib.Description("Max fallback hops (default 3)")),
		),
		setFallbackPolicyHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("remove_fallback_policy",
			mcplib.WithDescription("Delete a fallback policy by its numeric ID."),
			mcplib.WithNumber("id", mcplib.Required(), mcplib.Description("Policy ID returned by set_fallback_policy")),
		),
		removeFallbackPolicyHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("list_fallback_policies",
			mcplib.WithDescription("List all fallback policies visible to a team: team-scoped rows plus global rows."),
			mcplib.WithString("team_id", mcplib.Description("Team ID or '' for global-only listing")),
		),
		listFallbackPoliciesHandler(st),
	)
}

type fallbackPolicyJSON struct {
	ID             int64  `json:"id"`
	TeamID         string `json:"team_id"`
	Trigger        string `json:"trigger"`
	ThresholdMs    int    `json:"threshold_ms"`
	Action         string `json:"action"`
	TargetProvider string `json:"target_provider,omitempty"`
	MaxChainDepth  int    `json:"max_chain_depth"`
}

func policyToJSON(p store.FallbackPolicy) fallbackPolicyJSON {
	return fallbackPolicyJSON{
		ID:             p.ID,
		TeamID:         p.TeamID,
		Trigger:        p.Trigger,
		ThresholdMs:    p.ThresholdMs,
		Action:         p.Action,
		TargetProvider: p.TargetProvider,
		MaxChainDepth:  p.MaxChainDepth,
	}
}

func setFallbackPolicyHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "set_fallback_policy"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		trigger := req.GetString("trigger", "")
		if trigger == "" {
			return mcplib.NewToolResultError("required parameter missing: trigger"), nil
		}
		action := req.GetString("action", "")
		if action == "" {
			return mcplib.NewToolResultError("required parameter missing: action"), nil
		}
		p := store.FallbackPolicy{
			TeamID:         req.GetString("team_id", ""),
			Trigger:        trigger,
			ThresholdMs:    int(req.GetFloat("threshold_ms", 0)),
			Action:         action,
			TargetProvider: req.GetString("target_provider", ""),
			MaxChainDepth:  int(req.GetFloat("max_chain_depth", 0)),
		}
		id, err := st.SetFallbackPolicy(p)
		if err != nil {
			return mcplib.NewToolResultError("set_fallback_policy failed: " + err.Error()), nil
		}
		p.ID = id
		out, _ := json.Marshal(policyToJSON(p))
		return mcplib.NewToolResultText(string(out)), nil
	}
}

func removeFallbackPolicyHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "remove_fallback_policy"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		id := int64(req.GetFloat("id", 0))
		if id <= 0 {
			return mcplib.NewToolResultError("required parameter missing: id"), nil
		}
		if err := st.RemoveFallbackPolicy(id); err != nil {
			return mcplib.NewToolResultError("remove_fallback_policy failed: " + err.Error()), nil
		}
		out, _ := json.Marshal(map[string]any{"deleted": id})
		return mcplib.NewToolResultText(string(out)), nil
	}
}

func listFallbackPoliciesHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "list_fallback_policies"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		teamID := req.GetString("team_id", "")
		policies, err := st.ListFallbackPolicies(teamID)
		if err != nil {
			return mcplib.NewToolResultError("list_fallback_policies failed: " + err.Error()), nil
		}
		out := make([]fallbackPolicyJSON, len(policies))
		for i, p := range policies {
			out[i] = policyToJSON(p)
		}
		b, _ := json.Marshal(out)
		return mcplib.NewToolResultText(string(b)), nil
	}
}

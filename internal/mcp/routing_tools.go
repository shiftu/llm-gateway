package mcp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/panda/llm-gateway/internal/store"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// RegisterRoutingTools adds the 3 routing-rule tools to s.
// set_routing_rule and remove_routing_rule require mcp_super;
// list_routing_rules requires mcp_admin.
func RegisterRoutingTools(s *mcpserver.MCPServer, st *store.Store) {
	s.AddTool(
		mcplib.NewTool("set_routing_rule",
			mcplib.WithDescription("Create or replace a routing rule. Routing rules conditionally dispatch requests to specific providers based on model name or provider kind, evaluated after alias lookup but before default provider fallback."),
			mcplib.WithNumber("id", mcplib.Description("Existing rule ID to update (omit to create a new rule)")),
			mcplib.WithString("team_id", mcplib.Description("Team ID to scope this rule to (omit for global rule)")),
			mcplib.WithNumber("priority", mcplib.Description("Lower priority = evaluated first (default 0)")),
			mcplib.WithString("match_field", mcplib.Description("What to match against: 'model' (request model name) or 'kind' (provider kind, e.g. 'deepseek') (default 'model')")),
			mcplib.WithString("match_op", mcplib.Description("Match operation: 'prefix', 'equals', or 'regex' (default 'prefix')")),
			mcplib.WithString("match_value", mcplib.Required(), mcplib.Description("Pattern to match, e.g. 'deepseek-' for prefix, 'glm-4' for equals, 'deepseek-.*' for regex")),
			mcplib.WithString("provider_name", mcplib.Required(), mcplib.Description("Provider to route matching requests to")),
			mcplib.WithString("upstream_model", mcplib.Description("Override upstream model name (omit to pass client model through)")),
			mcplib.WithBoolean("is_active", mcplib.Description("Whether the rule is active (default true)")),
		),
		setRoutingRuleHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("remove_routing_rule",
			mcplib.WithDescription("Delete a routing rule by ID."),
			mcplib.WithNumber("id", mcplib.Required(), mcplib.Description("Rule ID to delete")),
		),
		removeRoutingRuleHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("list_routing_rules",
			mcplib.WithDescription("List routing rules. Pass team_id to filter by team, empty for global rules only, 'all' for all rules."),
			mcplib.WithString("team_id", mcplib.Description("Team ID filter (omit for global rules, 'all' for all)")),
		),
		listRoutingRulesHandler(st),
	)
}

func setRoutingRuleHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "set_routing_rule"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}

		rule := store.RoutingRule{
			ID:            int64(req.GetInt("id", 0)),
			TeamID:        req.GetString("team_id", ""),
			Priority:      req.GetInt("priority", 0),
			MatchField:    req.GetString("match_field", "model"),
			MatchOp:       req.GetString("match_op", "prefix"),
			MatchValue:    req.GetString("match_value", ""),
			ProviderName:  req.GetString("provider_name", ""),
			UpstreamModel: req.GetString("upstream_model", ""),
			IsActive:      true,
		}

		if rule.MatchValue == "" || rule.ProviderName == "" {
			return mcplib.NewToolResultError("required: match_value, provider_name"), nil
		}

		// Default is_active to true unless explicitly set to false
		if !req.GetBool("is_active", true) {
			rule.IsActive = false
		}

		rule, err := st.SetRoutingRule(rule)
		if err != nil {
			return mcplib.NewToolResultError("set_routing_rule failed: " + err.Error()), nil
		}

		out, _ := json.Marshal(map[string]any{
			"ok":          true,
			"id":          rule.ID,
			"match_field": rule.MatchField,
			"match_op":    rule.MatchOp,
			"match_value": rule.MatchValue,
			"provider":    rule.ProviderName,
		})
		return mcplib.NewToolResultText(string(out)), nil
	}
}

func removeRoutingRuleHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "remove_routing_rule"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		id := int64(req.GetInt("id", 0))
		if id == 0 {
			return mcplib.NewToolResultError("required: id"), nil
		}
		if err := st.DeleteRoutingRule(id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return mcplib.NewToolResultError("rule not found"), nil
			}
			return mcplib.NewToolResultError("remove_routing_rule failed: " + err.Error()), nil
		}
		out, _ := json.Marshal(map[string]any{"ok": true, "deleted_id": id})
		return mcplib.NewToolResultText(string(out)), nil
	}
}

func listRoutingRulesHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "list_routing_rules"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		teamID := req.GetString("team_id", "")
		rules, err := st.ListRoutingRules(teamID)
		if err != nil {
			return mcplib.NewToolResultError("list_routing_rules failed: " + err.Error()), nil
		}

		type ruleJSON struct {
			ID            int64  `json:"id"`
			TeamID        string `json:"team_id,omitempty"`
			Priority      int    `json:"priority"`
			MatchField    string `json:"match_field"`
			MatchOp       string `json:"match_op"`
			MatchValue    string `json:"match_value"`
			ProviderName  string `json:"provider_name"`
			UpstreamModel string `json:"upstream_model,omitempty"`
			IsActive      bool   `json:"is_active"`
		}

		out := make([]ruleJSON, len(rules))
		for i, r := range rules {
			out[i] = ruleJSON{
				ID: r.ID, TeamID: r.TeamID, Priority: r.Priority,
				MatchField: r.MatchField, MatchOp: r.MatchOp,
				MatchValue: r.MatchValue, ProviderName: r.ProviderName,
				UpstreamModel: r.UpstreamModel, IsActive: r.IsActive,
			}
		}
		b, _ := json.Marshal(out)
		return mcplib.NewToolResultText(string(b)), nil
	}
}

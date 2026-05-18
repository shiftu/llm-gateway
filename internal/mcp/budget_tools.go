package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/panda/llm-gateway/internal/store"
)

// RegisterBudgetTools adds the 3 per-team budget management tools to s (v0.3 T16).
func RegisterBudgetTools(s *mcpserver.MCPServer, st *store.Store) {
	s.AddTool(
		mcplib.NewTool("set_budget",
			mcplib.WithDescription("Set or update a per-team USD spending budget for a period (day or month). Hard cap blocks requests once the limit is reached; soft threshold triggers a warning metric."),
			mcplib.WithString("team_id", mcplib.Required(), mcplib.Description("Team ID to set the budget for")),
			mcplib.WithString("period", mcplib.Required(), mcplib.Description("Budget period: 'day' or 'month'")),
			mcplib.WithNumber("usd_limit", mcplib.Required(), mcplib.Description("Maximum USD spend for the period (e.g. 10.0)")),
			mcplib.WithNumber("soft_threshold_pct", mcplib.Description("Percentage of limit at which a soft warning fires (default 80)")),
			mcplib.WithString("hard_cap_action", mcplib.Description("Action when limit is reached: 'block' (default) or 'warn'")),
		),
		setBudgetHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("check_budget",
			mcplib.WithDescription("Return current spend vs budget for a team+period. Reports spent USD, limit, percentage used, and whether soft/hard thresholds are breached."),
			mcplib.WithString("team_id", mcplib.Required(), mcplib.Description("Team ID to check")),
			mcplib.WithString("period", mcplib.Required(), mcplib.Description("Period to check: 'day' or 'month'")),
		),
		checkBudgetHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("list_budgets",
			mcplib.WithDescription("List all configured budgets, optionally filtered to a single team."),
			mcplib.WithString("team_id", mcplib.Description("Filter to this team (omit for all teams)")),
		),
		listBudgetsHandler(st),
	)
}

func setBudgetHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "set_budget"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		teamID := req.GetString("team_id", "")
		if teamID == "" {
			return mcplib.NewToolResultError("required: team_id"), nil
		}
		period := req.GetString("period", "")
		if period != "day" && period != "month" {
			return mcplib.NewToolResultError("period must be 'day' or 'month'"), nil
		}
		usdLimit := req.GetFloat("usd_limit", 0)
		if usdLimit <= 0 {
			return mcplib.NewToolResultError("usd_limit must be > 0"), nil
		}
		softPct := int(req.GetFloat("soft_threshold_pct", 80))
		if softPct < 0 || softPct > 100 {
			return mcplib.NewToolResultError("soft_threshold_pct must be 0–100"), nil
		}
		hardCapAction := req.GetString("hard_cap_action", "block")
		if hardCapAction != "block" && hardCapAction != "warn" {
			return mcplib.NewToolResultError("hard_cap_action must be 'block' or 'warn'"), nil
		}

		b := store.Budget{
			TeamID:           teamID,
			Period:           period,
			USDLimit:         usdLimit,
			SoftThresholdPct: softPct,
			HardCapAction:    hardCapAction,
			UpdatedAt:        time.Now(),
		}
		if err := st.SetBudget(b); err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("set_budget failed: %v", err)), nil
		}
		out, _ := json.Marshal(map[string]any{
			"status": "ok", "team_id": teamID, "period": period, "usd_limit": usdLimit,
		})
		return mcplib.NewToolResultText(string(out)), nil
	}
}

func checkBudgetHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "check_budget"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		teamID := req.GetString("team_id", "")
		if teamID == "" {
			return mcplib.NewToolResultError("required: team_id"), nil
		}
		period := req.GetString("period", "")
		if period != "day" && period != "month" {
			return mcplib.NewToolResultError("period must be 'day' or 'month'"), nil
		}

		status, err := st.CheckBudget(teamID, period, time.Now())
		if err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("check_budget failed: %v", err)), nil
		}

		out, _ := json.Marshal(map[string]any{
			"team_id":       teamID,
			"period":        period,
			"spent_usd":     status.SpentUSD,
			"limit_usd":     status.LimitUSD,
			"used_pct":      status.UsedPct,
			"soft_breached": status.SoftBreached,
			"hard_breached": status.HardBreached,
			"action":        status.Action,
		})
		return mcplib.NewToolResultText(string(out)), nil
	}
}

func listBudgetsHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "list_budgets"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		teamID := req.GetString("team_id", "")
		budgets, err := st.ListBudgets(teamID)
		if err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("list_budgets failed: %v", err)), nil
		}
		type row struct {
			TeamID           string  `json:"team_id"`
			Period           string  `json:"period"`
			USDLimit         float64 `json:"usd_limit"`
			SoftThresholdPct int     `json:"soft_threshold_pct"`
			HardCapAction    string  `json:"hard_cap_action"`
		}
		out := make([]row, len(budgets))
		for i, b := range budgets {
			out[i] = row{
				TeamID:           b.TeamID,
				Period:           b.Period,
				USDLimit:         b.USDLimit,
				SoftThresholdPct: b.SoftThresholdPct,
				HardCapAction:    b.HardCapAction,
			}
		}
		b, _ := json.Marshal(out)
		return mcplib.NewToolResultText(string(b)), nil
	}
}

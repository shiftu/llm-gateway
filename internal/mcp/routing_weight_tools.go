package mcp

import (
	"context"
	"encoding/json"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/panda/llm-gateway/internal/store"
)

// RegisterRoutingWeightTools adds set_routing_weights (mcp_admin) and
// get_routing_weights (mcp_auditor) so agents can tune the v0.3 cognitive
// router's per-team scoring without restarting the gateway.
func RegisterRoutingWeightTools(s *mcpserver.MCPServer, st *store.Store) {
	s.AddTool(
		mcplib.NewTool("set_routing_weights",
			mcplib.WithDescription("Set per-team routing-score weights for cost / latency / quality / health. Weights are not normalized — pass any non-negative floats; the router applies them as a linear weighted sum."),
			mcplib.WithString("team_id", mcplib.Required(), mcplib.Description("Team ID (tm_... format)")),
			mcplib.WithNumber("cost", mcplib.Required(), mcplib.Description("Weight applied to 1/cost score")),
			mcplib.WithNumber("latency", mcplib.Required(), mcplib.Description("Weight applied to 1/p99_latency score")),
			mcplib.WithNumber("quality", mcplib.Required(), mcplib.Description("Weight applied to quality score (default 1.0 per provider)")),
			mcplib.WithNumber("health", mcplib.Required(), mcplib.Description("Weight applied to binary health score (0 or 1)")),
		),
		setRoutingWeightsHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("get_routing_weights",
			mcplib.WithDescription("Return the per-team routing weights, or the v0.3 defaults (cost=0.4, latency=0.3, quality=0.2, health=0.1) if no row exists."),
			mcplib.WithString("team_id", mcplib.Required(), mcplib.Description("Team ID")),
		),
		getRoutingWeightsHandler(st),
	)
}

// routingWeightsJSON is the agent-facing JSON shape (snake_case keys).
type routingWeightsJSON struct {
	TeamID  string  `json:"team_id"`
	Cost    float64 `json:"cost"`
	Latency float64 `json:"latency"`
	Quality float64 `json:"quality"`
	Health  float64 `json:"health"`
}

func weightsToJSON(w store.RoutingWeights) routingWeightsJSON {
	return routingWeightsJSON{
		TeamID:  w.TeamID,
		Cost:    w.Cost,
		Latency: w.Latency,
		Quality: w.Quality,
		Health:  w.Health,
	}
}

func setRoutingWeightsHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "set_routing_weights"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		teamID := req.GetString("team_id", "")
		if teamID == "" {
			return mcplib.NewToolResultError("required parameter missing: team_id"), nil
		}
		w := store.RoutingWeights{
			TeamID:  teamID,
			Cost:    req.GetFloat("cost", 0),
			Latency: req.GetFloat("latency", 0),
			Quality: req.GetFloat("quality", 0),
			Health:  req.GetFloat("health", 0),
		}
		if err := st.SetRoutingWeights(w); err != nil {
			return mcplib.NewToolResultError("set_routing_weights failed: " + err.Error()), nil
		}
		out, _ := json.Marshal(weightsToJSON(w))
		return mcplib.NewToolResultText(string(out)), nil
	}
}

func getRoutingWeightsHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "get_routing_weights"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		teamID := req.GetString("team_id", "")
		if teamID == "" {
			return mcplib.NewToolResultError("required parameter missing: team_id"), nil
		}
		w, err := st.GetRoutingWeights(teamID)
		if err != nil {
			return mcplib.NewToolResultError("get_routing_weights failed: " + err.Error()), nil
		}
		out, _ := json.Marshal(weightsToJSON(w))
		return mcplib.NewToolResultText(string(out)), nil
	}
}

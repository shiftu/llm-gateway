package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/panda/llm-gateway/internal/store"
)

// RegisterExplainTools adds explain_route_trace to s. It allows mcp_auditor
// and above to inspect the score-and-pick decision behind any request.
func RegisterExplainTools(s *mcpserver.MCPServer, st *store.Store) {
	s.AddTool(
		mcplib.NewTool("explain_route_trace",
			mcplib.WithDescription("Show why the gateway chose a specific provider for a request. "+
				"Returns the full cognitive scoring breakdown — weights, per-provider scores, and the winner. "+
				"Returns a 'static routing' message for requests that did not use cognitive mode."),
			mcplib.WithNumber("request_id",
				mcplib.Description("ID from request_logs (use list_request_logs to find it)"),
				mcplib.Required(),
			),
		),
		explainRouteTraceHandler(st),
	)
}

// traceJSON mirrors the JSON produced by json.Marshal(router.CognitiveTrace).
// router.CognitiveTrace has no explicit json tags so fields are PascalCase.
type traceJSON struct {
	Weights    traceWeightsJSON     `json:"Weights"`
	Candidates []traceCandidateJSON `json:"Candidates"`
}

type traceWeightsJSON struct {
	Cost    float64 `json:"Cost"`
	Latency float64 `json:"Latency"`
	Quality float64 `json:"Quality"`
	Health  float64 `json:"Health"`
}

type traceCandidateJSON struct {
	ProviderName string           `json:"ProviderName"`
	Breakdown    traceBreakdown   `json:"Breakdown"`
}

type traceBreakdown struct {
	CostScore    float64 `json:"CostScore"`
	LatencyScore float64 `json:"LatencyScore"`
	QualityScore float64 `json:"QualityScore"`
	HealthScore  float64 `json:"HealthScore"`
	Total        float64 `json:"Total"`
}

func explainRouteTraceHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "explain_route_trace"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		id := int64(req.GetInt("request_id", 0))
		if id == 0 {
			return mcplib.NewToolResultError("required parameter missing: request_id"), nil
		}
		rl, err := st.GetRequestLog(id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return mcplib.NewToolResultError(
					fmt.Sprintf("request #%d not found in request_logs", id),
				), nil
			}
			return mcplib.NewToolResultError(fmt.Sprintf("store error: %v", err)), nil
		}

		if rl.RouteTrace == "" {
			return mcplib.NewToolResultText(fmt.Sprintf(
				"Request #%d used static routing — provider %q was selected directly from the alias or default, "+
					"no cognitive scoring was performed.",
				id, rl.ProviderName,
			)), nil
		}

		var trace traceJSON
		if err := json.Unmarshal([]byte(rl.RouteTrace), &trace); err != nil {
			return mcplib.NewToolResultError(
				fmt.Sprintf("route_trace for request #%d is malformed: %v", id, err),
			), nil
		}

		return mcplib.NewToolResultText(formatTrace(id, rl, trace)), nil
	}
}

func formatTrace(id int64, rl store.RequestLog, tr traceJSON) string {
	var sb strings.Builder
	w := tr.Weights
	fmt.Fprintf(&sb, "Route-decision trace for request #%d\n", id)
	fmt.Fprintf(&sb, "  Model: %q → provider: %q (upstream: %q)\n",
		rl.ClientModel, rl.ProviderName, rl.ResolvedModel)
	fmt.Fprintf(&sb, "\nScoring weights:  cost=%.2f  latency=%.2f  quality=%.2f  health=%.2f\n",
		w.Cost, w.Latency, w.Quality, w.Health)
	fmt.Fprintf(&sb, "\nCandidates (sorted best → worst):\n")
	fmt.Fprintf(&sb, "  %-4s  %-20s  %-10s  %-10s  %-10s  %-10s  %-10s\n",
		"Rank", "Provider", "Cost", "Latency", "Quality", "Health", "Total")
	fmt.Fprintf(&sb, "  %s\n", strings.Repeat("-", 80))

	for i, c := range tr.Candidates {
		b := c.Breakdown
		winner := ""
		if i == 0 {
			winner = "  ← winner"
		}
		fmt.Fprintf(&sb, "  #%-3d  %-20s  %-10.4f  %-10.4f  %-10.4f  %-10.4f  %-10.4f%s\n",
			i+1, c.ProviderName, b.CostScore, b.LatencyScore, b.QualityScore, b.HealthScore, b.Total, winner)
	}
	return sb.String()
}

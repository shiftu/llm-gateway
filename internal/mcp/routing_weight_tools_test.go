package mcp

import (
	"encoding/json"
	"testing"
)

// TestSetRoutingWeights_PersistsViaMCP: an mcp_admin caller can write per-team
// weights and read them back through get_routing_weights.
func TestSetRoutingWeights_PersistsViaMCP(t *testing.T) {
	st := openTestStore(t)
	team, err := st.AddTeam("billing", "Billing")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}

	setH := setRoutingWeightsHandler(st)
	getH := getRoutingWeightsHandler(st)

	res, err := setH(ctxWithScope("mcp_admin"), callTool(map[string]any{
		"team_id": team.ID,
		"cost":    0.5,
		"latency": 0.3,
		"quality": 0.1,
		"health":  0.1,
	}))
	if err != nil {
		t.Fatalf("set handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("set tool error: %v", res.Content)
	}

	res2, err := getH(ctxWithScope("mcp_auditor"), callTool(map[string]any{"team_id": team.ID}))
	if err != nil {
		t.Fatalf("get handler error: %v", err)
	}
	if res2.IsError {
		t.Fatalf("get tool error: %v", res2.Content)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(textOf(t, res2)), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["cost"].(float64) != 0.5 {
		t.Errorf("cost: want 0.5, got %v", body["cost"])
	}
	if body["latency"].(float64) != 0.3 {
		t.Errorf("latency: want 0.3, got %v", body["latency"])
	}
}

// TestGetRoutingWeights_DefaultsForMissingTeam: querying weights for a team
// with no row returns the v0.3 defaults as JSON.
func TestGetRoutingWeights_DefaultsForMissingTeam(t *testing.T) {
	st := openTestStore(t)
	getH := getRoutingWeightsHandler(st)

	res, err := getH(ctxWithScope("mcp_auditor"), callTool(map[string]any{"team_id": "no-such-team"}))
	if err != nil {
		t.Fatalf("get handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected defaults to be returned, got tool error: %v", res.Content)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(textOf(t, res)), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["cost"].(float64) != 0.4 {
		t.Errorf("default cost: want 0.4, got %v", body["cost"])
	}
	if body["health"].(float64) != 0.1 {
		t.Errorf("default health: want 0.1, got %v", body["health"])
	}
}

// TestSetRoutingWeights_AuditorDenied: an mcp_auditor caller is read-only;
// the write must fail with the permission-denied tool error.
func TestSetRoutingWeights_AuditorDenied(t *testing.T) {
	st := openTestStore(t)
	setH := setRoutingWeightsHandler(st)
	res, err := setH(ctxWithScope("mcp_auditor"), callTool(map[string]any{
		"team_id": "any", "cost": 1, "latency": 0, "quality": 0, "health": 0,
	}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.IsError {
		t.Fatalf("auditor write: want tool error, got success")
	}
}

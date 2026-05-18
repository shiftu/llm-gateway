package store

import (
	"testing"
)

// TestRoutingWeights_GetDefaults: GetRoutingWeights for a team with no row
// returns the v0.3 plan defaults (cost=0.4, latency=0.3, quality=0.2,
// health=0.1) with no error. This keeps the call site simple — the router
// always gets usable weights.
func TestRoutingWeights_GetDefaults(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	got, err := st.GetRoutingWeights("team-with-no-row")
	if err != nil {
		t.Fatalf("GetRoutingWeights on missing row: want defaults, got error %v", err)
	}
	if got.Cost != 0.4 {
		t.Errorf("default Cost: want 0.4, got %v", got.Cost)
	}
	if got.Latency != 0.3 {
		t.Errorf("default Latency: want 0.3, got %v", got.Latency)
	}
	if got.Quality != 0.2 {
		t.Errorf("default Quality: want 0.2, got %v", got.Quality)
	}
	if got.Health != 0.1 {
		t.Errorf("default Health: want 0.1, got %v", got.Health)
	}
	if got.TeamID != "team-with-no-row" {
		t.Errorf("TeamID echo: want team-with-no-row, got %q", got.TeamID)
	}
}

// TestRoutingWeights_SetAndGet: after SetRoutingWeights, the subsequent
// GetRoutingWeights for the same team returns the stored values rather than
// the defaults. A second Set on the same team_id upserts (no duplicate row,
// new values overwrite).
func TestRoutingWeights_SetAndGet(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	// Need a team to satisfy the FK.
	team, err := st.AddTeam("team1", "Team One")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}

	w := RoutingWeights{TeamID: team.ID, Cost: 0.7, Latency: 0.1, Quality: 0.1, Health: 0.1}
	if err := st.SetRoutingWeights(w); err != nil {
		t.Fatalf("SetRoutingWeights: %v", err)
	}
	got, err := st.GetRoutingWeights(team.ID)
	if err != nil {
		t.Fatalf("GetRoutingWeights: %v", err)
	}
	if got.Cost != 0.7 || got.Latency != 0.1 || got.Quality != 0.1 || got.Health != 0.1 {
		t.Errorf("after Set: want (0.7, 0.1, 0.1, 0.1), got (%v, %v, %v, %v)",
			got.Cost, got.Latency, got.Quality, got.Health)
	}

	// Upsert with new values.
	w2 := RoutingWeights{TeamID: team.ID, Cost: 0.2, Latency: 0.6, Quality: 0.1, Health: 0.1}
	if err := st.SetRoutingWeights(w2); err != nil {
		t.Fatalf("SetRoutingWeights upsert: %v", err)
	}
	got2, _ := st.GetRoutingWeights(team.ID)
	if got2.Cost != 0.2 || got2.Latency != 0.6 {
		t.Errorf("after upsert: want Cost=0.2 Latency=0.6, got Cost=%v Latency=%v", got2.Cost, got2.Latency)
	}
}

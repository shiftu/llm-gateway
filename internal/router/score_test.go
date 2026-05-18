package router

import (
	"math"
	"testing"
)

const floatEps = 1e-9

func approxEq(a, b float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return false
	}
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < floatEps
}

// TestScore_BasicWeights: with known inputs and weights, Score returns the
// expected weighted total and a populated breakdown. This is the core
// behavior — cost_score = 1/(input+output cost), latency_score = 1/p99,
// quality_score = quality_input, health_score = 1 if healthy.
func TestScore_BasicWeights(t *testing.T) {
	inputs := ScoreInputs{
		InputCostUSD1k:  0.001,
		OutputCostUSD1k: 0.002,
		P99LatencyMs:    1000,
		Quality:         1.0,
		Healthy:         true,
	}
	weights := Weights{
		Cost:    0.4,
		Latency: 0.3,
		Quality: 0.2,
		Health:  0.1,
	}
	got := Score(inputs, weights)

	// cost_score = 1 / (0.001+0.002) = 333.333...
	wantCost := 1.0 / 0.003
	if !approxEq(got.CostScore, wantCost) {
		t.Errorf("CostScore: want %v, got %v", wantCost, got.CostScore)
	}
	// latency_score = 1 / 1000 = 0.001
	if !approxEq(got.LatencyScore, 0.001) {
		t.Errorf("LatencyScore: want 0.001, got %v", got.LatencyScore)
	}
	// quality_score = 1.0 (pass-through)
	if !approxEq(got.QualityScore, 1.0) {
		t.Errorf("QualityScore: want 1.0, got %v", got.QualityScore)
	}
	// health_score = 1 (healthy)
	if !approxEq(got.HealthScore, 1.0) {
		t.Errorf("HealthScore: want 1.0, got %v", got.HealthScore)
	}
	wantTotal := 0.4*wantCost + 0.3*0.001 + 0.2*1.0 + 0.1*1.0
	if !approxEq(got.Total, wantTotal) {
		t.Errorf("Total: want %v, got %v", wantTotal, got.Total)
	}
}

// TestScore_UnhealthyZeroTotal: per the acceptance criterion on issue #11
// ("Unhealthy provider always scores 0 (gets skipped)"), an unhealthy
// provider's Total must be 0 regardless of cost/latency/quality. The
// per-dimension breakdown still reflects the observation so the explain
// trace shows WHY it scored 0 — but the composite is short-circuited.
func TestScore_UnhealthyZeroTotal(t *testing.T) {
	inputs := ScoreInputs{
		InputCostUSD1k:  0.0001, // cheap
		OutputCostUSD1k: 0.0002,
		P99LatencyMs:    10, // fast
		Quality:         5.0,
		Healthy:         false, // but DOWN
	}
	weights := Weights{Cost: 0.4, Latency: 0.3, Quality: 0.2, Health: 0.1}
	got := Score(inputs, weights)

	if got.Total != 0 {
		t.Errorf("Total: want 0 for unhealthy provider, got %v (breakdown=%+v)", got.Total, got)
	}
	if got.HealthScore != 0 {
		t.Errorf("HealthScore: want 0 for unhealthy, got %v", got.HealthScore)
	}
	// Other dims should still reflect the observation for explain trace.
	if got.CostScore == 0 {
		t.Errorf("CostScore should still reflect cost even when unhealthy (for trace), got 0")
	}
}

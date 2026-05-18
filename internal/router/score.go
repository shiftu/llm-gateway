package router

// ScoreInputs are the per-provider observations the scorer reads. Cost +
// quality come from the model_costs / future eval table; latency + health
// come from the v0.3 T1 health Manager snapshot.
type ScoreInputs struct {
	InputCostUSD1k  float64
	OutputCostUSD1k float64
	P99LatencyMs    int64
	Quality         float64
	Healthy         bool
}

// Weights configures the contribution of each scoring dimension. Defaults
// (cost=0.4, latency=0.3, quality=0.2, health=0.1) live in the routing_weights
// store layer; the scorer itself is weight-neutral.
type Weights struct {
	Cost    float64
	Latency float64
	Quality float64
	Health  float64
}

// Breakdown is the per-dimension explain output returned alongside the
// weighted Total. T5 explain-trace will surface this to agents verbatim.
type Breakdown struct {
	CostScore    float64
	LatencyScore float64
	QualityScore float64
	HealthScore  float64
	Total        float64
}

// Score computes the weighted composite score for one provider under one
// team's weights. Higher = preferred. Each dimension normalizes so the
// router can compare providers of very different cost/latency without one
// dimension dominating.
func Score(in ScoreInputs, w Weights) Breakdown {
	costScore := 1.0 / (in.InputCostUSD1k + in.OutputCostUSD1k)
	latencyScore := 1.0 / float64(in.P99LatencyMs)
	qualityScore := in.Quality
	healthScore := 0.0
	if in.Healthy {
		healthScore = 1.0
	}
	var total float64
	if in.Healthy {
		total = w.Cost*costScore + w.Latency*latencyScore + w.Quality*qualityScore + w.Health*healthScore
	}
	// Unhealthy → Total stays 0 even if cost/latency look great. Per-dim
	// scores are still populated so the explain trace shows why.
	return Breakdown{
		CostScore:    costScore,
		LatencyScore: latencyScore,
		QualityScore: qualityScore,
		HealthScore:  healthScore,
		Total:        total,
	}
}

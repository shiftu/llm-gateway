package store

import "time"

// SeedQwenModelCosts inserts default Qwen (DashScope) pricing rows.
// Safe to re-run — uses ON CONFLICT DO UPDATE internally (via SetModelCost).
// Pricing source: Alibaba Cloud DashScope rate card, effective 2026-05.
func SeedQwenModelCosts(s *Store) error {
	eff := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	models := []ModelCost{
		{Provider: "qwen", Model: "qwen-max", USDPerInput1k: 0.0040, USDPerOutput1k: 0.0120, EffectiveFrom: eff},
		{Provider: "qwen", Model: "qwen-max-longcontext", USDPerInput1k: 0.0040, USDPerOutput1k: 0.0120, EffectiveFrom: eff},
		{Provider: "qwen", Model: "qwen-plus", USDPerInput1k: 0.0008, USDPerOutput1k: 0.0024, EffectiveFrom: eff},
		{Provider: "qwen", Model: "qwen-turbo", USDPerInput1k: 0.0003, USDPerOutput1k: 0.0009, EffectiveFrom: eff},
		{Provider: "qwen", Model: "qwen-long", USDPerInput1k: 0.00014, USDPerOutput1k: 0.00057, EffectiveFrom: eff},
	}
	for _, mc := range models {
		if err := s.SetModelCost(mc); err != nil {
			return err
		}
	}
	return nil
}

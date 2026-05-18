package store

import "time"

// SeedMoonshotModelCosts inserts default Moonshot/Kimi pricing rows.
// Safe to re-run — uses ON CONFLICT DO UPDATE internally (via SetModelCost).
// Pricing source: Moonshot AI rate card, effective 2026-05.
// Moonshot uses context-window-based pricing (same rate for input and output).
func SeedMoonshotModelCosts(s *Store) error {
	effective := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	models := []ModelCost{
		{
			Provider:       "moonshot",
			Model:          "moonshot-v1-8k",
			USDPerInput1k:  0.0012,
			USDPerOutput1k: 0.0012,
			EffectiveFrom:  effective,
		},
		{
			Provider:       "moonshot",
			Model:          "moonshot-v1-32k",
			USDPerInput1k:  0.0024,
			USDPerOutput1k: 0.0024,
			EffectiveFrom:  effective,
		},
		{
			Provider:       "moonshot",
			Model:          "moonshot-v1-128k",
			USDPerInput1k:  0.0070,
			USDPerOutput1k: 0.0070,
			EffectiveFrom:  effective,
		},
	}
	for _, m := range models {
		if err := s.SetModelCost(m); err != nil {
			return err
		}
	}
	return nil
}

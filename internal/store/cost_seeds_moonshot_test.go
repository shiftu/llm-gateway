package store

import (
	"testing"
	"time"
)

func TestSeedMoonshotModelCosts(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	if err := SeedMoonshotModelCosts(s); err != nil {
		t.Fatal(err)
	}
	// Verify moonshot-v1-8k cost exists
	mc, err := s.GetModelCost("moonshot", "moonshot-v1-8k", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if mc.USDPerInput1k != 0.0012 {
		t.Errorf("want 0.0012, got %v", mc.USDPerInput1k)
	}
	if mc.USDPerOutput1k != 0.0012 {
		t.Errorf("want 0.0012, got %v", mc.USDPerOutput1k)
	}
	// Verify re-run is idempotent
	if err := SeedMoonshotModelCosts(s); err != nil {
		t.Fatal("re-run should be idempotent")
	}
}

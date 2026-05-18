package store

import (
	"testing"
	"time"
)

func TestSeedQwenModelCosts(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	if err := SeedQwenModelCosts(s); err != nil {
		t.Fatal(err)
	}
	// Verify qwen-max cost exists
	mc, err := s.GetModelCost("qwen", "qwen-max", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if mc.USDPerInput1k != 0.0040 {
		t.Errorf("want 0.0040, got %v", mc.USDPerInput1k)
	}
	if mc.USDPerOutput1k != 0.0120 {
		t.Errorf("want 0.0120, got %v", mc.USDPerOutput1k)
	}
	// Verify re-run is idempotent
	if err := SeedQwenModelCosts(s); err != nil {
		t.Fatalf("re-run should be idempotent: %v", err)
	}
}

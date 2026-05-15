package store

import (
	"testing"
	"time"
)

func TestGetAndIncrementRPM(t *testing.T) {
	s := openTest(t)
	// Need a real api_key for FK constraint
	team, err := s.AddTeam("rpm-test", "rpm-test")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}
	ak, _, err := s.IssueAPIKey(team.ID, "inbound", "rpm-test-key")
	if err != nil {
		t.Fatalf("IssueAPIKey: %v", err)
	}

	minute := time.Now().UTC().Unix() / 60

	// First increment → count=1
	c, err := s.GetAndIncrementRPM(ak.ID, minute)
	if err != nil {
		t.Fatalf("GetAndIncrementRPM: %v", err)
	}
	if c != 1 {
		t.Errorf("first increment: want count=1, got %d", c)
	}

	// Second increment → count=2
	c, err = s.GetAndIncrementRPM(ak.ID, minute)
	if err != nil {
		t.Fatalf("GetAndIncrementRPM: %v", err)
	}
	if c != 2 {
		t.Errorf("second increment: want count=2, got %d", c)
	}

	// Read-only should return 2
	c, err = s.GetRPMCount(ak.ID, minute)
	if err != nil {
		t.Fatalf("GetRPMCount: %v", err)
	}
	if c != 2 {
		t.Errorf("read-only: want count=2, got %d", c)
	}

	// Different minute → starts at 1
	minute2 := minute + 1
	c, err = s.GetAndIncrementRPM(ak.ID, minute2)
	if err != nil {
		t.Fatalf("GetAndIncrementRPM (minute2): %v", err)
	}
	if c != 1 {
		t.Errorf("new minute: want count=1, got %d", c)
	}

	// Original minute still at 2
	c, err = s.GetRPMCount(ak.ID, minute)
	if err != nil {
		t.Fatalf("GetRPMCount (original minute): %v", err)
	}
	if c != 2 {
		t.Errorf("original minute after new minute: want count=2, got %d", c)
	}
}

func TestGetRPMCount_NoRow(t *testing.T) {
	s := openTest(t)

	c, err := s.GetRPMCount("nonexistent", 0)
	if err != nil {
		t.Fatalf("GetRPMCount: %v", err)
	}
	if c != 0 {
		t.Errorf("want 0 for nonexistent bucket, got %d", c)
	}
}

func TestRPMRestartPersistence(t *testing.T) {
	// Verify that RPM state survives a store close/reopen.
	dsn := t.TempDir() + "/rpm_persist.db"

	s1, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open s1: %v", err)
	}

	team, err := s1.AddTeam("persist-team", "persist-team")
	if err != nil {
		t.Fatalf("AddTeam: %v", err)
	}
	ak, _, err := s1.IssueAPIKey(team.ID, "inbound", "persist-key")
	if err != nil {
		t.Fatalf("IssueAPIKey: %v", err)
	}

	minute := time.Now().UTC().Unix() / 60

	// Increment twice
	s1.GetAndIncrementRPM(ak.ID, minute)
	s1.GetAndIncrementRPM(ak.ID, minute)

	// Close store
	if err := s1.Close(); err != nil {
		t.Fatalf("Close s1: %v", err)
	}

	// Reopen
	s2, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open s2: %v", err)
	}
	defer s2.Close()

	// State should persist: next increment = 3
	c, err := s2.GetAndIncrementRPM(ak.ID, minute)
	if err != nil {
		t.Fatalf("GetAndIncrementRPM after restart: %v", err)
	}
	if c != 3 {
		t.Errorf("after restart: want count=3, got %d", c)
	}

	// Read-only confirms 3
	c, err = s2.GetRPMCount(ak.ID, minute)
	if err != nil {
		t.Fatalf("GetRPMCount after restart: %v", err)
	}
	if c != 3 {
		t.Errorf("read-only after restart: want 3, got %d", c)
	}
}

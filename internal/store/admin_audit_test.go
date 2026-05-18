package store

import (
	"testing"
	"time"
)

func TestLogAdminAction(t *testing.T) {
	s := openTest(t)

	err := s.LogAdminAction("add_provider", "provider", "deepseek", map[string]string{"kind": "deepseek"})
	if err != nil {
		t.Fatal(err)
	}

	logs, err := s.ListAdminAuditLogs(AuditLogFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logs))
	}
	if logs[0].Action != "add_provider" {
		t.Fatalf("expected action add_provider, got %s", logs[0].Action)
	}
	if logs[0].TargetType != "provider" {
		t.Fatalf("expected target_type provider, got %s", logs[0].TargetType)
	}
	if logs[0].TargetID != "deepseek" {
		t.Fatalf("expected target_id deepseek, got %s", logs[0].TargetID)
	}
}

func TestListAuditLogsFilter(t *testing.T) {
	s := openTest(t)

	s.LogAdminAction("add_provider", "provider", "ds", nil)
	s.LogAdminAction("issue_api_key", "api_key", "ak_1", nil)
	s.LogAdminAction("add_team", "team", "tm_1", nil)

	// Filter by action
	logs, _ := s.ListAdminAuditLogs(AuditLogFilter{Action: "issue_api_key", Limit: 10})
	if len(logs) != 1 {
		t.Fatalf("expected 1 log for issue_api_key, got %d", len(logs))
	}
	if logs[0].Action != "issue_api_key" {
		t.Fatalf("unexpected action: %s", logs[0].Action)
	}

	// Filter by target type
	logs, _ = s.ListAdminAuditLogs(AuditLogFilter{TargetType: "provider", Limit: 10})
	if len(logs) != 1 {
		t.Fatalf("expected 1 log for provider type, got %d", len(logs))
	}
}

func TestPruneAuditLog(t *testing.T) {
	s := openTest(t)

	s.LogAdminAction("add_provider", "provider", "ds", nil)
	s.LogAdminAction("issue_api_key", "api_key", "ak_1", nil)

	// beforeDays=365: cutoff is 1 year ago — recent entries must survive.
	n, err := s.PruneAuditLog(365)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected 0 rows pruned with 365-day window, got %d", n)
	}

	logs, _ := s.ListAdminAuditLogs(AuditLogFilter{Limit: 10})
	if len(logs) != 2 {
		t.Fatalf("expected 2 entries after no-op prune, got %d", len(logs))
	}
}

func TestAuditLogOrder(t *testing.T) {
	s := openTest(t)

	s.LogAdminAction("first", "provider", "a", nil)
	time.Sleep(2 * time.Millisecond)
	s.LogAdminAction("second", "provider", "b", nil)

	logs, _ := s.ListAdminAuditLogs(AuditLogFilter{Limit: 10})
	if len(logs) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(logs))
	}
	// Newest first
	if logs[0].Action != "second" {
		t.Fatalf("expected newest first, got %s", logs[0].Action)
	}
}

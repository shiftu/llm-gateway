package store

import (
	"testing"
)

func TestAuditChain_HashComputed(t *testing.T) {
	s := openTest(t)

	if err := s.LogAdminAction("add_provider", "provider", "ds", nil); err != nil {
		t.Fatalf("LogAdminAction: %v", err)
	}

	logs, err := s.ListAdminAuditLogs(AuditLogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListAdminAuditLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if logs[0].EntryHash == "" {
		t.Fatal("expected non-empty entry_hash after LogAdminAction")
	}
}

func TestAuditChain_ChainLink(t *testing.T) {
	s := openTest(t)

	if err := s.LogAdminAction("first_action", "provider", "p1", nil); err != nil {
		t.Fatalf("LogAdminAction first: %v", err)
	}
	if err := s.LogAdminAction("second_action", "provider", "p2", nil); err != nil {
		t.Fatalf("LogAdminAction second: %v", err)
	}

	logs, err := s.ListAdminAuditLogs(AuditLogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListAdminAuditLogs: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2 logs, got %d", len(logs))
	}
	// ListAdminAuditLogs returns newest-first; logs[0] is the second entry.
	second := logs[0]
	first := logs[1]

	if first.PrevHash != "" {
		t.Errorf("first entry prev_hash should be empty, got %q", first.PrevHash)
	}
	if second.PrevHash != first.EntryHash {
		t.Errorf("second.prev_hash=%q, want first.entry_hash=%q", second.PrevHash, first.EntryHash)
	}
}

func TestAuditChain_VerifyOK(t *testing.T) {
	s := openTest(t)

	if err := s.LogAdminAction("action_a", "team", "t1", nil); err != nil {
		t.Fatalf("LogAdminAction a: %v", err)
	}
	if err := s.LogAdminAction("action_b", "team", "t2", map[string]string{"k": "v"}); err != nil {
		t.Fatalf("LogAdminAction b: %v", err)
	}
	if err := s.LogAdminAction("action_c", "key", "k1", nil); err != nil {
		t.Fatalf("LogAdminAction c: %v", err)
	}

	res, err := s.VerifyAuditChain()
	if err != nil {
		t.Fatalf("VerifyAuditChain: %v", err)
	}
	if !res.OK {
		t.Errorf("expected OK=true, got OK=false, invalid_rows=%d, first_invalid=%d", res.InvalidRows, res.FirstInvalid)
	}
	if res.TotalRows != 3 {
		t.Errorf("expected total_rows=3, got %d", res.TotalRows)
	}
	if res.HashedRows != 3 {
		t.Errorf("expected hashed_rows=3, got %d", res.HashedRows)
	}
	if res.InvalidRows != 0 {
		t.Errorf("expected invalid_rows=0, got %d", res.InvalidRows)
	}
}

func TestAuditChain_DetectTamper(t *testing.T) {
	s := openTest(t)

	if err := s.LogAdminAction("original_action", "provider", "ds", map[string]string{"key": "val"}); err != nil {
		t.Fatalf("LogAdminAction: %v", err)
	}
	if err := s.LogAdminAction("second_action", "provider", "ds2", nil); err != nil {
		t.Fatalf("LogAdminAction second: %v", err)
	}

	// Tamper: overwrite the detail of the first row.
	logs, err := s.ListAdminAuditLogs(AuditLogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListAdminAuditLogs: %v", err)
	}
	// logs[0]=second (newest), logs[1]=first; tamper the first (older) row.
	firstID := logs[1].ID
	_, err = s.db.Exec(`UPDATE admin_audit SET detail='tampered' WHERE id=?`, firstID)
	if err != nil {
		t.Fatalf("UPDATE tamper: %v", err)
	}

	res, err := s.VerifyAuditChain()
	if err != nil {
		t.Fatalf("VerifyAuditChain: %v", err)
	}
	if res.OK {
		t.Error("expected OK=false after tamper")
	}
	if res.InvalidRows != 1 {
		t.Errorf("expected invalid_rows=1, got %d", res.InvalidRows)
	}
	if res.FirstInvalid != firstID {
		t.Errorf("expected first_invalid=%d, got %d", firstID, res.FirstInvalid)
	}
}

func TestAuditChain_PreChainRows(t *testing.T) {
	s := openTest(t)

	// Insert a legacy row with NULL entry_hash directly.
	_, err := s.db.Exec(`INSERT INTO admin_audit (ts, api_key_id, team_id, action, target_type, target_id, detail, ip_address)
		VALUES (1000000, NULL, NULL, 'legacy_action', 'provider', 'old', '', '')`)
	if err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	// Now log a new hashed entry.
	if err := s.LogAdminAction("new_action", "provider", "fresh", nil); err != nil {
		t.Fatalf("LogAdminAction: %v", err)
	}

	res, err := s.VerifyAuditChain()
	if err != nil {
		t.Fatalf("VerifyAuditChain: %v", err)
	}
	if res.TotalRows != 2 {
		t.Errorf("expected total_rows=2, got %d", res.TotalRows)
	}
	if res.HashedRows != 1 {
		t.Errorf("expected hashed_rows=1 (only the new row), got %d", res.HashedRows)
	}
	if !res.OK {
		t.Errorf("expected OK=true, got OK=false")
	}
}

package mcp

import (
	"encoding/json"
	"testing"

	"github.com/panda/llm-gateway/internal/store"
)

func TestVerifyAuditChain_ResponseShape(t *testing.T) {
	st := openTestStore(t)
	h := verifyAuditChainHandler(st)

	// Empty chain: no rows → ok=true, all zeros.
	res, err := h(ctxWithScope("mcp_auditor"), callTool(map[string]any{}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected no error, got: %v", res.Content)
	}

	var out store.AuditChainResult
	if err := json.Unmarshal([]byte(textOf(t, res)), &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !out.OK {
		t.Errorf("expected ok=true on empty chain, got false")
	}
	if out.TotalRows != 0 {
		t.Errorf("expected total_rows=0, got %d", out.TotalRows)
	}
	if out.HashedRows != 0 {
		t.Errorf("expected hashed_rows=0, got %d", out.HashedRows)
	}
	if out.InvalidRows != 0 {
		t.Errorf("expected invalid_rows=0, got %d", out.InvalidRows)
	}
}

func TestVerifyAuditChain_WithEntries(t *testing.T) {
	st := openTestStore(t)
	h := verifyAuditChainHandler(st)

	if err := st.LogAdminAction("add_provider", "provider", "p1", nil); err != nil {
		t.Fatalf("LogAdminAction: %v", err)
	}
	if err := st.LogAdminAction("issue_api_key", "api_key", "ak1", nil); err != nil {
		t.Fatalf("LogAdminAction: %v", err)
	}

	res, err := h(ctxWithScope("mcp_auditor"), callTool(map[string]any{}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected no error, got: %v", res.Content)
	}

	var out store.AuditChainResult
	if err := json.Unmarshal([]byte(textOf(t, res)), &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !out.OK {
		t.Errorf("expected ok=true, got false (invalid_rows=%d)", out.InvalidRows)
	}
	if out.TotalRows != 2 {
		t.Errorf("expected total_rows=2, got %d", out.TotalRows)
	}
	if out.HashedRows != 2 {
		t.Errorf("expected hashed_rows=2, got %d", out.HashedRows)
	}
}

func TestVerifyAuditChain_ACL_AuditorCanVerify(t *testing.T) {
	st := openTestStore(t)
	h := verifyAuditChainHandler(st)

	res, err := h(ctxWithScope("mcp_auditor"), callTool(map[string]any{}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Errorf("auditor should be able to verify audit chain: %v", res.Content)
	}
}

func TestVerifyAuditChain_ACL_InboundDenied(t *testing.T) {
	st := openTestStore(t)
	h := verifyAuditChainHandler(st)

	res, err := h(ctxWithScope("inbound"), callTool(map[string]any{}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Error("inbound scope should be denied verify_audit_chain")
	}
}

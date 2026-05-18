package mcp

// prompts_test.go covers T10: MCP preset ops prompts.
// Tests invoke prompt handlers directly (not through the MCP server wire).

import (
	"context"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
)

// promptReq builds a GetPromptRequest for a named prompt with given args.
func promptReq(name string, args map[string]string) mcplib.GetPromptRequest {
	return mcplib.GetPromptRequest{
		Params: mcplib.GetPromptParams{
			Name:      name,
			Arguments: args,
		},
	}
}

// firstMsgText returns the text of the first message in a GetPromptResult.
func firstMsgText(t *testing.T, res *mcplib.GetPromptResult) string {
	t.Helper()
	if res == nil {
		t.Fatal("GetPromptResult is nil")
	}
	if len(res.Messages) == 0 {
		t.Fatal("GetPromptResult has no messages")
	}
	tc, ok := res.Messages[0].Content.(mcplib.TextContent)
	if !ok {
		t.Fatalf("expected TextContent in first message, got %T", res.Messages[0].Content)
	}
	return tc.Text
}

// --- investigate_traffic_spike ---

func TestPrompt_InvestigateTrafficSpike_AuthRequired(t *testing.T) {
	h := investigateTrafficSpikeHandler()
	_, err := h(context.Background(), promptReq("investigate_traffic_spike", map[string]string{
		"team_id": "t1", "since": "2026-05-01T00:00:00Z",
	}))
	if err == nil {
		t.Error("expected error for unauthenticated caller")
	}
}

func TestPrompt_InvestigateTrafficSpike_ReturnsMessages(t *testing.T) {
	h := investigateTrafficSpikeHandler()
	res, err := h(ctxWithScope("mcp_auditor"), promptReq("investigate_traffic_spike", map[string]string{
		"team_id": "t1", "since": "2026-05-01T00:00:00Z",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Messages) == 0 {
		t.Error("expected at least one message")
	}
}

func TestPrompt_InvestigateTrafficSpike_InterpolatesArgs(t *testing.T) {
	h := investigateTrafficSpikeHandler()
	res, err := h(ctxWithScope("mcp_auditor"), promptReq("investigate_traffic_spike", map[string]string{
		"team_id": "acme", "since": "2026-05-17T00:00:00Z",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := firstMsgText(t, res)
	if !strings.Contains(text, "acme") {
		t.Errorf("expected team_id 'acme' in message text, got: %s", text)
	}
	if !strings.Contains(text, "2026-05-17T00:00:00Z") {
		t.Errorf("expected 'since' value in message text, got: %s", text)
	}
}

func TestPrompt_InvestigateTrafficSpike_MissingRequiredArg(t *testing.T) {
	h := investigateTrafficSpikeHandler()
	_, err := h(ctxWithScope("mcp_auditor"), promptReq("investigate_traffic_spike", map[string]string{
		"team_id": "t1",
		// "since" intentionally omitted
	}))
	if err == nil {
		t.Error("expected error when required argument 'since' is missing")
	}
}

// --- audit_who_used ---

func TestPrompt_AuditWhoUsed_AuthRequired(t *testing.T) {
	h := auditWhoUsedHandler()
	_, err := h(context.Background(), promptReq("audit_who_used", map[string]string{
		"model": "deepseek-chat", "since": "2026-05-01T00:00:00Z",
	}))
	if err == nil {
		t.Error("expected error for unauthenticated caller")
	}
}

func TestPrompt_AuditWhoUsed_InterpolatesArgs(t *testing.T) {
	h := auditWhoUsedHandler()
	res, err := h(ctxWithScope("mcp_auditor"), promptReq("audit_who_used", map[string]string{
		"model": "deepseek-chat", "since": "2026-05-01T00:00:00Z",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := firstMsgText(t, res)
	if !strings.Contains(text, "deepseek-chat") {
		t.Errorf("expected model name in message text, got: %s", text)
	}
	if !strings.Contains(text, "2026-05-01T00:00:00Z") {
		t.Errorf("expected since value in message text, got: %s", text)
	}
}

func TestPrompt_AuditWhoUsed_MissingModel(t *testing.T) {
	h := auditWhoUsedHandler()
	_, err := h(ctxWithScope("mcp_auditor"), promptReq("audit_who_used", map[string]string{
		"since": "2026-05-01T00:00:00Z",
		// "model" intentionally omitted
	}))
	if err == nil {
		t.Error("expected error when required argument 'model' is missing")
	}
}

// --- cost_review ---

func TestPrompt_CostReview_AuthRequired(t *testing.T) {
	h := costReviewHandler()
	_, err := h(context.Background(), promptReq("cost_review", map[string]string{
		"team_id": "t1", "period": "2026-05",
	}))
	if err == nil {
		t.Error("expected error for unauthenticated caller")
	}
}

func TestPrompt_CostReview_InterpolatesArgs(t *testing.T) {
	h := costReviewHandler()
	res, err := h(ctxWithScope("mcp_auditor"), promptReq("cost_review", map[string]string{
		"team_id": "acme", "period": "2026-05",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := firstMsgText(t, res)
	if !strings.Contains(text, "acme") {
		t.Errorf("expected team_id in message text, got: %s", text)
	}
	if !strings.Contains(text, "2026-05") {
		t.Errorf("expected period in message text, got: %s", text)
	}
}

func TestPrompt_CostReview_MissingPeriod(t *testing.T) {
	h := costReviewHandler()
	_, err := h(ctxWithScope("mcp_auditor"), promptReq("cost_review", map[string]string{
		"team_id": "t1",
		// "period" intentionally omitted
	}))
	if err == nil {
		t.Error("expected error when required argument 'period' is missing")
	}
}

// --- route_review ---

func TestPrompt_RouteReview_AuthRequired(t *testing.T) {
	h := routeReviewHandler()
	_, err := h(context.Background(), promptReq("route_review", map[string]string{
		"team_id": "t1", "since": "2026-05-01T00:00:00Z",
	}))
	if err == nil {
		t.Error("expected error for unauthenticated caller")
	}
}

func TestPrompt_RouteReview_InterpolatesArgs(t *testing.T) {
	h := routeReviewHandler()
	res, err := h(ctxWithScope("mcp_auditor"), promptReq("route_review", map[string]string{
		"team_id": "acme", "since": "2026-05-01T00:00:00Z",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := firstMsgText(t, res)
	if !strings.Contains(text, "acme") {
		t.Errorf("expected team_id in message text, got: %s", text)
	}
	if !strings.Contains(text, "2026-05-01T00:00:00Z") {
		t.Errorf("expected since value in message text, got: %s", text)
	}
}

func TestPrompt_RouteReview_MissingSince(t *testing.T) {
	h := routeReviewHandler()
	_, err := h(ctxWithScope("mcp_auditor"), promptReq("route_review", map[string]string{
		"team_id": "t1",
		// "since" intentionally omitted
	}))
	if err == nil {
		t.Error("expected error when required argument 'since' is missing")
	}
}

// --- provider_health_check ---

func TestPrompt_ProviderHealthCheck_AuthRequired(t *testing.T) {
	h := providerHealthCheckHandler()
	_, err := h(context.Background(), promptReq("provider_health_check", nil))
	if err == nil {
		t.Error("expected error for unauthenticated caller")
	}
}

func TestPrompt_ProviderHealthCheck_ReturnsMessages(t *testing.T) {
	h := providerHealthCheckHandler()
	res, err := h(ctxWithScope("mcp_auditor"), promptReq("provider_health_check", nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Messages) == 0 {
		t.Error("expected at least one message")
	}
	text := firstMsgText(t, res)
	if !strings.Contains(text, "get_provider_health") {
		t.Errorf("expected instructions to call get_provider_health, got: %s", text)
	}
}

// --- add_provider_wizard ---

func TestPrompt_AddProviderWizard_AuthRequired(t *testing.T) {
	h := addProviderWizardHandler()
	_, err := h(context.Background(), promptReq("add_provider_wizard", map[string]string{
		"kind": "deepseek",
	}))
	if err == nil {
		t.Error("expected error for unauthenticated caller")
	}
}

func TestPrompt_AddProviderWizard_InterpolatesKind(t *testing.T) {
	h := addProviderWizardHandler()
	res, err := h(ctxWithScope("mcp_admin"), promptReq("add_provider_wizard", map[string]string{
		"kind": "deepseek",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := firstMsgText(t, res)
	if !strings.Contains(text, "deepseek") {
		t.Errorf("expected kind 'deepseek' in message text, got: %s", text)
	}
	if !strings.Contains(text, "add_provider") {
		t.Errorf("expected instructions to call add_provider, got: %s", text)
	}
}

func TestPrompt_AddProviderWizard_MissingKind(t *testing.T) {
	h := addProviderWizardHandler()
	_, err := h(ctxWithScope("mcp_admin"), promptReq("add_provider_wizard", map[string]string{}))
	if err == nil {
		t.Error("expected error when required argument 'kind' is missing")
	}
}

// --- RegisterPrompts: smoke test that all 6 prompts register without panic ---

func TestRegisterPrompts_Smoke(t *testing.T) {
	s := Build()
	st := openTestStore(t)
	// Must not panic
	RegisterPrompts(s, st)
}

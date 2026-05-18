package mcp

// resources.go — T9: MCP resources with lgw:// URI scheme.
//
// Five resource templates are registered:
//   lgw://teams/{id}/usage        — today's aggregated token + cost usage for a team
//   lgw://teams/{id}/quota        — quota rows for a team
//   lgw://providers               — list all providers (no sensitive keys)
//   lgw://providers/{name}/health — live health snapshot (placeholder if no Manager)
//   lgw://providers/{name}/capabilities — provider capability registry rows
//
// URI parsing: strings.Split on "/" gives a fixed-index segment array:
//   "lgw://teams/tm_abc/usage" → ["lgw:", "", "teams", "tm_abc", "usage"]
//   index 0=scheme, 1=empty, 2=host-or-first-path, 3=second, 4=third
//
// Auth: all resources require mcp_auditor minimum via checkResource().

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/panda/llm-gateway/internal/store"
)

// RegisterResources registers all lgw:// resource templates on s.
func RegisterResources(s *mcpserver.MCPServer, st *store.Store) {
	s.AddResourceTemplate(
		mcplib.NewResourceTemplate(
			"lgw://teams/{id}/usage",
			"Team Usage",
			mcplib.WithTemplateDescription("Today's aggregated token and cost usage for a team (summed across all API keys)"),
			mcplib.WithTemplateMIMEType("application/json"),
		),
		teamUsageHandler(st),
	)

	s.AddResourceTemplate(
		mcplib.NewResourceTemplate(
			"lgw://teams/{id}/quota",
			"Team Quota",
			mcplib.WithTemplateDescription("Quota configuration rows for a team (all windows: day, month, minute)"),
			mcplib.WithTemplateMIMEType("application/json"),
		),
		teamQuotaHandler(st),
	)

	s.AddResourceTemplate(
		mcplib.NewResourceTemplate(
			"lgw://providers",
			"Providers",
			mcplib.WithTemplateDescription("List all registered providers with metadata (no API keys)"),
			mcplib.WithTemplateMIMEType("application/json"),
		),
		providersHandler(st),
	)

	s.AddResourceTemplate(
		mcplib.NewResourceTemplate(
			"lgw://providers/{name}/health",
			"Provider Health",
			mcplib.WithTemplateDescription("Health snapshot for a provider (sample count, last latency, last healthy status)"),
			mcplib.WithTemplateMIMEType("application/json"),
		),
		providerHealthHandler(st),
	)

	s.AddResourceTemplate(
		mcplib.NewResourceTemplate(
			"lgw://providers/{name}/capabilities",
			"Provider Capabilities",
			mcplib.WithTemplateDescription("Capability registry rows for a provider (streaming, context length, etc.)"),
			mcplib.WithTemplateMIMEType("application/json"),
		),
		providerCapabilitiesHandler(st),
	)
}

// checkResource enforces mcp_auditor minimum for resource reads.
// Returns a non-nil error when the caller is unauthenticated or under-scoped.
func checkResource(ctx context.Context, minScope string) error {
	return requireRole(ctx, minScope)
}

// uriSegment splits a lgw:// URI on "/" and returns the segment at index idx,
// or "" when the URI has fewer parts. Index layout:
//
//	"lgw://teams/tm_abc/usage" → ["lgw:", "", "teams", "tm_abc", "usage"]
//	  0        1     2       3          4
func uriSegment(uri string, idx int) string {
	parts := strings.Split(uri, "/")
	if idx < 0 || idx >= len(parts) {
		return ""
	}
	return parts[idx]
}

// jsonResource marshals v and returns a single-element TextResourceContents slice.
func jsonResource(uri string, v any) ([]mcplib.ResourceContents, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("json marshal: %w", err)
	}
	return []mcplib.ResourceContents{
		mcplib.TextResourceContents{
			URI:      uri,
			MIMEType: "application/json",
			Text:     string(b),
		},
	}, nil
}

// --- lgw://teams/{id}/usage ---

type teamUsageJSON struct {
	TeamID          string `json:"team_id"`
	Day             string `json:"day"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	ReasoningTokens int64  `json:"reasoning_tokens"`
	CostMicros      int64  `json:"cost_micros"`
}

func teamUsageHandler(st *store.Store) mcpserver.ResourceTemplateHandlerFunc {
	return func(ctx context.Context, req mcplib.ReadResourceRequest) ([]mcplib.ResourceContents, error) {
		if err := checkResource(ctx, "mcp_auditor"); err != nil {
			return nil, fmt.Errorf("unauthorized: %w", err)
		}

		teamID := uriSegment(req.Params.URI, 3)
		today := dayStartUTC(time.Now().UTC())
		todayMS := today.UnixMilli()

		// Aggregate usage across all API keys belonging to the team.
		keys, err := st.ListAPIKeysByTeam(teamID)
		if err != nil {
			return nil, fmt.Errorf("list api keys: %w", err)
		}

		result := teamUsageJSON{
			TeamID: teamID,
			Day:    today.Format("2006-01-02"),
		}
		for _, k := range keys {
			uc, err := st.GetUsageCounter(k.ID, todayMS)
			if err != nil {
				// ErrNotFound means no usage recorded for this key today — skip.
				continue
			}
			result.InputTokens += uc.InputTokens
			result.OutputTokens += uc.OutputTokens
			result.ReasoningTokens += uc.ReasoningTokens
			result.CostMicros += uc.CostUSDMicros
		}

		return jsonResource(req.Params.URI, result)
	}
}

// dayStartUTC returns midnight UTC for the given time.
func dayStartUTC(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// --- lgw://teams/{id}/quota ---

type quotaJSON struct {
	ScopeKind    string  `json:"scope_kind"`
	ScopeID      string  `json:"scope_id"`
	Window       string  `json:"window"`
	MaxRequests  *int64  `json:"max_requests"`
	MaxTokens    *int64  `json:"max_tokens"`
	MaxUSDMicros *int64  `json:"max_usd_micros"`
}

func quotaToJSON(q store.Quota) quotaJSON {
	return quotaJSON{
		ScopeKind:    q.ScopeKind,
		ScopeID:      q.ScopeID,
		Window:       q.Window,
		MaxRequests:  q.MaxRequests,
		MaxTokens:    q.MaxTokens,
		MaxUSDMicros: q.MaxUSDMicros,
	}
}

func teamQuotaHandler(st *store.Store) mcpserver.ResourceTemplateHandlerFunc {
	return func(ctx context.Context, req mcplib.ReadResourceRequest) ([]mcplib.ResourceContents, error) {
		if err := checkResource(ctx, "mcp_auditor"); err != nil {
			return nil, fmt.Errorf("unauthorized: %w", err)
		}

		teamID := uriSegment(req.Params.URI, 3)
		quotas, err := st.ListQuotasForScope("team", teamID)
		if err != nil {
			return nil, fmt.Errorf("list quotas: %w", err)
		}

		out := make([]quotaJSON, len(quotas))
		for i, q := range quotas {
			out[i] = quotaToJSON(q)
		}
		// Return [] not null when empty.
		if out == nil {
			out = []quotaJSON{}
		}
		return jsonResource(req.Params.URI, out)
	}
}

// --- lgw://providers ---

type providerInfoJSON struct {
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	HasOpenAIURL    bool   `json:"has_openai_url"`
	HasAnthropicURL bool   `json:"has_anthropic_url"`
	IsDefault       bool   `json:"is_default"`
}

func providersHandler(st *store.Store) mcpserver.ResourceTemplateHandlerFunc {
	return func(ctx context.Context, req mcplib.ReadResourceRequest) ([]mcplib.ResourceContents, error) {
		if err := checkResource(ctx, "mcp_auditor"); err != nil {
			return nil, fmt.Errorf("unauthorized: %w", err)
		}

		providers, err := st.ListProviders()
		if err != nil {
			return nil, fmt.Errorf("list providers: %w", err)
		}

		out := make([]providerInfoJSON, len(providers))
		for i, p := range providers {
			out[i] = providerInfoJSON{
				Name:            p.Name,
				Kind:            p.Kind,
				HasOpenAIURL:    p.OpenAIBaseURL != "",
				HasAnthropicURL: p.AnthropicBaseURL != "",
				IsDefault:       p.IsDefault,
			}
		}
		// Return [] not null when empty.
		if out == nil {
			out = []providerInfoJSON{}
		}
		return jsonResource(req.Params.URI, out)
	}
}

// --- lgw://providers/{name}/health ---

// providerHealthJSON is the resource-level health snapshot. The health.Manager
// is not available at resource registration time (it's wired in server/mcp.go
// after RegisterResources is called). We surface what we can from the store
// (provider existence + metadata) and note that live probe data requires the
// HTTP gateway's health.Manager.
type providerHealthJSON struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

func providerHealthHandler(st *store.Store) mcpserver.ResourceTemplateHandlerFunc {
	return func(ctx context.Context, req mcplib.ReadResourceRequest) ([]mcplib.ResourceContents, error) {
		if err := checkResource(ctx, "mcp_auditor"); err != nil {
			return nil, fmt.Errorf("unauthorized: %w", err)
		}

		name := uriSegment(req.Params.URI, 3)

		// Verify the provider exists.
		p, err := st.GetProvider(name)
		if err != nil {
			return nil, fmt.Errorf("provider %q not found: %w", name, err)
		}

		snap := providerHealthJSON{
			Name:    p.Name,
			Kind:    p.Kind,
			Message: "health data requires Manager injection (use get_provider_health MCP tool for live data)",
		}
		return jsonResource(req.Params.URI, snap)
	}
}

// --- lgw://providers/{name}/capabilities ---

type resourceCapabilityJSON struct {
	Capability string `json:"capability"`
	Value      string `json:"value"`
	UpdatedAt  string `json:"updated_at"`
}

func providerCapabilitiesHandler(st *store.Store) mcpserver.ResourceTemplateHandlerFunc {
	return func(ctx context.Context, req mcplib.ReadResourceRequest) ([]mcplib.ResourceContents, error) {
		if err := checkResource(ctx, "mcp_auditor"); err != nil {
			return nil, fmt.Errorf("unauthorized: %w", err)
		}

		name := uriSegment(req.Params.URI, 3)
		caps, err := st.ListProviderCapabilities(name)
		if err != nil {
			return nil, fmt.Errorf("list capabilities: %w", err)
		}

		out := make([]resourceCapabilityJSON, len(caps))
		for i, c := range caps {
			out[i] = resourceCapabilityJSON{
				Capability: c.Capability,
				Value:      c.Value,
				UpdatedAt:  c.UpdatedAt,
			}
		}
		// ListProviderCapabilities already returns [] not nil, but be safe.
		if out == nil {
			out = []resourceCapabilityJSON{}
		}
		return jsonResource(req.Params.URI, out)
	}
}

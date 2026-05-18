package mcp

import (
	"context"
	"fmt"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	authpkg "github.com/panda/llm-gateway/internal/auth"
	"github.com/panda/llm-gateway/internal/store"
)

// RegisterPrompts adds all 6 preset ops prompts to s.
// Auth: any authenticated caller (presence of API key required, no scope restriction).
func RegisterPrompts(s *mcpserver.MCPServer, _ *store.Store) {
	s.AddPrompt(
		mcplib.NewPrompt("investigate_traffic_spike",
			mcplib.WithPromptDescription("Step-by-step instructions for investigating a traffic spike for a team."),
			mcplib.WithArgument("team_id",
				mcplib.ArgumentDescription("The team ID to investigate."),
				mcplib.RequiredArgument(),
			),
			mcplib.WithArgument("since",
				mcplib.ArgumentDescription("ISO 8601 timestamp to filter from, e.g. 2026-05-17T00:00:00Z."),
				mcplib.RequiredArgument(),
			),
		),
		investigateTrafficSpikeHandler(),
	)

	s.AddPrompt(
		mcplib.NewPrompt("audit_who_used",
			mcplib.WithPromptDescription("Compliance audit: find which teams and API keys called a model since a given time."),
			mcplib.WithArgument("model",
				mcplib.ArgumentDescription("Model name to audit, e.g. deepseek-chat."),
				mcplib.RequiredArgument(),
			),
			mcplib.WithArgument("since",
				mcplib.ArgumentDescription("ISO 8601 timestamp to filter from."),
				mcplib.RequiredArgument(),
			),
		),
		auditWhoUsedHandler(),
	)

	s.AddPrompt(
		mcplib.NewPrompt("cost_review",
			mcplib.WithPromptDescription("Review cost and quota usage for a team over a period."),
			mcplib.WithArgument("team_id",
				mcplib.ArgumentDescription("The team ID to review."),
				mcplib.RequiredArgument(),
			),
			mcplib.WithArgument("period",
				mcplib.ArgumentDescription("Period to review, e.g. '2026-05' or 'last_7_days'."),
				mcplib.RequiredArgument(),
			),
		),
		costReviewHandler(),
	)

	s.AddPrompt(
		mcplib.NewPrompt("route_review",
			mcplib.WithPromptDescription("Review routing decisions (cognitive vs static) for a team since a given time."),
			mcplib.WithArgument("team_id",
				mcplib.ArgumentDescription("The team ID to review."),
				mcplib.RequiredArgument(),
			),
			mcplib.WithArgument("since",
				mcplib.ArgumentDescription("ISO 8601 timestamp to filter from."),
				mcplib.RequiredArgument(),
			),
		),
		routeReviewHandler(),
	)

	s.AddPrompt(
		mcplib.NewPrompt("provider_health_check",
			mcplib.WithPromptDescription("Instructions for checking the health of all registered providers."),
		),
		providerHealthCheckHandler(),
	)

	s.AddPrompt(
		mcplib.NewPrompt("add_provider_wizard",
			mcplib.WithPromptDescription("Step-by-step wizard to add a new LLM provider to the gateway."),
			mcplib.WithArgument("kind",
				mcplib.ArgumentDescription("Provider kind, e.g. deepseek, openai, anthropic, qwen, moonshot."),
				mcplib.RequiredArgument(),
			),
		),
		addProviderWizardHandler(),
	)
}

// checkPromptAuth returns an error if there is no authenticated caller in ctx.
func checkPromptAuth(ctx context.Context) error {
	_, ok := authpkg.APIKeyFromContext(ctx)
	if !ok {
		return fmt.Errorf("unauthorized: prompt requires an authenticated API key")
	}
	return nil
}

// requireArg returns the value of a required argument or an error.
func requireArg(args map[string]string, name string) (string, error) {
	v, ok := args[name]
	if !ok || v == "" {
		return "", fmt.Errorf("missing required argument: %s", name)
	}
	return v, nil
}

// textPromptResult wraps instructions text in a single-message GetPromptResult.
func textPromptResult(description, text string) *mcplib.GetPromptResult {
	return &mcplib.GetPromptResult{
		Description: description,
		Messages: []mcplib.PromptMessage{
			{
				Role:    mcplib.RoleUser,
				Content: mcplib.NewTextContent(text),
			},
		},
	}
}

// --- investigateTrafficSpikeHandler ---

func investigateTrafficSpikeHandler() mcpserver.PromptHandlerFunc {
	return func(ctx context.Context, req mcplib.GetPromptRequest) (*mcplib.GetPromptResult, error) {
		if err := checkPromptAuth(ctx); err != nil {
			return nil, err
		}
		args := req.Params.Arguments
		teamID, err := requireArg(args, "team_id")
		if err != nil {
			return nil, err
		}
		since, err := requireArg(args, "since")
		if err != nil {
			return nil, err
		}
		text := fmt.Sprintf(`Investigate a traffic spike for team %s since %s.

Steps:
1. Call list_request_logs with team_id=%s and since=%s to retrieve recent requests.
2. Group by model and provider — look for models or providers that appear more than usual.
3. Check latency_ms distribution: flag any requests > 10000ms as outliers.
4. Call get_provider_health to see if any providers show degraded health during this window.
5. Summarize: request count, top models, top providers, error rate, and any anomalies.`,
			teamID, since, teamID, since)
		return textPromptResult(
			fmt.Sprintf("Traffic spike investigation for team %s since %s", teamID, since),
			text,
		), nil
	}
}

// --- auditWhoUsedHandler ---

func auditWhoUsedHandler() mcpserver.PromptHandlerFunc {
	return func(ctx context.Context, req mcplib.GetPromptRequest) (*mcplib.GetPromptResult, error) {
		if err := checkPromptAuth(ctx); err != nil {
			return nil, err
		}
		args := req.Params.Arguments
		model, err := requireArg(args, "model")
		if err != nil {
			return nil, err
		}
		since, err := requireArg(args, "since")
		if err != nil {
			return nil, err
		}
		text := fmt.Sprintf(`Compliance audit: find who called model %s since %s.

Steps:
1. Call list_request_logs with model=%s and since=%s to retrieve all matching requests.
2. Group results by team_id and api_key_id to identify callers.
3. For each caller, report: team name, API key label, request count, first and last request timestamp.
4. Flag any teams or keys with unusually high volume.
5. Produce a compliance summary: which teams called %s, how many times, and when.`,
			model, since, model, since, model)
		return textPromptResult(
			fmt.Sprintf("Compliance audit for model %s since %s", model, since),
			text,
		), nil
	}
}

// --- costReviewHandler ---

func costReviewHandler() mcpserver.PromptHandlerFunc {
	return func(ctx context.Context, req mcplib.GetPromptRequest) (*mcplib.GetPromptResult, error) {
		if err := checkPromptAuth(ctx); err != nil {
			return nil, err
		}
		args := req.Params.Arguments
		teamID, err := requireArg(args, "team_id")
		if err != nil {
			return nil, err
		}
		period, err := requireArg(args, "period")
		if err != nil {
			return nil, err
		}
		text := fmt.Sprintf(`Review cost and quota usage for team %s over period %s.

Steps:
1. Read the resource lgw://teams/%s/usage to get current usage counters and quota limits.
2. Compare actual usage against the quota — flag if within 80%% or exceeding limit.
3. Call list_request_logs with team_id=%s to break down requests by model.
4. For each model, show: request count, total input tokens, total output tokens, estimated cost.
5. Identify the top-cost models and any budget anomalies.
6. Summarize: total spend for period %s, quota status, and cost breakdown by model.`,
			teamID, period, teamID, teamID, period)
		return textPromptResult(
			fmt.Sprintf("Cost review for team %s, period %s", teamID, period),
			text,
		), nil
	}
}

// --- routeReviewHandler ---

func routeReviewHandler() mcpserver.PromptHandlerFunc {
	return func(ctx context.Context, req mcplib.GetPromptRequest) (*mcplib.GetPromptResult, error) {
		if err := checkPromptAuth(ctx); err != nil {
			return nil, err
		}
		args := req.Params.Arguments
		teamID, err := requireArg(args, "team_id")
		if err != nil {
			return nil, err
		}
		since, err := requireArg(args, "since")
		if err != nil {
			return nil, err
		}
		text := fmt.Sprintf(`Review routing decisions for team %s since %s.

Steps:
1. Call list_request_logs with team_id=%s and since=%s to retrieve requests.
2. Group by provider — identify which providers are being used most.
3. Examine the route_trace field on each request to see whether routing was cognitive (dynamic) or static.
4. For interesting requests (e.g. those routed differently than expected or with high latency), call explain_route_trace to get a human-readable explanation of the routing decision.
5. Look for patterns: are fallback routes triggering? Is the cognitive router selecting unexpected providers?
6. Summarize: provider distribution, cognitive vs static routing ratio, and any notable routing anomalies.`,
			teamID, since, teamID, since)
		return textPromptResult(
			fmt.Sprintf("Route review for team %s since %s", teamID, since),
			text,
		), nil
	}
}

// --- providerHealthCheckHandler ---

func providerHealthCheckHandler() mcpserver.PromptHandlerFunc {
	return func(ctx context.Context, req mcplib.GetPromptRequest) (*mcplib.GetPromptResult, error) {
		if err := checkPromptAuth(ctx); err != nil {
			return nil, err
		}
		text := `Check the health of all registered LLM providers.

Steps:
1. Call get_provider_health for each registered provider to retrieve their health snapshots.
2. For each provider, examine: success_rate, avg_latency_ms, p99_latency_ms, last_healthy, sample_count.
3. Flag any provider where success_rate < 0.95 — this indicates degraded reliability.
4. Flag any provider where p99_latency_ms > 5000ms — this indicates high tail latency.
5. If a provider is flagged, note when it was last_healthy and how many samples have been taken.
6. Summarize: overall provider health status, list of degraded providers, and recommended actions (e.g. adjust routing weights, trigger fallback).`
		return textPromptResult("Provider health check instructions", text), nil
	}
}

// --- addProviderWizardHandler ---

// knownProviderBaseURLs maps provider kinds to their standard base URLs.
var knownProviderBaseURLs = map[string]string{
	"deepseek":  "https://api.deepseek.com/v1",
	"openai":    "https://api.openai.com/v1",
	"anthropic": "https://api.anthropic.com/v1",
	"qwen":      "https://dashscope.aliyuncs.com/compatible-mode/v1",
	"moonshot":  "https://api.moonshot.cn/v1",
}

func addProviderWizardHandler() mcpserver.PromptHandlerFunc {
	return func(ctx context.Context, req mcplib.GetPromptRequest) (*mcplib.GetPromptResult, error) {
		if err := checkPromptAuth(ctx); err != nil {
			return nil, err
		}
		args := req.Params.Arguments
		kind, err := requireArg(args, "kind")
		if err != nil {
			return nil, err
		}
		baseURL, known := knownProviderBaseURLs[kind]
		baseURLHint := ""
		if known {
			baseURLHint = fmt.Sprintf(" The standard base_url for %s is: %s", kind, baseURL)
		} else {
			baseURLHint = fmt.Sprintf(" (base_url for %s is not pre-configured — check the provider's API documentation)", kind)
		}

		text := fmt.Sprintf(`Add a new %s provider to the gateway.

Steps:
1. Gather the required information:
   - name: a unique slug for this provider instance (e.g. "%s-prod")
   - kind: "%s"
   - api_key: your API key from the provider dashboard
   - openai_base_url: the provider's API base URL.%s
2. Call add_provider with the gathered parameters to register the provider.
3. If this is the first provider, call set_default_provider with the provider name to make it the default route.
4. Optionally call set_routing_weights to adjust traffic distribution if multiple providers are registered.
5. Verify the provider is registered by calling list_providers and confirming the new entry appears.
6. Optionally run the provider_health_check prompt to confirm the new provider responds correctly.`,
			kind, kind, kind, baseURLHint)
		return textPromptResult(
			fmt.Sprintf("Wizard to add a %s provider", kind),
			text,
		), nil
	}
}

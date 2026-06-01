package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/panda/llm-gateway/internal/store"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// RegisterProviderTools adds the 7 provider + model-alias management tools
// to s. All tools require at least mcp_admin scope (enforced via checkTool).
// Call this after Build() in mcp_serve.go.
func RegisterProviderTools(s *mcpserver.MCPServer, st *store.Store) {
	s.AddTool(
		mcplib.NewTool("add_provider",
			mcplib.WithDescription("Add an upstream LLM provider. At least one of openai_base_url or anthropic_base_url is required."),
			mcplib.WithString("name", mcplib.Required(), mcplib.Description("Unique provider slug, e.g. 'deepseek' or 'glm-prod'")),
			mcplib.WithString("kind", mcplib.Required(), mcplib.Description("Provider kind: deepseek | glm | openai | anthropic | custom")),
			mcplib.WithString("api_key", mcplib.Required(), mcplib.Description("Provider API key (stored plaintext, chmod-600 config dir)")),
			mcplib.WithString("openai_base_url", mcplib.Description("OpenAI-compat base URL, e.g. https://api.deepseek.com")),
			mcplib.WithString("anthropic_base_url", mcplib.Description("Anthropic-compat base URL, e.g. https://api.deepseek.com/anthropic")),
			mcplib.WithString("anthropic_version", mcplib.Description("anthropic-version header value (default: 2023-06-01)")),
			mcplib.WithBoolean("is_default", mcplib.Description("Make this the default provider for unrouted requests")),
		),
		addProviderHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("remove_provider",
			mcplib.WithDescription("Remove a provider and cascade-delete its model aliases. Fails if provider has active routes."),
			mcplib.WithString("name", mcplib.Required(), mcplib.Description("Provider slug to remove")),
		),
		removeProviderHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("list_providers",
			mcplib.WithDescription("List all registered providers. API keys are masked."),
		),
		listProvidersHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("set_default_provider",
			mcplib.WithDescription("Set the default provider used when no model alias matches. Clears the previous default."),
			mcplib.WithString("name", mcplib.Required(), mcplib.Description("Provider slug to make default")),
		),
		setDefaultProviderHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("set_model_alias",
			mcplib.WithDescription("Create or update a global model alias. The alias maps a short name (e.g. 'fast') to a provider + upstream model."),
			mcplib.WithString("alias", mcplib.Required(), mcplib.Description("Short alias name, e.g. 'fast' or 'smart'")),
			mcplib.WithString("provider_name", mcplib.Required(), mcplib.Description("Provider slug this alias routes to")),
			mcplib.WithString("upstream_model", mcplib.Required(), mcplib.Description("Actual model name at the provider, e.g. 'deepseek-v4-flash'")),
			mcplib.WithNumber("context_length", mcplib.Description("Context window in tokens (optional). Returned in /v1/models for agent model-selection.")),
			mcplib.WithNumber("max_completion_tokens", mcplib.Description("Max output tokens (optional). Returned in /v1/models for agent model-selection.")),
			mcplib.WithString("mode", mcplib.Description("Routing mode: 'static' (default — fixed provider) or 'cognitive' (v0.3 T3 — router scores eligible providers and picks the highest)")),
		),
		setModelAliasHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("delete_model_alias",
			mcplib.WithDescription("Delete a global model alias."),
			mcplib.WithString("alias", mcplib.Required(), mcplib.Description("Alias name to delete")),
		),
		deleteModelAliasHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("list_model_aliases",
			mcplib.WithDescription("List all global model aliases with their provider and upstream model."),
		),
		listModelAliasesHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("set_model_cost",
			mcplib.WithDescription("Set pricing for a provider+model pair. Inserts a new effective-from row; history is preserved. Prices are USD per 1K tokens."),
			mcplib.WithString("provider", mcplib.Required(), mcplib.Description("Provider slug, e.g. 'deepseek'")),
			mcplib.WithString("model", mcplib.Required(), mcplib.Description("Upstream model name, e.g. 'deepseek-v4-flash'")),
			mcplib.WithNumber("usd_per_input_1k", mcplib.Required(), mcplib.Description("Input token price in USD per 1K tokens")),
			mcplib.WithNumber("usd_per_output_1k", mcplib.Required(), mcplib.Description("Output token price in USD per 1K tokens")),
			mcplib.WithNumber("usd_per_reasoning_1k", mcplib.Description("Reasoning token price in USD per 1K tokens (optional, for reasoning models)")),
			mcplib.WithNumber("usd_per_cached_1k", mcplib.Description("Prompt-cache hit price in USD per 1K tokens (optional, for prompt-caching models). Cached portion of prompt_tokens bills at this rate; uncached portion bills at usd_per_input_1k.")),
		),
		setModelCostHandler(st),
	)
	s.AddTool(
		mcplib.NewTool("list_model_costs",
			mcplib.WithDescription("List current pricing for all provider+model pairs (latest effective row per pair)."),
		),
		listModelCostsHandler(st),
	)
}

// --- handler factories ---

func addProviderHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "add_provider"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		name := req.GetString("name", "")
		kind := req.GetString("kind", "")
		apiKey := req.GetString("api_key", "")
		if name == "" || kind == "" || apiKey == "" {
			return mcplib.NewToolResultError("required parameters missing: name, kind, api_key"), nil
		}
		p := store.Provider{
			Name:             name,
			Kind:             kind,
			APIKey:           apiKey,
			OpenAIBaseURL:    req.GetString("openai_base_url", ""),
			AnthropicBaseURL: req.GetString("anthropic_base_url", ""),
			AnthropicVersion: req.GetString("anthropic_version", ""),
			IsDefault:        req.GetBool("is_default", false),
		}
		if err := st.AddProvider(p); err != nil {
			if errors.Is(err, store.ErrDuplicate) {
				return mcplib.NewToolResultError("provider already exists: " + name), nil
			}
			return mcplib.NewToolResultError("add_provider failed: " + err.Error()), nil
		}
		if p.IsDefault {
			if err := st.SetDefaultProvider(name); err != nil {
				return mcplib.NewToolResultError("provider added but set_default failed: " + err.Error()), nil
			}
		}
		audit(st, "add_provider", "provider", name, map[string]string{"kind": kind})
		out, _ := json.Marshal(map[string]any{"ok": true, "name": name})
		return mcplib.NewToolResultText(string(out)), nil
	}
}

func removeProviderHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "remove_provider"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		name := req.GetString("name", "")
		if name == "" {
			return mcplib.NewToolResultError("required: name"), nil
		}
		if err := st.RemoveProvider(name); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return mcplib.NewToolResultError("provider not found: " + name), nil
			}
			return mcplib.NewToolResultError("remove_provider failed: " + err.Error()), nil
		}
		audit(st, "remove_provider", "provider", name, nil)
		out, _ := json.Marshal(map[string]any{"ok": true, "removed": name})
		return mcplib.NewToolResultText(string(out)), nil
	}
}

func listProvidersHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "list_providers"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		providers, err := st.ListProviders()
		if err != nil {
			return mcplib.NewToolResultError("list_providers failed: " + err.Error()), nil
		}
		type row struct {
			Name             string `json:"name"`
			Kind             string `json:"kind"`
			OpenAIBaseURL    string `json:"openai_base_url,omitempty"`
			AnthropicBaseURL string `json:"anthropic_base_url,omitempty"`
			APIKey           string `json:"api_key"`
			IsDefault        bool   `json:"is_default"`
		}
		out := make([]row, len(providers))
		for i, p := range providers {
			out[i] = row{
				Name:             p.Name,
				Kind:             p.Kind,
				OpenAIBaseURL:    p.OpenAIBaseURL,
				AnthropicBaseURL: p.AnthropicBaseURL,
				APIKey:           maskKey(p.APIKey),
				IsDefault:        p.IsDefault,
			}
		}
		b, _ := json.Marshal(out)
		return mcplib.NewToolResultText(string(b)), nil
	}
}

func setDefaultProviderHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "set_default_provider"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		name := req.GetString("name", "")
		if name == "" {
			return mcplib.NewToolResultError("required: name"), nil
		}
		if err := st.SetDefaultProvider(name); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return mcplib.NewToolResultError("provider not found: " + name), nil
			}
			return mcplib.NewToolResultError("set_default_provider failed: " + err.Error()), nil
		}
		audit(st, "set_default_provider", "provider", name, nil)
		out, _ := json.Marshal(map[string]any{"ok": true, "default": name})
		return mcplib.NewToolResultText(string(out)), nil
	}
}

func setModelAliasHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "set_model_alias"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		alias := req.GetString("alias", "")
		providerName := req.GetString("provider_name", "")
		upstreamModel := req.GetString("upstream_model", "")
		if alias == "" || providerName == "" || upstreamModel == "" {
			return mcplib.NewToolResultError("required: alias, provider_name, upstream_model"), nil
		}
		contextLength := optInt64(req.GetInt("context_length", 0))
		maxCompletionTokens := optInt64(req.GetInt("max_completion_tokens", 0))
		mode := req.GetString("mode", "static")
		if mode != "static" && mode != "cognitive" {
			return mcplib.NewToolResultError("invalid mode: " + mode + " (want 'static' or 'cognitive')"), nil
		}
		if err := st.SetAliasWithMode(alias, providerName, upstreamModel, contextLength, maxCompletionTokens, mode); err != nil {
			return mcplib.NewToolResultError("set_model_alias failed: " + err.Error()), nil
		}
		audit(st, "set_model_alias", "alias", alias, map[string]string{"provider": providerName, "upstream_model": upstreamModel, "mode": mode})
		out, _ := json.Marshal(map[string]any{
			"ok": true, "alias": alias,
			"provider": providerName, "upstream_model": upstreamModel, "mode": mode,
		})
		return mcplib.NewToolResultText(string(out)), nil
	}
}

func deleteModelAliasHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "delete_model_alias"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		alias := req.GetString("alias", "")
		if alias == "" {
			return mcplib.NewToolResultError("required: alias"), nil
		}
		if err := st.RemoveAlias(alias); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return mcplib.NewToolResultError("alias not found: " + alias), nil
			}
			return mcplib.NewToolResultError("delete_model_alias failed: " + err.Error()), nil
		}
		audit(st, "delete_model_alias", "alias", alias, nil)
		out, _ := json.Marshal(map[string]any{"ok": true, "deleted": alias})
		return mcplib.NewToolResultText(string(out)), nil
	}
}

func listModelAliasesHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "list_model_aliases"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		aliases, err := st.ListAliases()
		if err != nil {
			return mcplib.NewToolResultError("list_model_aliases failed: " + err.Error()), nil
		}
		type row struct {
			Alias               string `json:"alias"`
			ProviderName        string `json:"provider_name"`
			UpstreamModel       string `json:"upstream_model"`
			ContextLength       *int64 `json:"context_length,omitempty"`
			MaxCompletionTokens *int64 `json:"max_completion_tokens,omitempty"`
		}
		out := make([]row, len(aliases))
		for i, a := range aliases {
			out[i] = row{
				Alias:               a.Alias,
				ProviderName:        a.ProviderName,
				UpstreamModel:       a.UpstreamModel,
				ContextLength:       a.ContextLength,
				MaxCompletionTokens: a.MaxCompletionTokens,
			}
		}
		b, _ := json.Marshal(out)
		return mcplib.NewToolResultText(string(b)), nil
	}
}

// optInt64 converts an int MCP param to *int64. Returns nil when n <= 0.
func optInt64(n int) *int64 {
	if n <= 0 {
		return nil
	}
	v := int64(n)
	return &v
}

func setModelCostHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "set_model_cost"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		provider := req.GetString("provider", "")
		model := req.GetString("model", "")
		inputRaw := req.GetFloat("usd_per_input_1k", 0)
		outputRaw := req.GetFloat("usd_per_output_1k", 0)
		if provider == "" || model == "" {
			return mcplib.NewToolResultError("required: provider, model"), nil
		}
		if inputRaw <= 0 || outputRaw <= 0 {
			return mcplib.NewToolResultError("usd_per_input_1k and usd_per_output_1k must be > 0"), nil
		}
		c := store.ModelCost{
			Provider:       provider,
			Model:          model,
			USDPerInput1k:  inputRaw,
			USDPerOutput1k: outputRaw,
			EffectiveFrom:  time.Now(),
		}
		if r := req.GetFloat("usd_per_reasoning_1k", 0); r > 0 {
			v := r
			c.USDPerReasoning1k = &v
		}
		if r := req.GetFloat("usd_per_cached_1k", 0); r > 0 {
			v := r
			c.USDPerCached1k = &v
		}
		if err := st.SetModelCost(c); err != nil {
			return mcplib.NewToolResultError("set_model_cost failed: " + err.Error()), nil
		}
		audit(st, "set_model_cost", "model_cost", provider+"/"+model, nil)
		resp := map[string]any{
			"ok": true, "provider": provider, "model": model,
			"usd_per_input_1k": inputRaw, "usd_per_output_1k": outputRaw,
		}
		if c.USDPerCached1k != nil {
			resp["usd_per_cached_1k"] = *c.USDPerCached1k
		}
		out, _ := json.Marshal(resp)
		return mcplib.NewToolResultText(string(out)), nil
	}
}

func listModelCostsHandler(st *store.Store) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		if err := checkTool(ctx, "list_model_costs"); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		costs, err := st.ListModelCosts()
		if err != nil {
			return mcplib.NewToolResultError("list_model_costs failed: " + err.Error()), nil
		}
		type row struct {
			Provider          string   `json:"provider"`
			Model             string   `json:"model"`
			USDPerInput1k     float64  `json:"usd_per_input_1k"`
			USDPerOutput1k    float64  `json:"usd_per_output_1k"`
			USDPerReasoning1k *float64 `json:"usd_per_reasoning_1k,omitempty"`
			USDPerCached1k    *float64 `json:"usd_per_cached_1k,omitempty"`
			EffectiveFrom     int64    `json:"effective_from"`
		}
		out := make([]row, len(costs))
		for i, c := range costs {
			out[i] = row{
				Provider:          c.Provider,
				Model:             c.Model,
				USDPerInput1k:     c.USDPerInput1k,
				USDPerOutput1k:    c.USDPerOutput1k,
				USDPerReasoning1k: c.USDPerReasoning1k,
				USDPerCached1k:    c.USDPerCached1k,
				EffectiveFrom:     c.EffectiveFrom.Unix(),
			}
		}
		b, _ := json.Marshal(out)
		return mcplib.NewToolResultText(string(b)), nil
	}
}

// maskKey redacts an API key, showing only the last 4 characters.
// Keeps enough context to identify which key is configured without
// leaking it in MCP tool output (agent transcripts, logs, etc.).
func maskKey(key string) string {
	if len(key) <= 4 {
		return "****"
	}
	return "****" + key[len(key)-4:]
}

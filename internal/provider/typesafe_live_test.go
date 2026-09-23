//go:build live

package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// Opt-in; never reads credential files or runs with the normal test suite.
// TYPESAFE_BASE_URL is versioned (including /v1), just like TypeSafeBaseURL.
func TestLive_TypeSafe_SystemOne(t *testing.T) {
	key := os.Getenv("TYPESAFE_API_KEY")
	if key == "" {
		t.Skip("TYPESAFE_API_KEY not set")
	}
	p := Provider{
		Name: "typesafe", Kind: "typesafe", APIKey: key,
		TypeSafeBaseURL: envOr("TYPESAFE_BASE_URL", "https://api.typesafe.ai/v1"),
	}
	body, err := json.Marshal(map[string]any{
		"model": envOr("TYPESAFE_MODEL", "jev-latest"),
		"state": "I was charged twice. Please refund the duplicate payment.",
		"questions": map[string]any{
			"refund":  map[string]any{"type": "noul", "instructions": "Is the customer requesting a refund?"},
			"team":    map[string]any{"type": "choice", "instructions": "Which team should handle this?", "criteria": map[string]string{"billing": "Payments and refunds", "technical": "Product issues"}},
			"urgency": map[string]any{"type": "score", "instructions": "How urgent is this?", "criteria": []string{"Routine", "Urgent", "Critical"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	resp, err := p.TypeSafeRequest(ctx, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upstream status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Model   string `json:"model"`
		Answers map[string]struct {
			Type string `json:"type"`
		} `json:"answers"`
		Usage struct {
			Input  *int `json:"input_tokens"`
			Output *int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Model == "" || result.Answers["refund"].Type != "noul" || result.Answers["team"].Type != "choice" || result.Answers["urgency"].Type != "score" || result.Usage.Input == nil || result.Usage.Output == nil {
		t.Fatal("unexpected System One response shape")
	}
	t.Logf("model=%s input_tokens=%d output_tokens=%d", result.Model, *result.Usage.Input, *result.Usage.Output)
}

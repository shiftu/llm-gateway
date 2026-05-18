package ir

import (
	"encoding/json"
	"testing"
)

// anthropic request body with one cached block and one plain block
var anthropicBodyWithCache = mustMarshal(map[string]any{
	"messages": []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":          "text",
					"text":          "You are a helpful assistant.",
					"cache_control": map[string]any{"type": "ephemeral"},
				},
				map[string]any{
					"type": "text",
					"text": "What is the capital of France?",
				},
			},
		},
	},
})

// anthropic request body with no cache_control fields
var anthropicBodyNoCache = mustMarshal(map[string]any{
	"messages": []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": "Hello"},
				map[string]any{"type": "text", "text": "World"},
			},
		},
	},
})

// openai request body shape with cache_control passed through as extension field
var openAIBodyWithCache = mustMarshal(map[string]any{
	"messages": []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":          "text",
					"text":          "You are a helpful assistant.",
					"cache_control": map[string]any{"type": "ephemeral"},
				},
				map[string]any{
					"type": "text",
					"text": "What is the capital of France?",
				},
			},
		},
	},
})

var openAIBodyNoCache = mustMarshal(map[string]any{
	"messages": []any{
		map[string]any{
			"role":    "user",
			"content": "Plain string content — no blocks.",
		},
	},
})

func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func TestExtractCacheControlsAnthropic_Present(t *testing.T) {
	blocks := ExtractCacheControlsAnthropic(anthropicBodyWithCache)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block with cache_control, got %d", len(blocks))
	}
	b := blocks[0]
	if b.Type != "text" {
		t.Errorf("expected type %q, got %q", "text", b.Type)
	}
	if b.Text != "You are a helpful assistant." {
		t.Errorf("unexpected text: %q", b.Text)
	}
	if b.CacheControl == nil {
		t.Fatal("expected non-nil CacheControl")
	}
	if b.CacheControl.Type != "ephemeral" {
		t.Errorf("expected CacheControl.Type %q, got %q", "ephemeral", b.CacheControl.Type)
	}
}

func TestExtractCacheControlsAnthropic_None(t *testing.T) {
	blocks := ExtractCacheControlsAnthropic(anthropicBodyNoCache)
	if blocks != nil {
		t.Errorf("expected nil, got %v", blocks)
	}
}

func TestExtractCacheControlsAnthropic_EmptyBody(t *testing.T) {
	// Must not panic; must return nil for both empty and invalid JSON.
	if got := ExtractCacheControlsAnthropic(nil); got != nil {
		t.Errorf("nil body: expected nil, got %v", got)
	}
	if got := ExtractCacheControlsAnthropic([]byte{}); got != nil {
		t.Errorf("empty body: expected nil, got %v", got)
	}
	if got := ExtractCacheControlsAnthropic([]byte("not json")); got != nil {
		t.Errorf("invalid JSON: expected nil, got %v", got)
	}
}

func TestExtractCacheControlsOpenAI_Present(t *testing.T) {
	blocks := ExtractCacheControlsOpenAI(openAIBodyWithCache)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block with cache_control, got %d", len(blocks))
	}
	b := blocks[0]
	if b.Type != "text" {
		t.Errorf("expected type %q, got %q", "text", b.Type)
	}
	if b.CacheControl == nil {
		t.Fatal("expected non-nil CacheControl")
	}
	if b.CacheControl.Type != "ephemeral" {
		t.Errorf("expected CacheControl.Type %q, got %q", "ephemeral", b.CacheControl.Type)
	}
}

func TestExtractCacheControlsOpenAI_None(t *testing.T) {
	blocks := ExtractCacheControlsOpenAI(openAIBodyNoCache)
	if blocks != nil {
		t.Errorf("expected nil, got %v", blocks)
	}
}

func TestHasCacheControl(t *testing.T) {
	cc := &CacheControl{Type: "ephemeral"}

	withCache := []ContentBlock{
		{Type: "text", Text: "hello", CacheControl: cc},
	}
	if !HasCacheControl(withCache) {
		t.Error("expected true for blocks with CacheControl, got false")
	}

	withoutCache := []ContentBlock{
		{Type: "text", Text: "hello"},
	}
	if HasCacheControl(withoutCache) {
		t.Error("expected false for blocks without CacheControl, got true")
	}

	if HasCacheControl(nil) {
		t.Error("expected false for nil blocks, got true")
	}

	if HasCacheControl([]ContentBlock{}) {
		t.Error("expected false for empty blocks, got true")
	}
}

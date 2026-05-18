package ir

import "encoding/json"

// CacheControl represents a prompt caching directive attached to a content block.
// Only "ephemeral" is defined in Anthropic's API v1.
type CacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

// cacheRawBlock is the wire shape used for JSON extraction only.
type cacheRawBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *CacheControl `json:"cache_control"`
}

// cacheRequestBody is the minimal shape shared by both Anthropic and OpenAI
// request bodies for cache_control extraction.
type cacheRequestBody struct {
	Messages []struct {
		Content []cacheRawBlock `json:"content"`
	} `json:"messages"`
}

// ExtractCacheControlsAnthropic parses an Anthropic-format request body and
// returns all content blocks that carry a cache_control directive, in order.
// Returns nil if none are found or body is not valid JSON.
func ExtractCacheControlsAnthropic(body []byte) []ContentBlock {
	return extractCacheBlocks(body)
}

// ExtractCacheControlsOpenAI parses an OpenAI-format request body and
// returns all content blocks that carry a cache_control directive (passed
// through as an extension field by Anthropic-compat providers).
// Returns nil if none are found.
func ExtractCacheControlsOpenAI(body []byte) []ContentBlock {
	return extractCacheBlocks(body)
}

// extractCacheBlocks is the shared implementation for both protocol shapes.
// Both Anthropic and OpenAI use the same messages[].content[] structure for
// cache_control pass-through, so a single parser covers both.
func extractCacheBlocks(body []byte) []ContentBlock {
	if len(body) == 0 {
		return nil
	}
	var req cacheRequestBody
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}
	var out []ContentBlock
	for _, msg := range req.Messages {
		for _, raw := range msg.Content {
			if raw.CacheControl == nil {
				continue
			}
			out = append(out, ContentBlock{
				Type:         raw.Type,
				Text:         raw.Text,
				CacheControl: raw.CacheControl,
			})
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// HasCacheControl reports whether any block in blocks carries a non-nil CacheControl.
func HasCacheControl(blocks []ContentBlock) bool {
	for _, b := range blocks {
		if b.CacheControl != nil {
			return true
		}
	}
	return false
}

package ir

import "encoding/json"

// ToolsFromOpenAI converts an OpenAI tools array (each element has type:"function"
// + nested "function" object) into IR []Tool. Returns nil on parse failure or empty input.
func ToolsFromOpenAI(raw []json.RawMessage) []Tool {
	if len(raw) == 0 {
		return nil
	}
	var out []Tool
	for _, r := range raw {
		var wrapper struct {
			Function struct {
				Name        string         `json:"name"`
				Description string         `json:"description"`
				Parameters  map[string]any `json:"parameters"`
			} `json:"function"`
		}
		if err := json.Unmarshal(r, &wrapper); err != nil {
			continue
		}
		t := Tool{
			Name:        wrapper.Function.Name,
			Description: wrapper.Function.Description,
			InputSchema: wrapper.Function.Parameters,
		}
		if t.Name == "" {
			continue
		}
		out = append(out, t)
	}
	return out
}

// ToolsToOpenAI converts IR []Tool to OpenAI wire format (array of
// {"type":"function","function":{...}}). Returns nil on empty input.
func ToolsToOpenAI(tools []Tool) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]any, len(tools))
	for i, t := range tools {
		fn := map[string]any{
			"name": t.Name,
		}
		if t.Description != "" {
			fn["description"] = t.Description
		}
		if t.InputSchema != nil {
			fn["parameters"] = t.InputSchema
		}
		out[i] = map[string]any{
			"type":     "function",
			"function": fn,
		}
	}
	return out
}

// ToolUsesFromOpenAI parses tool_calls from an OpenAI blocking response body
// (choices[0].message.tool_calls). Arguments field is a JSON string that gets
// parsed into map[string]any for the IR Input field. Returns nil when no
// tool_calls present or on parse failure.
func ToolUsesFromOpenAI(body []byte) []ToolUse {
	var resp struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}
	if len(resp.Choices) == 0 || len(resp.Choices[0].Message.ToolCalls) == 0 {
		return nil
	}
	calls := resp.Choices[0].Message.ToolCalls
	out := make([]ToolUse, 0, len(calls))
	for _, tc := range calls {
		var input map[string]any
		// Arguments is a JSON-encoded string per the OpenAI wire format — parse it into a map
		if tc.Function.Arguments != "" {
			// Arguments is a JSON-encoded string per the OpenAI wire format — parse it into a map
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
				continue
			}
		}
		out = append(out, ToolUse{
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: input,
		})
	}
	return out
}

// ToolUsesToOpenAI converts IR []ToolUse to OpenAI tool_calls wire format
// (array of {"id":...,"type":"function","function":{"name":...,"arguments":"<json_string>"}}).
// The Input map is re-serialised to a JSON string for the "arguments" field.
func ToolUsesToOpenAI(uses []ToolUse) []map[string]any {
	if len(uses) == 0 {
		return nil
	}
	out := make([]map[string]any, len(uses))
	for i, u := range uses {
		// OpenAI wire format requires arguments as a JSON string, not a map
		var argsStr string
		if u.Input != nil {
			b, err := json.Marshal(u.Input)
			if err != nil {
				b = []byte("{}")
			}
			argsStr = string(b)
		} else {
			argsStr = "{}"
		}
		out[i] = map[string]any{
			"id":   u.ID,
			"type": "function",
			"function": map[string]any{
				"name":      u.Name,
				"arguments": argsStr,
			},
		}
	}
	return out
}

// ToolUsesFromAnthropic extracts tool_use content blocks from an Anthropic
// blocking response body. Each block with type="tool_use" becomes a ToolUse.
// Returns nil when no tool_use blocks present.
func ToolUsesFromAnthropic(body []byte) []ToolUse {
	var resp struct {
		Content []struct {
			Type  string         `json:"type"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}
	var out []ToolUse
	for _, block := range resp.Content {
		if block.Type != BlockTypeToolUse {
			continue
		}
		out = append(out, ToolUse{
			ID:    block.ID,
			Name:  block.Name,
			Input: block.Input,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ToolUsesToAnthropic converts IR []ToolUse to Anthropic content blocks
// (array of {"type":"tool_use","id":...,"name":...,"input":{...}}).
func ToolUsesToAnthropic(uses []ToolUse) []map[string]any {
	if len(uses) == 0 {
		return nil
	}
	out := make([]map[string]any, len(uses))
	for i, u := range uses {
		input := u.Input
		if input == nil {
			input = map[string]any{}
		}
		out[i] = map[string]any{
			"type":  BlockTypeToolUse,
			"id":    u.ID,
			"name":  u.Name,
			"input": input,
		}
	}
	return out
}

// ToolResultsFromOpenAI converts an OpenAI tool-role messages array into IR
// []ToolResult. Each message with role="tool" becomes a ToolResult.
// Returns nil on empty/missing input.
func ToolResultsFromOpenAI(messages []map[string]any) []ToolResult {
	if len(messages) == 0 {
		return nil
	}
	var out []ToolResult
	for _, m := range messages {
		if m["role"] != "tool" {
			continue
		}
		id, _ := m["tool_call_id"].(string)
		content, _ := m["content"].(string)
		out = append(out, ToolResult{
			ToolUseID: id,
			Content:   content,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ToolResultsToOpenAI converts IR []ToolResult to OpenAI tool-role messages
// (array of {"role":"tool","tool_call_id":...,"content":...}).
func ToolResultsToOpenAI(results []ToolResult) []map[string]any {
	if len(results) == 0 {
		return nil
	}
	out := make([]map[string]any, len(results))
	for i, r := range results {
		out[i] = map[string]any{
			"role":         "tool",
			"tool_call_id": r.ToolUseID,
			"content":      r.Content,
		}
	}
	return out
}

// ToolResultsFromAnthropic extracts tool_result content blocks from an
// Anthropic user message content array. Returns nil when none present.
func ToolResultsFromAnthropic(content []map[string]any) []ToolResult {
	if len(content) == 0 {
		return nil
	}
	var out []ToolResult
	for _, block := range content {
		if block["type"] != BlockTypeToolResult {
			continue
		}
		id, _ := block["tool_use_id"].(string)
		c, _ := block["content"].(string)
		isErr, _ := block["is_error"].(bool)
		out = append(out, ToolResult{
			ToolUseID: id,
			Content:   c,
			IsError:   isErr,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ToolResultsToAnthropic converts IR []ToolResult to Anthropic content blocks
// (array of {"type":"tool_result","tool_use_id":...,"content":...}).
func ToolResultsToAnthropic(results []ToolResult) []map[string]any {
	if len(results) == 0 {
		return nil
	}
	out := make([]map[string]any, len(results))
	for i, r := range results {
		out[i] = map[string]any{
			"type":        BlockTypeToolResult,
			"tool_use_id": r.ToolUseID,
			"content":     r.Content,
		}
		if r.IsError {
			out[i]["is_error"] = true
		}
	}
	return out
}

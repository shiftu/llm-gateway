package ir

var openAIToIR = map[string]string{
	"stop":           StopReasonEndTurn,
	"length":         StopReasonMaxTokens,
	"tool_calls":     StopReasonToolUse,
	"content_filter": StopReasonEndTurn,
	"function_call":  StopReasonToolUse,
}

var anthropicToIR = map[string]string{
	"end_turn":      StopReasonEndTurn,
	"max_tokens":    StopReasonMaxTokens,
	"stop_sequence": StopReasonStopSequence,
	"tool_use":      StopReasonToolUse,
}

var irToOpenAI = map[string]string{
	StopReasonEndTurn:      "stop",
	StopReasonMaxTokens:    "length",
	StopReasonStopSequence: "stop",
	StopReasonToolUse:      "tool_calls",
}

var irToAnthropic = map[string]string{
	StopReasonEndTurn:      "end_turn",
	StopReasonMaxTokens:    "max_tokens",
	StopReasonStopSequence: "stop_sequence",
	StopReasonToolUse:      "tool_use",
}

// FinishReasonFromOpenAI maps an OpenAI finish_reason string to the canonical
// IR StopReason. Unknown values fall back to StopReasonEndTurn — never panics.
func FinishReasonFromOpenAI(finishReason string) string {
	if r, ok := openAIToIR[finishReason]; ok {
		return r
	}
	return StopReasonEndTurn
}

// FinishReasonFromAnthropic maps an Anthropic stop_reason string to the
// canonical IR StopReason. Unknown values fall back to StopReasonEndTurn.
func FinishReasonFromAnthropic(stopReason string) string {
	if r, ok := anthropicToIR[stopReason]; ok {
		return r
	}
	return StopReasonEndTurn
}

// FinishReasonToOpenAI maps an IR StopReason to the OpenAI finish_reason wire value.
func FinishReasonToOpenAI(stopReason string) string {
	if r, ok := irToOpenAI[stopReason]; ok {
		return r
	}
	return "stop"
}

// FinishReasonToAnthropic maps an IR StopReason to the Anthropic stop_reason wire value.
func FinishReasonToAnthropic(stopReason string) string {
	if r, ok := irToAnthropic[stopReason]; ok {
		return r
	}
	return "end_turn"
}

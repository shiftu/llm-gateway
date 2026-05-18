package ir_test

import (
	"testing"

	"github.com/panda/llm-gateway/internal/ir"
)

func TestFinishReasonFromOpenAI(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"stop", ir.StopReasonEndTurn},
		{"length", ir.StopReasonMaxTokens},
		{"tool_calls", ir.StopReasonToolUse},
		{"content_filter", ir.StopReasonEndTurn},
		{"function_call", ir.StopReasonToolUse},
		{"", ir.StopReasonEndTurn},
		{"totally_unknown_value", ir.StopReasonEndTurn},
	}
	for _, c := range cases {
		got := ir.FinishReasonFromOpenAI(c.input)
		if got != c.want {
			t.Errorf("FinishReasonFromOpenAI(%q) = %q; want %q", c.input, got, c.want)
		}
	}
}

func TestFinishReasonFromAnthropic(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"end_turn", ir.StopReasonEndTurn},
		{"max_tokens", ir.StopReasonMaxTokens},
		{"stop_sequence", ir.StopReasonStopSequence},
		{"tool_use", ir.StopReasonToolUse},
		{"", ir.StopReasonEndTurn},
		{"totally_unknown_value", ir.StopReasonEndTurn},
	}
	for _, c := range cases {
		got := ir.FinishReasonFromAnthropic(c.input)
		if got != c.want {
			t.Errorf("FinishReasonFromAnthropic(%q) = %q; want %q", c.input, got, c.want)
		}
	}
}

func TestFinishReasonToOpenAI(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{ir.StopReasonEndTurn, "stop"},
		{ir.StopReasonMaxTokens, "length"},
		{ir.StopReasonStopSequence, "stop"},
		{ir.StopReasonToolUse, "tool_calls"},
		{"totally_unknown_value", "stop"},
	}
	for _, c := range cases {
		got := ir.FinishReasonToOpenAI(c.input)
		if got != c.want {
			t.Errorf("FinishReasonToOpenAI(%q) = %q; want %q", c.input, got, c.want)
		}
	}
}

func TestFinishReasonToAnthropic(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{ir.StopReasonEndTurn, "end_turn"},
		{ir.StopReasonMaxTokens, "max_tokens"},
		{ir.StopReasonStopSequence, "stop_sequence"},
		{ir.StopReasonToolUse, "tool_use"},
		{"totally_unknown_value", "end_turn"},
	}
	for _, c := range cases {
		got := ir.FinishReasonToAnthropic(c.input)
		if got != c.want {
			t.Errorf("FinishReasonToAnthropic(%q) = %q; want %q", c.input, got, c.want)
		}
	}
}

func TestFinishReason_RoundTrip_OpenAI(t *testing.T) {
	// StopReasonStopSequence does NOT round-trip through OpenAI: it maps to
	// "stop" which maps back to StopReasonEndTurn. That lossy mapping is
	// intentional — OpenAI collapses stop_sequence into "stop".
	cases := []struct {
		reason    string
		roundTrip bool // false = lossy, documented here but not asserted
	}{
		{ir.StopReasonEndTurn, true},
		{ir.StopReasonMaxTokens, true},
		{ir.StopReasonStopSequence, false}, // lossy: "stop" → end_turn
		{ir.StopReasonToolUse, true},
	}
	for _, c := range cases {
		wire := ir.FinishReasonToOpenAI(c.reason)
		got := ir.FinishReasonFromOpenAI(wire)
		if c.roundTrip && got != c.reason {
			t.Errorf("OpenAI round-trip %q: ToOpenAI=%q, FromOpenAI=%q; want %q", c.reason, wire, got, c.reason)
		}
	}
}

func TestFinishReason_RoundTrip_Anthropic(t *testing.T) {
	reasons := []string{
		ir.StopReasonEndTurn,
		ir.StopReasonMaxTokens,
		ir.StopReasonStopSequence,
		ir.StopReasonToolUse,
	}
	for _, r := range reasons {
		wire := ir.FinishReasonToAnthropic(r)
		got := ir.FinishReasonFromAnthropic(wire)
		if got != r {
			t.Errorf("Anthropic round-trip %q: ToAnthropic=%q, FromAnthropic=%q; want %q", r, wire, got, r)
		}
	}
}

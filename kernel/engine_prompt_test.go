package kernel

import (
	"strings"
	"testing"

	"nofx/store"
)

func TestStrategyEngineSystemPromptRequiresTrailingStopOnly(t *testing.T) {
	engine := NewStrategyEngine(&store.StrategyConfig{Language: "en"})
	prompt := engine.BuildSystemPrompt(1000, "")

	for _, expected := range []string{
		"# Native Trailing Stop (Required for New Positions)",
		"`trailing_stop_activation_pct`",
		"`trailing_stop_callback_pct`",
		"Required range: 0.1-5.0",
		"For a long, activation is above entry; for a short, activation is below entry.",
		"no fixed risk/reward ratio is required",
		"Do not output fixed `stop_loss` or `take_profit` prices.",
		"# Direction Selection: Long and Short Are Symmetric",
		"When bearish trend, momentum, and entry timing meet the same quality and confidence standards as a long, use `open_short`.",
	} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("system prompt should contain %q", expected)
		}
	}
	if strings.Contains(prompt, "Risk-Reward Ratio:") {
		t.Error("system prompt should not require a fixed risk/reward ratio")
	}
}

func TestExtractDecisionsNormalizesEnglishConfidenceWords(t *testing.T) {
	decisions, err := extractDecisions(`[{"symbol":"BTCUSDT","action":"wait","confidence": sixty,"reason":"no signal"},{"symbol":"ETHUSDT","action":"wait","confidence":"seventy-five"}]`)
	if err != nil {
		t.Fatalf("extractDecisions returned error: %v", err)
	}
	if len(decisions) != 2 {
		t.Fatalf("got %d decisions, want 2", len(decisions))
	}
	if decisions[0].Confidence != 60 || decisions[1].Confidence != 75 {
		t.Fatalf("confidence values = %v, %v; want 60, 75", decisions[0].Confidence, decisions[1].Confidence)
	}
}

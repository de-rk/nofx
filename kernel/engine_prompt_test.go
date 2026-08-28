package kernel

import (
	"strings"
	"testing"

	"nofx/store"
)

func TestStrategyEngineSystemPromptIncludesOKXTrailingStop(t *testing.T) {
	engine := NewStrategyEngine(&store.StrategyConfig{Language: "en"})
	prompt := engine.BuildSystemPrompt(1000, "")

	for _, expected := range []string{
		"# OKX Native Trailing Stop",
		"`trailing_stop_activation_pct`",
		"`trailing_stop_callback_pct`",
		"Must be 0.1-5.0",
		"For a long, activation is above entry; for a short, activation is below entry.",
		"Always provide fixed `stop_loss` and `take_profit` as well",
	} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("system prompt should contain %q", expected)
		}
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

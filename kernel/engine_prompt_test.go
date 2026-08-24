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

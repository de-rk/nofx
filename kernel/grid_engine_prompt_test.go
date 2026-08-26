package kernel

import (
	"strings"
	"testing"

	"nofx/store"
)

func TestGridSystemPromptIncludesActiveReductionActions(t *testing.T) {
	prompt := BuildGridSystemPrompt(&store.StrategyConfig{
		GridConfig: &store.GridStrategyConfig{Symbol: "HYPEUSDT"},
	}, "en")

	for _, expected := range []string{
		"| reduce_long | Partially close a long position at market |",
		"| reduce_short | Partially close a short position at market |",
		"reduce_long / reduce_short are ordinary partial market-close actions.",
		"Use close_long / close_short for a full exit.",
	} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("grid system prompt should contain %q", expected)
		}
	}
}

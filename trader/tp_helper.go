package trader

import (
	"nofx/store"
)

// LoadTPLevelsFromConfig loads take profit levels from strategy config
func LoadTPLevelsFromConfig(config *store.StrategyConfig) []store.TPLevel {
	if config == nil || config.PositionManagement == nil {
		return nil
	}

	pm := config.PositionManagement
	if !pm.EnablePartialTakeProfit || len(pm.TPLevels) == 0 {
		return nil
	}

	levels := make([]store.TPLevel, len(pm.TPLevels))
	for i, tpl := range pm.TPLevels {
		levels[i] = store.TPLevel{
			Pct:        tpl.Pct,
			CloseRatio: tpl.CloseRatio,
			Executed:   false,
		}
	}
	return levels
}

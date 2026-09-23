package trader

import (
	"testing"

	"nofx/kernel"
	"nofx/store"
)

func TestBuildAlgoGridDecisionPrioritizesLevelsNearestCurrentPrice(t *testing.T) {
	config := &store.GridStrategyConfig{Symbol: "TESTUSDT", Leverage: 1}
	at := &AutoTrader{config: AutoTraderConfig{StrategyConfig: &store.StrategyConfig{GridConfig: config}}}
	ctx := &kernel.GridContext{
		CurrentPrice:     100,
		AvailableBalance: 100,
		TotalInvestment:  100,
		Leverage:         1,
		Levels: []kernel.GridLevelInfo{
			{Index: 0, Price: 80, State: "empty", Side: "buy", AllocatedUSD: 10},
			{Index: 1, Price: 99, State: "empty", Side: "buy", AllocatedUSD: 10},
			{Index: 2, Price: 102, State: "empty", Side: "sell", AllocatedUSD: 10},
			{Index: 3, Price: 120, State: "empty", Side: "sell", AllocatedUSD: 10},
		},
	}

	decision := at.buildAlgoGridDecision(ctx)
	if len(decision.Decisions) != 4 {
		t.Fatalf("expected 4 order decisions, got %d", len(decision.Decisions))
	}
	wantPrices := []float64{99, 102, 80, 120}
	for i, want := range wantPrices {
		if got := decision.Decisions[i].Price; got != want {
			t.Errorf("decision %d price = %v, want %v", i, got, want)
		}
	}
}

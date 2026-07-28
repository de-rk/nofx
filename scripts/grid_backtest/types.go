package main

// GridParams is the search space for the simulated-annealing optimizer.
// It mirrors the subset of store.GridStrategyConfig that most directly
// drives grid risk/return: bounds width, level distribution, leverage,
// and profit-reduce cadence. Fields not listed here (symbol, total
// investment, T-trade, investment refresh, etc.) are held fixed for a
// given backtest run rather than searched.
type GridParams struct {
	GridCount              int     // number of price levels
	ATRMultiplier           float64 // grid half-range = ATR14 * this
	Distribution            string  // "uniform" | "gaussian" | "pyramid"
	Leverage                int
	ProfitReduceStepPct     float64 // % profit step that triggers a partial reduce
	ProfitReduceMultiplier  float64 // fraction of position reduced per step
}

func (p GridParams) Clone() GridParams { return p }

// simLevel is one grid price level during simulation.
type simLevel struct {
	Price        float64
	AllocatedUSD float64
	Side         string // "buy" (opens long) | "sell" (opens short)
	Qty          float64
	Filled       bool
}

// SimResult summarizes one backtest run's outcome.
type SimResult struct {
	FinalEquity     float64
	ReturnPct       float64 // (FinalEquity - TotalInvestment) / TotalInvestment * 100
	MaxDrawdownPct  float64
	FilledLevels    int
	LongReduces     int
	ShortReduces    int
	BlewUp          bool // equity dropped to <= 0 during the run (proxy for liquidation)
	Score           float64
}

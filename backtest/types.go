// Package backtest offline-tests grid-strategy parameter combinations
// against historical K-lines and searches for a high-scoring combination
// via simulated annealing. It is fully standalone: no DB, no exchange
// calls, no AI calls, and it never touches the live trading path in
// trader/. Callers (CLI or HTTP handler) get back a suggested parameter
// set to review — this package never writes anything back to a config
// or database itself.
package backtest

// GridParams is the search space for the simulated-annealing optimizer.
// It mirrors the subset of store.GridStrategyConfig that most directly
// drives grid risk/return: bounds width, level distribution, leverage,
// and profit-reduce cadence. Fields not listed here (symbol, total
// investment, T-trade, investment refresh, etc.) are held fixed for a
// given backtest run rather than searched.
type GridParams struct {
	GridCount              int     `json:"grid_count"`
	ATRMultiplier           float64 `json:"atr_multiplier"`
	Distribution            string  `json:"distribution"` // "uniform" | "gaussian" | "pyramid"
	Leverage                int     `json:"leverage"`
	ProfitReduceStepPct     float64 `json:"profit_reduce_step_pct"`
	ProfitReduceMultiplier  float64 `json:"profit_reduce_multiplier"`
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
	FinalEquity    float64 `json:"final_equity"`
	ReturnPct      float64 `json:"return_pct"` // (FinalEquity - TotalInvestment) / TotalInvestment * 100
	MaxDrawdownPct float64 `json:"max_drawdown_pct"`
	FilledLevels   int     `json:"filled_levels"`
	LongReduces    int     `json:"long_reduces"`
	ShortReduces   int     `json:"short_reduces"`
	BlewUp         bool    `json:"blew_up"` // equity dropped to <= 0 during the run (proxy for liquidation)
	Score          float64 `json:"score"`
}

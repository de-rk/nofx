// Package backtest offline-tests grid-strategy parameter combinations
// against historical K-lines and searches for a high-scoring combination
// via simulated annealing. It is fully standalone: no DB, no exchange
// calls, no AI calls, and it never touches the live trading path in
// trader/. Callers (CLI or HTTP handler) get back a suggested parameter
// set to review — this package never writes anything back to a config
// or database itself.
package backtest

// GridParams is the search space for the simulated-annealing optimizer,
// plus a few fixed (non-searched) risk-mechanism toggles carried along so a
// backtest run can reflect a real strategy's full risk configuration.
// GridCount/ATRMultiplier/Distribution/Leverage/ProfitReduceStepPct/
// ProfitReduceMultiplier are perturbed by Anneal (see anneal.go's
// neighbor()); the T-trade/profit-drawdown/small-position-close/fee
// fields below are held fixed at whatever value the caller supplies
// (typically copied from a selected strategy's grid_config) for
// the whole search — they describe risk mechanisms/costs to simulate
// faithfully, not dimensions to optimize over. Fields not listed here
// (symbol, total investment, investment refresh, etc.) are likewise held
// fixed for a given run.
type GridParams struct {
	GridCount              int     `json:"grid_count"`
	ATRMultiplier          float64 `json:"atr_multiplier"`
	Distribution           string  `json:"distribution"` // "uniform" | "gaussian" | "pyramid"
	Leverage               int     `json:"leverage"`
	ProfitReduceStepPct    float64 `json:"profit_reduce_step_pct"`
	ProfitReduceMultiplier float64 `json:"profit_reduce_multiplier"`

	// T-trade (trapped-position handling) — ports trader.ttradeTagOrders /
	// ttradeProcessFills / placeTTradeReduceOrder. When EnableTTrade is
	// true and a side's position value (at entry price) exceeds
	// TTradePositionThresholdPct of total investment, still-pending grid
	// orders on that side are "tagged"; a tagged order's fill immediately
	// queues an opposite reduce-only order at TTradeSpreadPct away from
	// its own fill price (floored at 0.2%, matching the live floor).
	EnableTTrade               bool    `json:"enable_trapped_reduce"`
	TTradePositionThresholdPct float64 `json:"t_trade_position_threshold_pct"`
	TTradeSpreadPct            float64 `json:"t_trade_spread_pct"`

	// ProfitDrawdownThresholdPct ports the peak-tracking full-close in
	// trader/auto_trader.go's checkPositionDrawdown: once a side's profit
	// exceeds 5% and then pulls back this many percentage points off its
	// own running peak profit%, the entire side is closed. 0 disables the
	// check, matching the live GridConfig.ProfitDrawdownThreshold==0
	// convention. Distinct from the stepped ProfitReduceStepPct mechanism.
	ProfitDrawdownThresholdPct float64 `json:"profit_drawdown_threshold"`

	// EnableSmallPositionClose ports the early-exit branch inside
	// trader.checkProfitReduce: once profit exceeds 2x ProfitReduceStepPct
	// and the position's notional value drops under $100, the side is
	// closed entirely instead of stepping down gradually.
	EnableSmallPositionClose bool `json:"enable_small_position_close"`

	// FeePct is a flat maker/taker fee %% applied to every simulated fill's
	// notional (grid entry fills, T-trade reduce fills, profit-reduce steps,
	// and full closes) — a simplified approximation. The live system doesn't
	// compute fees from a formula, it reads exchange-reported commission per
	// fill; this flat-rate model is a deliberate simplification, not a
	// fidelity target. 0 disables fee simulation.
	FeePct float64 `json:"fee_pct"`

	// ScoreMode picks the annealing objective's return/drawdown trade-off
	// (see Score()): "balanced" (default, empty also means this) weighs
	// drawdown heavily so the search favors safer parameter sets;
	// "return_focused" weighs it much less so the search chases raw return,
	// still bounded by the separate hard -1e9 penalty for BlewUp. Unlike the
	// other fields above, this doesn't change what Simulate() does — the
	// same run produces the same fills/fees/drawdown — it only changes which
	// candidate Anneal() ends up preferring.
	ScoreMode string `json:"score_mode"`
}

func (p GridParams) Clone() GridParams { return p }

// simLevel is one grid price level during simulation.
type simLevel struct {
	Price        float64
	AllocatedUSD float64
	Side         string // "buy" (opens long) | "sell" (opens short)
	Qty          float64
	Filled       bool
	TTradeTagged bool // watched for a T-trade reduce order once this level fills
}

// ttradeReduceOrder is a pending reduce-only limit order spawned when a
// T-trade-tagged level fills. IsLong true means this order closes long
// exposure (i.e. it's a sell order); false means it closes short (a buy).
type ttradeReduceOrder struct {
	Price  float64
	Qty    float64
	IsLong bool
}

// SimResult summarizes one backtest run's outcome.
type SimResult struct {
	FinalEquity         float64 `json:"final_equity"`
	ReturnPct           float64 `json:"return_pct"` // (FinalEquity - TotalInvestment) / TotalInvestment * 100
	MaxDrawdownPct      float64 `json:"max_drawdown_pct"`
	FilledLevels        int     `json:"filled_levels"`
	LongReduces         int     `json:"long_reduces"`
	ShortReduces        int     `json:"short_reduces"`
	TTradeReduces       int     `json:"t_trade_reduces"`
	DrawdownCloses      int     `json:"drawdown_closes"`
	SmallPositionCloses int     `json:"small_position_closes"`
	TotalFeesPaid       float64 `json:"total_fees_paid"`
	BlewUp              bool    `json:"blew_up"` // equity dropped to <= maintenance margin during the run (proxy for cross-margin liquidation)
	Score               float64 `json:"score"`
}

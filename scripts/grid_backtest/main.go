// grid_backtest is the CLI entry point for nofx/backtest — see that
// package's doc comment for scope and limitations. This file is a thin
// wrapper; all the actual logic lives in nofx/backtest so the web API
// handler (api/server.go) can reuse it without shelling out to this binary.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"nofx/backtest"
	"nofx/market"
)

func main() {
	var (
		symbol                     string
		timeframe                  string
		days                       int
		totalInvestment            float64
		leverage                   int
		iterations                 int
		seed                       int64
		enableTTrade               bool
		ttradePositionThresholdPct float64
		ttradeSpreadPct            float64
		profitDrawdownThresholdPct float64
		enableSmallPositionClose   bool
		feePct                     float64
		scoreMode                  string
	)
	flag.StringVar(&symbol, "symbol", "HYPEUSDT", "trading symbol")
	flag.StringVar(&timeframe, "timeframe", "15m", "candle timeframe for the backtest")
	flag.IntVar(&days, "days", 60, "how many days of history to backtest over")
	flag.Float64Var(&totalInvestment, "investment", 1000, "starting total investment (USDT)")
	flag.IntVar(&leverage, "leverage", 5, "starting leverage (also searched)")
	flag.IntVar(&iterations, "iterations", 3000, "simulated annealing iterations")
	flag.Int64Var(&seed, "seed", 1, "RNG seed (change for a different search trajectory)")
	flag.BoolVar(&enableTTrade, "enable-ttrade", false, "simulate T-trade (trapped-position tag/reduce) handling")
	flag.Float64Var(&ttradePositionThresholdPct, "ttrade-position-threshold-pct", 30, "position size %% of total investment that activates T-trade")
	flag.Float64Var(&ttradeSpreadPct, "ttrade-spread-pct", 0.2, "price spread %% for T-trade reduce orders (floored at 0.2)")
	flag.Float64Var(&profitDrawdownThresholdPct, "profit-drawdown-threshold-pct", 0, "peak-profit pullback %% that triggers a full close (0 disables)")
	flag.BoolVar(&enableSmallPositionClose, "enable-small-position-close", false, "fully close a side once profit > 2x step and notional < $100")
	flag.Float64Var(&feePct, "fee-pct", 0.02, "flat maker/taker fee %% charged on every simulated fill's notional (0 disables)")
	flag.StringVar(&scoreMode, "score-mode", "balanced", "annealing objective: \"balanced\" (penalize drawdown heavily) or \"return_focused\" (chase raw return)")
	flag.Parse()

	tfDur, err := market.TFDuration(timeframe)
	if err != nil {
		log.Fatalf("❌ invalid timeframe: %v", err)
	}

	// Pull extra warm-up history so ATR14 is well-formed at the start of the
	// scored window, then fetch the scored window itself as one contiguous
	// range so indices line up.
	warmupBars := 60
	end := time.Now()
	start := end.Add(-time.Duration(days) * 24 * time.Hour).Add(-time.Duration(warmupBars) * tfDur)

	fmt.Printf("📥 Fetching %s %s klines from %s to %s...\n", symbol, timeframe, start.Format(time.RFC3339), end.Format(time.RFC3339))
	klines, err := market.GetKlinesRange(symbol, timeframe, start, end)
	if err != nil {
		log.Fatalf("❌ failed to fetch klines: %v", err)
	}
	if len(klines) < warmupBars+50 {
		log.Fatalf("❌ not enough klines returned (%d) — need at least %d for a meaningful backtest", len(klines), warmupBars+50)
	}
	fmt.Printf("✅ got %d bars\n\n", len(klines))

	startIdx := warmupBars

	initial := backtest.GridParams{
		GridCount:                  20,
		ATRMultiplier:              3.0,
		Distribution:               "gaussian",
		Leverage:                   leverage,
		ProfitReduceStepPct:        6,
		ProfitReduceMultiplier:     0.1,
		EnableTTrade:               enableTTrade,
		TTradePositionThresholdPct: ttradePositionThresholdPct,
		TTradeSpreadPct:            ttradeSpreadPct,
		ProfitDrawdownThresholdPct: profitDrawdownThresholdPct,
		EnableSmallPositionClose:   enableSmallPositionClose,
		FeePct:                     feePct,
		ScoreMode:                  scoreMode,
	}

	baseline := backtest.Simulate(klines, startIdx, totalInvestment, initial)
	printResult("Baseline (initial guess)", initial, baseline)

	eval := func(p backtest.GridParams) backtest.SimResult {
		return backtest.Simulate(klines, startIdx, totalInvestment, p)
	}

	cfg := backtest.AnnealConfig{
		Iterations:  iterations,
		InitialTemp: 10.0,
		CoolingRate: 1 - 5.0/float64(iterations), // reach ~1% of InitialTemp by the end
		Seed:        seed,
	}

	fmt.Printf("🔥 Running simulated annealing (%d iterations)...\n", cfg.Iterations)
	result := backtest.Anneal(initial, eval, cfg)
	fmt.Println()
	printResult("Best found", result.Best, result.BestResult)

	if result.BestResult.BlewUp {
		fmt.Println("\n⚠️  Best result still triggered a simulated cross-margin liquidation somewhere in the window — this symbol/period combination may not be suitable for grid trading regardless of parameters.")
	}

	fmt.Println("\nThis is a suggestion only — nothing was written back to any config. Review and apply manually if it looks reasonable.")
	os.Exit(0)
}

func printResult(label string, p backtest.GridParams, r backtest.SimResult) {
	fmt.Printf("--- %s ---\n", label)
	fmt.Printf("  grid_count=%d atr_multiplier=%.2f distribution=%s leverage=%d profit_reduce_step_pct=%.1f profit_reduce_multiplier=%.2f\n",
		p.GridCount, p.ATRMultiplier, p.Distribution, p.Leverage, p.ProfitReduceStepPct, p.ProfitReduceMultiplier)
	if p.EnableTTrade {
		fmt.Printf("  ttrade: enabled position_threshold_pct=%.1f spread_pct=%.2f\n", p.TTradePositionThresholdPct, p.TTradeSpreadPct)
	}
	if p.ProfitDrawdownThresholdPct > 0 {
		fmt.Printf("  profit_drawdown_threshold_pct=%.1f\n", p.ProfitDrawdownThresholdPct)
	}
	if p.EnableSmallPositionClose {
		fmt.Printf("  small_position_close: enabled\n")
	}
	if p.FeePct > 0 {
		fmt.Printf("  fee_pct=%.3f%%\n", p.FeePct)
	}
	if p.ScoreMode == "return_focused" {
		fmt.Printf("  score_mode: return_focused\n")
	}
	fmt.Printf("  return=%.2f%% max_drawdown=%.2f%% filled_levels=%d long_reduces=%d short_reduces=%d ttrade_reduces=%d drawdown_closes=%d small_position_closes=%d total_fees_paid=%.2f blew_up=%v score=%.2f\n",
		r.ReturnPct, r.MaxDrawdownPct, r.FilledLevels, r.LongReduces, r.ShortReduces, r.TTradeReduces, r.DrawdownCloses, r.SmallPositionCloses, r.TotalFeesPaid, r.BlewUp, r.Score)
}

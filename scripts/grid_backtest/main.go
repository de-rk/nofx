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
		symbol          string
		timeframe       string
		days            int
		totalInvestment float64
		leverage        int
		iterations      int
		seed            int64
	)
	flag.StringVar(&symbol, "symbol", "HYPEUSDT", "trading symbol")
	flag.StringVar(&timeframe, "timeframe", "15m", "candle timeframe for the backtest")
	flag.IntVar(&days, "days", 60, "how many days of history to backtest over")
	flag.Float64Var(&totalInvestment, "investment", 1000, "starting total investment (USDT)")
	flag.IntVar(&leverage, "leverage", 5, "starting leverage (also searched)")
	flag.IntVar(&iterations, "iterations", 3000, "simulated annealing iterations")
	flag.Int64Var(&seed, "seed", 1, "RNG seed (change for a different search trajectory)")
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
		GridCount:              20,
		ATRMultiplier:          3.0,
		Distribution:           "gaussian",
		Leverage:               leverage,
		ProfitReduceStepPct:    6,
		ProfitReduceMultiplier: 0.1,
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
		fmt.Println("\n⚠️  Best result still blew up (equity <= 0) somewhere in the window — this symbol/period combination may not be suitable for grid trading regardless of parameters.")
	}

	fmt.Println("\nThis is a suggestion only — nothing was written back to any config. Review and apply manually if it looks reasonable.")
	os.Exit(0)
}

func printResult(label string, p backtest.GridParams, r backtest.SimResult) {
	fmt.Printf("--- %s ---\n", label)
	fmt.Printf("  grid_count=%d atr_multiplier=%.2f distribution=%s leverage=%d profit_reduce_step_pct=%.1f profit_reduce_multiplier=%.2f\n",
		p.GridCount, p.ATRMultiplier, p.Distribution, p.Leverage, p.ProfitReduceStepPct, p.ProfitReduceMultiplier)
	fmt.Printf("  return=%.2f%% max_drawdown=%.2f%% filled_levels=%d long_reduces=%d short_reduces=%d blew_up=%v score=%.2f\n",
		r.ReturnPct, r.MaxDrawdownPct, r.FilledLevels, r.LongReduces, r.ShortReduces, r.BlewUp, r.Score)
}

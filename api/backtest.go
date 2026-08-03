package api

import (
	"fmt"
	"nofx/backtest"
	"nofx/market"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// handleGridBacktestRun streams a grid-strategy offline backtest + simulated
// annealing parameter search as Server-Sent Events. This never touches the
// live trading path (trader/), the database, or any exchange/AI credentials
// — it only fetches historical K-lines and runs pure in-memory simulation.
// Results are a suggestion for the operator to review; nothing is written
// back to any strategy config.
//
// Events emitted, in order:
//   - "baseline": {params, result} for the fixed starting parameter set
//   - "progress": {iteration, iterations, best_score} periodically during the search
//   - "done":     {params, result} for the best parameter set found
//   - "error":    {error} if fetching data or running the search fails
func (s *Server) handleGridBacktestRun(c *gin.Context) {
	symbol := c.DefaultQuery("symbol", "HYPEUSDT")
	timeframe := c.DefaultQuery("timeframe", "15m")
	days := queryInt(c, "days", 60, 7, 365)
	totalInvestment := queryFloat(c, "investment", 1000, 10, 1_000_000)
	leverage := queryInt(c, "leverage", 5, 1, 10)
	iterations := queryInt(c, "iterations", 2000, 100, 20000)
	seed := int64(queryInt(c, "seed", 1, 1, 1<<30))

	// Baseline grid params default to a generic guess, but the caller (the
	// frontend) may pass a selected strategy's actual values instead so the
	// "baseline" comparison reflects what's really configured, not a
	// hardcoded stand-in.
	gridCount := queryInt(c, "grid_count", 20, 2, 100)
	atrMultiplier := queryFloat(c, "atr_multiplier", 3.0, 0.1, 20)
	distribution := c.DefaultQuery("distribution", "gaussian")
	profitReduceStepPct := queryFloat(c, "profit_reduce_step_pct", 6, 0.1, 100)
	profitReduceMultiplier := queryFloat(c, "profit_reduce_multiplier", 0.1, 0.01, 1)
	enableTTrade := queryBool(c, "enable_trapped_reduce", false)
	ttradePositionThresholdPct := queryFloat(c, "t_trade_position_threshold_pct", 30, 1, 100)
	ttradeSpreadPct := queryFloat(c, "t_trade_spread_pct", 0.2, 0.01, 10)
	profitDrawdownThresholdPct := queryFloat(c, "profit_drawdown_threshold", 0, 0, 100)
	enableSmallPositionClose := queryBool(c, "enable_small_position_close", false)
	feePct := queryFloat(c, "fee_pct", 0.02, 0, 1)
	scoreMode := c.DefaultQuery("score_mode", "balanced")

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	fmt.Fprintf(c.Writer, ": ok\n\n")
	c.Writer.Flush()

	tfDur, err := market.TFDuration(timeframe)
	if err != nil {
		c.SSEvent("error", gin.H{"error": fmt.Sprintf("invalid timeframe: %v", err)})
		c.Writer.Flush()
		return
	}

	const warmupBars = 60
	end := time.Now()
	start := end.Add(-time.Duration(days) * 24 * time.Hour).Add(-time.Duration(warmupBars) * tfDur)

	klines, err := market.GetKlinesRange(symbol, timeframe, start, end)
	if err != nil {
		c.SSEvent("error", gin.H{"error": fmt.Sprintf("failed to fetch klines: %v", err)})
		c.Writer.Flush()
		return
	}
	if len(klines) < warmupBars+50 {
		c.SSEvent("error", gin.H{"error": fmt.Sprintf("not enough klines returned (%d) for a meaningful backtest", len(klines))})
		c.Writer.Flush()
		return
	}
	startIdx := warmupBars

	initial := backtest.GridParams{
		GridCount:                  gridCount,
		ATRMultiplier:              atrMultiplier,
		Distribution:               distribution,
		Leverage:                   leverage,
		ProfitReduceStepPct:        profitReduceStepPct,
		ProfitReduceMultiplier:     profitReduceMultiplier,
		EnableTTrade:               enableTTrade,
		TTradePositionThresholdPct: ttradePositionThresholdPct,
		TTradeSpreadPct:            ttradeSpreadPct,
		ProfitDrawdownThresholdPct: profitDrawdownThresholdPct,
		EnableSmallPositionClose:   enableSmallPositionClose,
		FeePct:                     feePct,
		ScoreMode:                  scoreMode,
	}

	baseline := backtest.Simulate(klines, startIdx, totalInvestment, initial)
	c.SSEvent("baseline", gin.H{"params": initial, "result": baseline})
	c.Writer.Flush()

	eval := func(p backtest.GridParams) backtest.SimResult {
		return backtest.Simulate(klines, startIdx, totalInvestment, p)
	}

	clientGone := c.Request.Context().Done()
	// progressEvery avoids flooding the SSE stream on high iteration counts —
	// at most ~50 progress events per run regardless of size.
	progressEvery := iterations / 50
	if progressEvery < 1 {
		progressEvery = 1
	}

	cfg := backtest.AnnealConfig{
		Iterations:  iterations,
		InitialTemp: 10.0,
		CoolingRate: 1 - 5.0/float64(iterations),
		Seed:        seed,
		OnProgress: func(iteration int, bestScore float64) {
			if iteration%progressEvery != 0 && iteration != iterations {
				return
			}
			select {
			case <-clientGone:
				return
			default:
			}
			c.SSEvent("progress", gin.H{
				"iteration":  iteration,
				"iterations": iterations,
				"best_score": bestScore,
			})
			c.Writer.Flush()
		},
	}

	result := backtest.Anneal(initial, eval, cfg)

	select {
	case <-clientGone:
		return
	default:
	}
	c.SSEvent("done", gin.H{"params": result.Best, "result": result.BestResult})
	c.Writer.Flush()
}

// queryInt reads an integer query param with a default and clamps it to
// [min, max] so a bad/malicious value can't request an absurd backtest size.
func queryInt(c *gin.Context, key string, def, min, max int) int {
	raw := c.Query(key)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func queryBool(c *gin.Context, key string, def bool) bool {
	raw := c.Query(key)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return def
	}
	return v
}

func queryFloat(c *gin.Context, key string, def, min, max float64) float64 {
	raw := c.Query(key)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

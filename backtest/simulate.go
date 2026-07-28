package backtest

import (
	"nofx/market"
)

// sideState tracks one side's (long or short) accumulated grid position.
type sideState struct {
	Qty            float64 // base-asset size, always >= 0
	EntryPrice     float64 // volume-weighted average fill price
	AlreadyReduced float64 // highest profit-reduce step already applied, resets to 0 when profit <= 0
	ReduceCount    int
}

func (s *sideState) addFill(qty, price float64) {
	if s.Qty+qty <= 0 {
		return
	}
	s.EntryPrice = (s.EntryPrice*s.Qty + price*qty) / (s.Qty + qty)
	s.Qty += qty
}

// unrealizedPnL and margin follow the same formulas as
// trader.checkProfitReduce: margin = notional / leverage,
// pnl = (mark-entry)*qty for long, (entry-mark)*qty for short.
func (s *sideState) unrealizedPnL(mark float64, isLong bool) float64 {
	if s.Qty == 0 {
		return 0
	}
	if isLong {
		return (mark - s.EntryPrice) * s.Qty
	}
	return (s.EntryPrice - mark) * s.Qty
}

func (s *sideState) margin(leverage int) float64 {
	if s.Qty == 0 || leverage <= 0 {
		return 0
	}
	return s.Qty * s.EntryPrice / float64(leverage)
}

// applyProfitReduce ports trader.checkProfitReduce's per-side step logic.
// Returns the realized PnL freed by the reduce (added to cash, removed from
// the floating position) and whether a reduce fired this bar.
func applyProfitReduce(s *sideState, mark float64, isLong bool, leverage int, stepPct, multiplier float64) (realizedPnL float64, fired bool) {
	if s.Qty == 0 || stepPct <= 0 {
		return 0, false
	}
	margin := s.margin(leverage)
	if margin <= 0 {
		return 0, false
	}
	pnl := s.unrealizedPnL(mark, isLong)
	profitPct := pnl / margin * 100

	if profitPct <= 0 {
		s.AlreadyReduced = 0
		return 0, false
	}

	targetStep := stepFloor(profitPct, stepPct)
	if targetStep <= s.AlreadyReduced {
		return 0, false
	}

	mult := multiplier
	if mult <= 0 {
		mult = 1.0
	}
	reduceQty := s.Qty * (targetStep / 100) * mult
	if reduceQty > s.Qty {
		reduceQty = s.Qty
	}
	if reduceQty <= 0 {
		return 0, false
	}

	// Realize PnL proportional to the reduced fraction, then shrink the
	// position. EntryPrice is unchanged (matches real reduce-only orders,
	// which close part of the position at the current mark price).
	fraction := reduceQty / s.Qty
	realized := pnl * fraction
	s.Qty -= reduceQty
	s.AlreadyReduced = targetStep
	s.ReduceCount++
	return realized, true
}

func stepFloor(profitPct, step float64) float64 {
	steps := int(profitPct / step)
	return float64(steps) * step
}

// Simulate runs one backtest pass over klines (ascending time order) for the
// given parameter set, starting at klines[startIdx]. Bars before startIdx are
// used only to seed the ATR14 lookback window — they are never used to size
// or fill the grid, avoiding lookahead.
//
// Fill model: a level's limit order is considered filled the first bar whose
// [Low, High] range covers the level's price. This is the standard
// simplification for grid backtesting in the absence of exchange order-book
// data; it does not model partial fills, maker queue position, or fees.
// Equity/drawdown are evaluated on each bar's Close, so intrabar spikes
// through a level (not settled by Close) are not reflected in the drawdown
// figure — a known limitation, not a bug.
func Simulate(klines []market.Kline, startIdx int, totalInvestment float64, p GridParams) SimResult {
	if startIdx >= len(klines) {
		return SimResult{}
	}
	firstBar := klines[startIdx]
	atr14 := atrAt(klines, startIdx)
	upper, lower := computeBounds(firstBar.Close, atr14, p)
	levels, _, err := buildLevels(firstBar.Close, upper, lower, totalInvestment, p)
	if err != nil {
		return SimResult{BlewUp: true, Score: -1e9}
	}
	for i := range levels {
		notional := levels[i].AllocatedUSD * float64(p.Leverage)
		if levels[i].Price > 0 {
			levels[i].Qty = notional / levels[i].Price
		}
	}

	var long, short sideState
	cashReleased := 0.0 // realized PnL from profit-reduce, added back to equity
	peakEquity := totalInvestment
	maxDrawdownPct := 0.0
	filledCount := 0

	equityAt := func(mark float64) float64 {
		return totalInvestment + cashReleased + long.unrealizedPnL(mark, true) + short.unrealizedPnL(mark, false)
	}

	for i := startIdx; i < len(klines); i++ {
		bar := klines[i]

		// 1. Fill detection.
		for li := range levels {
			lv := &levels[li]
			if lv.Filled {
				continue
			}
			if bar.Low <= lv.Price && lv.Price <= bar.High {
				lv.Filled = true
				filledCount++
				if lv.Side == "buy" {
					long.addFill(lv.Qty, lv.Price)
				} else {
					short.addFill(lv.Qty, lv.Price)
				}
			}
		}

		// 2. Profit-reduce check (once per bar, mirrors the live WS-driven cadence).
		if realized, fired := applyProfitReduce(&long, bar.Close, true, p.Leverage, p.ProfitReduceStepPct, p.ProfitReduceMultiplier); fired {
			cashReleased += realized
		}
		if realized, fired := applyProfitReduce(&short, bar.Close, false, p.Leverage, p.ProfitReduceStepPct, p.ProfitReduceMultiplier); fired {
			cashReleased += realized
		}

		// 3. Equity / drawdown tracking.
		equity := equityAt(bar.Close)
		if equity <= 0 {
			return SimResult{
				FinalEquity:    0,
				ReturnPct:      -100,
				MaxDrawdownPct: 100,
				FilledLevels:   filledCount,
				LongReduces:    long.ReduceCount,
				ShortReduces:   short.ReduceCount,
				BlewUp:         true,
				Score:          -1e9,
			}
		}
		if equity > peakEquity {
			peakEquity = equity
		} else if peakEquity > 0 {
			dd := (peakEquity - equity) / peakEquity * 100
			if dd > maxDrawdownPct {
				maxDrawdownPct = dd
			}
		}
	}

	finalEquity := equityAt(klines[len(klines)-1].Close)
	returnPct := (finalEquity - totalInvestment) / totalInvestment * 100

	return SimResult{
		FinalEquity:    finalEquity,
		ReturnPct:      returnPct,
		MaxDrawdownPct: maxDrawdownPct,
		FilledLevels:   filledCount,
		LongReduces:    long.ReduceCount,
		ShortReduces:   short.ReduceCount,
		BlewUp:         false,
		Score:          Score(returnPct, maxDrawdownPct),
	}
}

// Score is the annealing objective: reward return, penalize drawdown.
// Tune drawdownPenalty to taste — higher values favor safer parameter sets
// over raw return.
func Score(returnPct, maxDrawdownPct float64) float64 {
	const drawdownPenalty = 1.5
	return returnPct - drawdownPenalty*maxDrawdownPct
}

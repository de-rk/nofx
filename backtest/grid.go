package backtest

import (
	"fmt"
	"math"

	"nofx/market"
)

// computeBounds ports trader.calculateATRBounds / calculateDefaultBounds.
// atr14 must be pre-computed by the caller from klines preceding the
// simulation window (never from data inside the window, to avoid lookahead).
func computeBounds(price, atr14 float64, p GridParams) (upper, lower float64) {
	if atr14 <= 0 {
		multiplier := 0.03 * float64(p.GridCount) / 10
		return price * (1 + multiplier), price * (1 - multiplier)
	}
	mult := p.ATRMultiplier
	if mult <= 0 {
		mult = 2.0
	}
	halfRange := atr14 * mult
	return price + halfRange, price - halfRange
}

// buildLevels ports trader.initializeGridLevels.
func buildLevels(currentPrice, upper, lower, totalInvestment float64, p GridParams) ([]simLevel, float64, error) {
	if p.GridCount < 2 {
		return nil, 0, fmt.Errorf("grid_count must be >= 2, got %d", p.GridCount)
	}
	spacing := (upper - lower) / float64(p.GridCount-1)

	weights := make([]float64, p.GridCount)
	totalWeight := 0.0
	for i := 0; i < p.GridCount; i++ {
		switch p.Distribution {
		case "gaussian":
			center := float64(p.GridCount-1) / 2
			sigma := float64(p.GridCount) / 4
			weights[i] = math.Exp(-math.Pow(float64(i)-center, 2) / (2 * sigma * sigma))
		case "pyramid":
			weights[i] = float64(p.GridCount - i)
		default: // uniform
			weights[i] = 1.0
		}
		totalWeight += weights[i]
	}

	levels := make([]simLevel, p.GridCount)
	for i := 0; i < p.GridCount; i++ {
		price := lower + float64(i)*spacing
		allocatedUSD := totalInvestment * weights[i] / totalWeight
		side := "buy"
		if price > currentPrice {
			side = "sell"
		}
		levels[i] = simLevel{
			Price:        price,
			AllocatedUSD: allocatedUSD,
			Side:         side,
		}
	}
	return levels, spacing, nil
}

// atr14Period matches market.calculateATR's period as used throughout this
// package (grid bound sizing and mid-run resets both use ATR14).
const atr14Period = 14

// atrSeries returns, for every idx in [0, len(klines)], the ATR14 value
// that market.ExportCalculateATR(klines[:idx], atr14Period) would have
// returned — i.e. the Wilder-smoothed ATR as of having seen exactly the
// first idx bars, with idx==0 unused and idx<=atr14Period holding 0 (matching
// calculateATR's own "not enough bars yet" guard).
//
// This replaces the once-per-call atrAt(klines, idx), which recomputed the
// full true-range series and re-ran Wilder smoothing from scratch on every
// call — O(idx) per call, and since the simulation's main loop called it once
// per bar (backtest/simulate.go's per-bar reset check), the whole run was
// O(n^2) in the number of K-lines. Wilder smoothing is already an O(1)
// per-step recurrence (atr = (atr*(period-1) + tr)/period), so computing it
// once, incrementally, for every idx up front is both exact — the same
// arithmetic in the same order, so results are identical, not merely close —
// and O(n) overall.
func atrSeries(klines []market.Kline) []float64 {
	n := len(klines)
	atr := make([]float64, n+1) // atr[idx] corresponds to klines[:idx]
	if n <= atr14Period {
		return atr // every idx <= n <= period, all zero per the guard above
	}

	trs := make([]float64, n) // trs[0] unused; trs[i] is the true range of klines[i] vs klines[i-1]
	for i := 1; i < n; i++ {
		high := klines[i].High
		low := klines[i].Low
		prevClose := klines[i-1].Close
		tr1 := high - low
		tr2 := math.Abs(high - prevClose)
		tr3 := math.Abs(low - prevClose)
		trs[i] = math.Max(tr1, math.Max(tr2, tr3))
	}

	sum := 0.0
	for i := 1; i <= atr14Period; i++ {
		sum += trs[i]
	}
	value := sum / float64(atr14Period)
	atr[atr14Period+1] = value
	for idx := atr14Period + 2; idx <= n; idx++ {
		value = (value*float64(atr14Period-1) + trs[idx-1]) / float64(atr14Period)
		atr[idx] = value
	}
	return atr
}

// checkGridSkew ports trader.checkGridSkew: the grid is considered skewed if
// one side has filled >=3x more levels than the other (both sides having
// filled more than 5), or if one side has filled at least one level while
// the other has never filled anything and has more than 5 unfilled levels.
// Unlike live, this simplified fill model has no distinct "pending" (order
// resting) vs "empty" (no order placed) level state — every unfilled level
// here is instantaneously fillable, closer in spirit to live's "pending"
// than "empty" — so the "one side completely dead" branch is approximated
// using unfilled-level counts rather than live's stricter "empty" count.
func checkGridSkew(levels []simLevel) bool {
	buyFilled, sellFilled, buyUnfilled, sellUnfilled := 0, 0, 0, 0
	for _, lv := range levels {
		if lv.Side == "buy" {
			if lv.Filled {
				buyFilled++
			} else {
				buyUnfilled++
			}
		} else {
			if lv.Filled {
				sellFilled++
			} else {
				sellUnfilled++
			}
		}
	}
	if buyFilled > 0 && sellFilled == 0 && sellUnfilled > 5 {
		return true
	}
	if sellFilled > 0 && buyFilled == 0 && buyUnfilled > 5 {
		return true
	}
	if buyFilled >= 3*sellFilled && buyFilled > 5 {
		return true
	}
	if sellFilled >= 3*buyFilled && sellFilled > 5 {
		return true
	}
	return false
}

// maybeResetGrid ports trader.autoAdjustGrid + resetGrid. If the grid is
// heavily skewed (checkGridSkew) AND the current price has drifted more than
// 30% of the grid range away from its center — the same two-part gate live
// uses before paying the cost of a rebuild — the grid is rebuilt around the
// current price: bounds are recalculated (computeBounds) and levels rebuilt
// from scratch (buildLevels), then any currently-filled level's position is
// carried over to the nearest new level by price. This is exact here (unlike
// live's PositionEntry-based nearest-match, which accounts for real fill
// slippage) since this simulator's fills never slip from a level's target
// price. Returns the levels to use going forward (the original slice,
// unchanged, if no reset fired) and whether a reset happened.
func maybeResetGrid(levels []simLevel, currentPrice, atr14, totalInvestment float64, p GridParams) ([]simLevel, bool) {
	if !checkGridSkew(levels) {
		return levels, false
	}

	lower, upper := levels[0].Price, levels[0].Price
	for _, lv := range levels {
		if lv.Price < lower {
			lower = lv.Price
		}
		if lv.Price > upper {
			upper = lv.Price
		}
	}
	gridRange := upper - lower
	midPrice := (upper + lower) / 2
	if gridRange <= 0 || math.Abs(currentPrice-midPrice) < gridRange*0.3 {
		return levels, false
	}

	newUpper, newLower := computeBounds(currentPrice, atr14, p)
	newLevels, _, err := buildLevels(currentPrice, newUpper, newLower, totalInvestment, p)
	if err != nil {
		return levels, false
	}
	for i := range newLevels {
		notional := newLevels[i].AllocatedUSD * float64(p.Leverage)
		if newLevels[i].Price > 0 {
			newLevels[i].Qty = notional / newLevels[i].Price
		}
	}

	for _, old := range levels {
		if !old.Filled {
			continue
		}
		closestIdx := -1
		closestDist := math.MaxFloat64
		for i, nl := range newLevels {
			if dist := math.Abs(nl.Price - old.Price); dist < closestDist {
				closestDist = dist
				closestIdx = i
			}
		}
		if closestIdx >= 0 {
			newLevels[closestIdx].Filled = true
			newLevels[closestIdx].Qty = old.Qty
		}
	}

	return newLevels, true
}

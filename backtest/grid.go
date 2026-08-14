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
			// Symmetric around center (see trader.initializeGridLevels'
			// comment) — a one-sided "GridCount - i" only grows weight
			// toward the buy side and shrinks it toward the sell side,
			// inverting the intended dollar-cost-averaging shape above
			// the current price.
			center := float64(p.GridCount-1) / 2
			weights[i] = 1 + math.Abs(float64(i)-center)
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

// checkGridSkew checks if current price has moved outside the grid boundaries.
// Ported from trader.checkGridSkew after 2026-08-09 refactor: the grid reset
// trigger is now based on price position (exceeding upper*1.03 or below lower*0.97)
// rather than filled-level imbalance, matching live behavior for consistency.
// Returns true if the grid should be reset.
func checkGridSkew(levels []simLevel, currentPrice float64) bool {
	if len(levels) == 0 {
		return false
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

	// Reset if price has moved outside the grid boundaries by more than 3%.
	// This triggers when price exceeds upper + 3% or falls below lower - 3%.
	upperThreshold := upper * 1.03
	lowerThreshold := lower * 0.97
	return currentPrice > upperThreshold || currentPrice < lowerThreshold
}

// maybeResetGrid checks if the grid needs to be reset based on price position.
// Ported from trader.autoAdjustGrid after 2026-08-09 refactor: simplified to
// rely entirely on checkGridSkew's price-boundary logic (currentPrice exceeding
// upper*1.03 or below lower*0.97), removing the duplicate 30%-drift check that
// live previously had. If reset is triggered, the grid is rebuilt around the
// current price: bounds are recalculated (computeBounds) and levels rebuilt
// from scratch (buildLevels), then any currently-filled level's position is
// carried over to the nearest new level by price. This is exact here (unlike
// live's PositionEntry-based nearest-match, which accounts for real fill
// slippage) since this simulator's fills never slip from a level's target
// price. Returns the levels to use going forward (the original slice,
// unchanged, if no reset fired) and whether a reset happened.
func maybeResetGrid(levels []simLevel, currentPrice, atr14, totalInvestment float64, p GridParams) ([]simLevel, bool) {
	if !checkGridSkew(levels, currentPrice) {
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

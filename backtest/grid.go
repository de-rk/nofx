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

// atrAt computes ATR14 (Wilder-style, matching market.ExportCalculateATR)
// using only klines[0:idx] (exclusive of idx) so the simulation never looks
// ahead into the future relative to the current bar.
func atrAt(klines []market.Kline, idx int) float64 {
	const period = 14
	if idx <= period {
		return 0
	}
	return market.ExportCalculateATR(klines[:idx], period)
}

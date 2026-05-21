package trader

import (
	"math"
	"nofx/market"
	"time"
)

// ============================================================================
// Task 6: Regime Level Classification
// ============================================================================

// classifyRegimeLevel determines the regime level based on market indicators
// bollingerWidth: Bollinger band width as percentage
// atr14Pct: ATR14 as percentage of current price
func classifyRegimeLevel(bollingerWidth, atr14Pct float64) market.RegimeLevel {
	// Narrow: Bollinger < 2%, ATR < 1%
	if bollingerWidth < 2.0 && atr14Pct < 1.0 {
		return market.RegimeLevelNarrow
	}

	// Standard: Bollinger 2-3%, ATR 1-2%
	if bollingerWidth <= 3.0 && atr14Pct <= 2.0 {
		return market.RegimeLevelStandard
	}

	// Wide: Bollinger 3-4%, ATR 2-3%
	if bollingerWidth <= 4.0 && atr14Pct <= 3.0 {
		return market.RegimeLevelWide
	}

	// Volatile: Bollinger > 4%, ATR > 3%
	return market.RegimeLevelVolatile
}

// interpolate calculates a value based on linear interpolation between two points
func interpolate(current, low, high, lowVal, highVal float64) float64 {
	if current <= low {
		return lowVal
	}
	if current >= high {
		return highVal
	}
	return lowVal + (highVal-lowVal)*(current-low)/(high-low)
}

// getDynamicLeverage returns a continuous leverage limit based on bollinger width
func getDynamicLeverage(bollingerWidth float64) int {
	// Define mapping points: {width, leverage}
	// 2% -> 2x, 4% -> 4x, 6% -> 2x
	var leverage float64
	if bollingerWidth < 2.0 {
		leverage = 2.0
	} else if bollingerWidth < 4.0 {
		leverage = interpolate(bollingerWidth, 2.0, 4.0, 2.0, 4.0)
	} else if bollingerWidth < 6.0 {
		leverage = interpolate(bollingerWidth, 4.0, 6.0, 4.0, 2.0)
	} else {
		leverage = 2.0
	}
	return int(math.Round(leverage))
}

// getDynamicPositionLimit returns a continuous position limit percentage based on bollinger width
func getDynamicPositionLimit(bollingerWidth float64) float64 {
	// Define mapping points: {width, limitPct}
	// 2% -> 40%, 4% -> 70%, 6% -> 40%
	if bollingerWidth < 2.0 {
		return 40.0
	} else if bollingerWidth < 4.0 {
		return interpolate(bollingerWidth, 2.0, 4.0, 40.0, 70.0)
	} else if bollingerWidth < 6.0 {
		return interpolate(bollingerWidth, 4.0, 6.0, 70.0, 40.0)
	} else {
		return 40.0
	}
}

// ============================================================================
// Task 7: Breakout Detection
// ============================================================================

// detectBoxBreakout checks if price has broken out of any box level
// Returns the highest breakout level and direction
func detectBoxBreakout(box *market.BoxData) (market.BreakoutLevel, string) {
	if box == nil {
		return market.BreakoutNone, ""
	}

	price := box.CurrentPrice

	// Check long box first (highest priority)
	if price > box.LongUpper {
		return market.BreakoutLong, "up"
	}
	if price < box.LongLower {
		return market.BreakoutLong, "down"
	}

	// Check mid box
	if price > box.MidUpper {
		return market.BreakoutMid, "up"
	}
	if price < box.MidLower {
		return market.BreakoutMid, "down"
	}

	// Check short box
	if price > box.ShortUpper {
		return market.BreakoutShort, "up"
	}
	if price < box.ShortLower {
		return market.BreakoutShort, "down"
	}

	return market.BreakoutNone, ""
}

// ============================================================================
// Task 8: Breakout Confirmation Logic
// ============================================================================

const BreakoutConfirmRequired = 2 // 2 candles to confirm breakout

// BreakoutState tracks the current breakout state
type BreakoutState struct {
	Level        market.BreakoutLevel
	Direction    string
	ConfirmCount int
	StartTime    time.Time
}

// confirmBreakout updates breakout state and returns true if breakout is confirmed
func confirmBreakout(state *BreakoutState, currentLevel market.BreakoutLevel, direction string) bool {
	// If price returned to box, reset state
	if currentLevel == market.BreakoutNone {
		state.ConfirmCount = 0
		state.Level = market.BreakoutNone
		state.Direction = ""
		return false
	}

	// If same breakout continues, increment count
	if state.Level == currentLevel && state.Direction == direction {
		state.ConfirmCount++
	} else {
		// New breakout, reset count
		state.Level = currentLevel
		state.Direction = direction
		state.ConfirmCount = 1
		state.StartTime = time.Now()
	}

	return state.ConfirmCount >= BreakoutConfirmRequired
}

// ============================================================================
// Task 9: Breakout Handler
// ============================================================================

// BreakoutAction represents the action to take on breakout
type BreakoutAction int

const (
	BreakoutActionNone BreakoutAction = iota
	BreakoutActionReducePosition // Short box breakout: reduce to 50%
	BreakoutActionPauseGrid      // Mid box breakout: pause grid + cancel orders
	BreakoutActionCloseAll       // Long box breakout: pause + cancel + close all
)

// getBreakoutAction returns the appropriate action for a breakout level
func getBreakoutAction(level market.BreakoutLevel) BreakoutAction {
	switch level {
	case market.BreakoutShort:
		return BreakoutActionReducePosition
	case market.BreakoutMid:
		return BreakoutActionPauseGrid
	case market.BreakoutLong:
		return BreakoutActionCloseAll
	default:
		return BreakoutActionNone
	}
}

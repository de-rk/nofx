package trader

import (
	"math"
	"nofx/logger"
	"time"
)

// ============================================================================
// T-Trade Enhanced Logic (T字交易增强逻辑)
// ============================================================================
// This file implements enhanced T-trade functionality supporting both:
// - LONG trapped: place_buy_limit at LOWER price → reduce_long at HIGHER price
// - SHORT trapped: place_sell_limit at HIGHER price → reduce_short at LOWER price
//
// Key improvements:
// 1. Bidirectional T-trade support (both long and short trapped positions)
// 2. Dynamic ATR-based spread calculation
// 3. Enhanced order state tracking

// TTradeState represents the state of a T-trade operation
type TTradeState struct {
	PrepOrderID   string    // Prep order ID (buy for long, sell for short)
	PrepPrice     float64   // Price of prep order
	PrepQty       float64   // Quantity of prep order
	PlacedAt      time.Time // When prep order was placed
	Side          string    // "buy" (long trapped) or "sell" (short trapped)
	PendingReduce float64   // Reduce quantity waiting for prep fill
	ReduceOrderID string    // Reduce limit order ID (after prep fills)
	ATR           float64   // ATR at initiation time for dynamic spread
}

// IsPending returns true if there's a pending T-trade operation
func (t *TTradeState) IsPending() bool {
	return t.PrepOrderID != "" && t.PendingReduce > 0
}

// IsLongTrapped returns true if this is a long position T-trade
func (t *TTradeState) IsLongTrapped() bool {
	return t.Side == "buy"
}

// IsShortTrapped returns true if this is a short position T-trade
func (t *TTradeState) IsShortTrapped() bool {
	return t.Side == "sell"
}

// CalculateDynamicSpread calculates the spread percentage based on ATR
// Higher volatility = wider spread for better fill probability
// Lower volatility = tighter spread for better profit
func CalculateDynamicSpread(atr, currentPrice float64, baseSpread float64) float64 {
	if atr <= 0 || currentPrice <= 0 {
		return baseSpread // Fallback to base spread
	}

	// ATR as percentage of price
	atrPct := atr / currentPrice * 100

	// Base spread is minimum, scale with volatility
	// Low vol (< 1% ATR): base spread
	// Medium vol (1-3% ATR): base + 0.25% per 1% ATR
	// High vol (> 3% ATR): base + 0.5% per 1% ATR above 3%
	var dynamicSpread float64

	switch {
	case atrPct < 1.0:
		// Low volatility: use base spread
		dynamicSpread = baseSpread
	case atrPct < 3.0:
		// Medium volatility: scale linearly
		dynamicSpread = baseSpread + (atrPct-1.0)*0.25
	default:
		// High volatility: cap at reasonable level
		dynamicSpread = baseSpread + 0.5 + (atrPct-3.0)*0.15
	}

	// Cap spread at 3% maximum
	return math.Min(dynamicSpread, 3.0)
}

// CalculateReducePrice calculates the reduce order price based on T-trade side
// For LONG trapped: reduce price should be ABOVE prep buy price
// For SHORT trapped: reduce price should be BELOW prep sell price
func (t *TTradeState) CalculateReducePrice(currentPrice float64, baseSpread float64) float64 {
	if t.PrepPrice <= 0 {
		return currentPrice // Fallback to current price
	}

	// Calculate dynamic spread
	spread := CalculateDynamicSpread(t.ATR, currentPrice, baseSpread)

	if t.IsLongTrapped() {
		// Long trapped: sell higher than buy price
		return t.PrepPrice * (1 + spread/100)
	} else {
		// Short trapped: buy lower than sell price
		return t.PrepPrice * (1 - spread/100)
	}
}

// CalculatePrepPrice calculates the optimal prep order price
// For LONG trapped: buy at LOWER price (below current)
// For SHORT trapped: sell at HIGHER price (above current)
func CalculatePrepPrice(currentPrice, atr float64, side string) float64 {
	if atr <= 0 {
		// Fallback: use 0.5% distance
		if side == "buy" {
			return currentPrice * 0.995
		}
		return currentPrice * 1.005
	}

	// Use 0.3-0.5 ATR distance for prep order
	atrDistance := atr * 0.4
	if side == "buy" {
		return currentPrice - atrDistance
	}
	return currentPrice + atrDistance
}

// ValidateTTradeSignal validates if T-trade should be initiated
// Returns: (shouldInitiate bool, reason string)
func ValidateTTradeSignal(side string, lossPct, thresholdPct, rsi float64, priceDiffPct float64) (bool, string) {
	// Check if loss exceeds threshold
	if lossPct < thresholdPct {
		return false, "loss below threshold"
	}

	// For long trapped, check if price is still declining (RSI not oversold)
	if side == "buy" {
		if rsi < 30 {
			return false, "RSI oversold, price may rebound"
		}
		if priceDiffPct < 0 {
			// Price is above entry, not trapped
			return false, "price above entry"
		}
		return true, "valid long trapped signal"
	}

	// For short trapped, check if price is still rising (RSI not overbought)
	if side == "sell" {
		if rsi > 70 {
			return false, "RSI overbought, price may decline"
		}
		if priceDiffPct > 0 {
			// Price is below entry, not trapped
			return false, "price below entry"
		}
		return true, "valid short trapped signal"
	}

	return false, "invalid side"
}

// GetTTradePhaseDescription returns human-readable phase description
func (t *TTradeState) GetTTradePhaseDescription() string {
	if t.PrepOrderID == "" {
		return "idle"
	}
	if t.ReduceOrderID != "" {
		return "reduce_pending"
	}
	return "waiting_prep_fill"
}

// LogTTradeState logs the current T-trade state for debugging
func LogTTradeState(t *TTradeState, prefix string) {
	if t == nil || !t.IsPending() {
		return
	}

	phase := t.GetTTradePhaseDescription()
	side := "LONG"
	if t.IsShortTrapped() {
		side = "SHORT"
	}

	logger.Infof("[Grid] %s T-Trade State: phase=%s, side=%s, prep_price=%.2f, prep_qty=%.4f, pending_reduce=%.4f, atr=%.2f",
		prefix, phase, side, t.PrepPrice, t.PrepQty, t.PendingReduce, t.ATR)
}

package trader

import (
	"encoding/json"
	"fmt"
	"math"
	"nofx/kernel"
	"nofx/logger"
	"nofx/market"
	"nofx/store"
	"nofx/trader/types"
	"sync"
	"time"
)

// ============================================================================
// Grid Trading State Management
// ============================================================================

// GridState holds the runtime state for grid trading
type GridState struct {
	mu sync.RWMutex

	// Configuration
	Config *store.GridStrategyConfig

	// Grid levels
	Levels []kernel.GridLevelInfo

	// Calculated bounds
	UpperPrice  float64
	LowerPrice  float64
	GridSpacing float64

	// State flags
	IsPaused      bool
	IsInitialized bool

	// Performance tracking
	TotalProfit    float64
	TotalTrades    int
	WinningTrades  int
	MaxDrawdown    float64
	PeakEquity     float64
	DailyPnL       float64
	LastDailyReset time.Time

	// Order tracking
	OrderBook map[string]int // OrderID -> LevelIndex

	// Decision memory (last 5 non-hold decisions)
	DecisionMemory []kernel.DecisionSummary

	// Box state
	ShortBoxUpper float64
	ShortBoxLower float64
	MidBoxUpper   float64
	MidBoxLower   float64
	LongBoxUpper  float64
	LongBoxLower  float64

	// Breakout state
	BreakoutLevel        string
	BreakoutDirection    string
	BreakoutConfirmCount int

	// Position reduction (0 = normal, 50 = reduced after false breakout)
	PositionReductionPct float64

	// Current regime level
	CurrentRegimeLevel string

	// Grid direction adjustment
	CurrentDirection     market.GridDirection
	DirectionChangedAt   time.Time
	DirectionChangeCount int

	// Trapped position reduction tracking (被套减仓追踪)
	LastTrappedReduceAt time.Time // time of last batch reduction
	TrappedReduceCount  int       // total number of batch reductions performed

	// T-trade state machine (T字操作状态机)
	// Phase 1: place low buy order → Phase 2: wait for fill → Phase 3: execute reduce
	TTradePrepOrderID      string    // order ID of the T-trade prep buy order (waiting for fill)
	TTradePrepPrice        float64   // price of the T-trade buy order
	TTradePrepQty          float64   // quantity of the T-trade buy order
	TTradePrepPlacedAt     time.Time // when the T-trade buy was placed
	TTradePendingReduceQty float64   // reduce qty deferred until T-trade buy fills (0 = none pending)
	TTradeReduceOrderID    string    // order ID of the reduce limit order (after buy fills)
	TTradeReducePlacedAt   time.Time // when the reduce order was placed (for timeout)
	TTradePrepSide         string    // "buy" = long trapped prep, "sell" = short trapped prep
	TTradePrepExecuted     bool      // true once deferred reduce has been dispatched (prevents double-execution)

	// Profit-based reduce tracking (per side)
	LongProfitReducedPct  float64 // cumulative % already reduced for long (multiples of 10)
	ShortProfitReducedPct float64 // cumulative % already reduced for short (multiples of 10)

	// T-trade ready-to-reduce state (set after prep order fills, cleared after AI reduce order placed)
	TTradeReadyToReduce     bool    // true = prep filled, AI should now place reduce order
	TTradeReadyReduceQty    float64 // qty to reduce
	TTradeReadyPrepPrice    float64 // fill price of the prep order (reduce price must be better than this)

	// T-trade placed reduce order tracking (for cancel detection and re-place)
	TTradeReduceQty   float64 // qty of the placed reduce order (for re-place on cancel)
	TTradeReducePrice float64 // price of the placed reduce order (for re-place on cancel)
	TTradeReduceSide  string  // "sell" (reduce_long) or "buy" (reduce_short)
}

// NewGridState creates a new grid state
func NewGridState(config *store.GridStrategyConfig) *GridState {
	return &GridState{
		Config:           config,
		Levels:           make([]kernel.GridLevelInfo, 0),
		OrderBook:        make(map[string]int),
		CurrentDirection: market.GridDirectionNeutral,
	}
}

// ============================================================================
// Breakout Detection
// ============================================================================

// BreakoutType represents the type of price breakout
type BreakoutType string

const (
	BreakoutNone  BreakoutType = "none"
	BreakoutUpper BreakoutType = "upper"
	BreakoutLower BreakoutType = "lower"
)

// checkBreakout detects if price has broken out of grid range
// Returns breakout type and percentage beyond boundary
func (at *AutoTrader) checkBreakout() (BreakoutType, float64) {
	gridConfig := at.config.StrategyConfig.GridConfig

	currentPrice, err := at.trader.GetMarketPrice(gridConfig.Symbol)
	if err != nil {
		return BreakoutNone, 0
	}

	at.gridState.mu.RLock()
	upper := at.gridState.UpperPrice
	lower := at.gridState.LowerPrice
	at.gridState.mu.RUnlock()

	if upper <= 0 || lower <= 0 {
		return BreakoutNone, 0
	}

	// Check upper breakout
	if currentPrice > upper {
		breakoutPct := (currentPrice - upper) / upper * 100
		return BreakoutUpper, breakoutPct
	}

	// Check lower breakout
	if currentPrice < lower {
		breakoutPct := (lower - currentPrice) / lower * 100
		return BreakoutLower, breakoutPct
	}

	return BreakoutNone, 0
}

// checkMaxDrawdown checks if current drawdown exceeds maximum allowed.
// Uses availableBalance (free capital not locked in positions) so that capital
// transferred out of the trading account does not falsely inflate drawdown.
// Returns: (exceeded bool, currentDrawdown float64)
func (at *AutoTrader) checkMaxDrawdown() (bool, float64) {
	gridConfig := at.config.StrategyConfig.GridConfig
	if gridConfig.MaxDrawdownPct <= 0 {
		return false, 0
	}

	balance, err := at.trader.GetBalance()
	if err != nil {
		return false, 0
	}

	// Use availableBalance: free capital only, excludes margin locked in open positions
	// and unrealized PnL, so transfers out and position closes don't trigger false exits.
	currentEquity := 0.0
	if avail, ok := balance["availableBalance"].(float64); ok && avail > 0 {
		currentEquity = avail
	} else if wallet, ok := balance["totalWalletBalance"].(float64); ok && wallet > 0 {
		currentEquity = wallet
	} else if equity, ok := balance["totalEquity"].(float64); ok {
		currentEquity = equity
	}

	if currentEquity <= 0 {
		return false, 0
	}

	// Update peak equity
	at.gridState.mu.Lock()
	if currentEquity > at.gridState.PeakEquity {
		at.gridState.PeakEquity = currentEquity
	}
	peakEquity := at.gridState.PeakEquity
	at.gridState.mu.Unlock()

	if peakEquity <= 0 {
		return false, 0
	}

	drawdown := (peakEquity - currentEquity) / peakEquity * 100
	logger.Warnf("[RiskControl] max_drawdown check: available=%.2f, peak=%.2f, drawdown=%.2f%%, threshold=%.2f%%",
		currentEquity, peakEquity, drawdown, gridConfig.MaxDrawdownPct)

	at.gridState.mu.Lock()
	if drawdown > at.gridState.MaxDrawdown {
		at.gridState.MaxDrawdown = drawdown
	}
	at.gridState.mu.Unlock()

	return drawdown >= gridConfig.MaxDrawdownPct, drawdown
}

// checkDailyLossLimit checks if daily loss exceeds limit.
// Uses availableBalance as the denominator so only free capital is counted.
// Returns: (exceeded bool, dailyLossPct float64)
func (at *AutoTrader) checkDailyLossLimit() (bool, float64) {
	gridConfig := at.config.StrategyConfig.GridConfig
	if gridConfig.DailyLossLimitPct <= 0 {
		return false, 0
	}

	at.gridState.mu.Lock()
	// Reset daily PnL if new day
	now := time.Now()
	if now.YearDay() != at.gridState.LastDailyReset.YearDay() ||
		now.Year() != at.gridState.LastDailyReset.Year() {
		at.gridState.DailyPnL = 0
		at.gridState.LastDailyReset = now
	}
	dailyPnL := at.gridState.DailyPnL
	at.gridState.mu.Unlock()

	if dailyPnL >= 0 {
		return false, 0
	}

	// Use availableBalance as denominator (free capital, excludes locked margin)
	balance, err := at.trader.GetBalance()
	denominator := gridConfig.TotalInvestment
	if err == nil {
		if avail, ok := balance["availableBalance"].(float64); ok && avail > 0 {
			denominator = avail
		}
	}

	dailyLossPct := 0.0
	if denominator > 0 {
		dailyLossPct = (-dailyPnL) / denominator * 100
	}

	logger.Warnf("[RiskControl] daily_loss check: dailyPnL=%.2f, available=%.2f, loss=%.2f%%, threshold=%.2f%%",
		dailyPnL, denominator, dailyLossPct, gridConfig.DailyLossLimitPct)

	return dailyLossPct >= gridConfig.DailyLossLimitPct, dailyLossPct
}

// updateDailyPnL updates the daily PnL tracking
func (at *AutoTrader) updateDailyPnL(realizedPnL float64) {
	at.gridState.mu.Lock()
	at.gridState.DailyPnL += realizedPnL
	at.gridState.TotalProfit += realizedPnL
	at.gridState.mu.Unlock()
}

// emergencyExit closes all positions and cancels all orders
func (at *AutoTrader) emergencyExit(reason string) error {
	gridConfig := at.config.StrategyConfig.GridConfig

	logger.Errorf("[Grid] EMERGENCY EXIT: %s", reason)

	// Cancel all orders
	if err := at.cancelAllGridOrders(); err != nil {
		logger.Errorf("[Grid] Failed to cancel orders in emergency: %v", err)
	}

	// Close all positions
	positions, err := at.trader.GetPositions()
	if err == nil {
		for _, pos := range positions {
			if sym, ok := pos["symbol"].(string); ok && sym == gridConfig.Symbol {
				if size, ok := pos["positionAmt"].(float64); ok && size != 0 {
					if size > 0 {
						at.trader.CloseLong(gridConfig.Symbol, size)
					} else {
						at.trader.CloseShort(gridConfig.Symbol, -size)
					}
				}
			}
		}
	}

	// Pause grid and reset PeakEquity so drawdown check doesn't re-trigger on next cycle
	at.gridState.mu.Lock()
	at.gridState.IsPaused = true
	at.gridState.PeakEquity = 0
	at.gridState.mu.Unlock()

	return nil
}

// handleBreakout handles price breakout from grid range
func (at *AutoTrader) handleBreakout(breakoutType BreakoutType, breakoutPct float64) error {
	logger.Warnf("[Grid] BREAKOUT DETECTED: %s, %.2f%% beyond boundary", breakoutType, breakoutPct)

	// If breakout exceeds 2%, pause grid and cancel orders
	if breakoutPct >= 2.0 {
		logger.Warnf("[Grid] Significant breakout (%.2f%%), pausing grid and canceling orders", breakoutPct)

		// Cancel all pending orders to prevent further losses
		if err := at.cancelAllGridOrders(); err != nil {
			logger.Errorf("[Grid] Failed to cancel orders on breakout: %v", err)
		}

		// Pause grid trading
		at.gridState.mu.Lock()
		at.gridState.IsPaused = true
		at.gridState.mu.Unlock()

		return fmt.Errorf("grid paused due to %s breakout (%.2f%%)", breakoutType, breakoutPct)
	}

	// If breakout is minor (< 2%), consider adjusting grid
	if breakoutPct >= 1.0 {
		logger.Infof("[Grid] Minor breakout (%.2f%%), considering grid adjustment", breakoutPct)
		// Let AI decide whether to adjust
	}

	return nil
}

// checkBoxBreakout checks for multi-period box breakouts and takes appropriate action
func (at *AutoTrader) checkBoxBreakout() error {
	gridConfig := at.config.StrategyConfig.GridConfig
	if gridConfig == nil {
		return nil
	}

	// Get box data
	box, err := market.GetBoxData(gridConfig.Symbol)
	if err != nil {
		logger.Infof("Failed to get box data: %v", err)
		return nil // Non-fatal, continue with other checks
	}

	// Update grid state with box values
	at.gridState.mu.Lock()
	at.gridState.ShortBoxUpper = box.ShortUpper
	at.gridState.ShortBoxLower = box.ShortLower
	at.gridState.MidBoxUpper = box.MidUpper
	at.gridState.MidBoxLower = box.MidLower
	at.gridState.LongBoxUpper = box.LongUpper
	at.gridState.LongBoxLower = box.LongLower
	at.gridState.mu.Unlock()

	// Detect breakout
	breakoutLevel, direction := detectBoxBreakout(box)

	// Get current breakout state
	state := &BreakoutState{
		Level:        market.BreakoutLevel(at.gridState.BreakoutLevel),
		Direction:    at.gridState.BreakoutDirection,
		ConfirmCount: at.gridState.BreakoutConfirmCount,
	}

	// Check if breakout is confirmed (3 candles)
	confirmed := confirmBreakout(state, breakoutLevel, direction)

	// Update grid state
	at.gridState.mu.Lock()
	at.gridState.BreakoutLevel = string(state.Level)
	at.gridState.BreakoutDirection = state.Direction
	at.gridState.BreakoutConfirmCount = state.ConfirmCount
	at.gridState.mu.Unlock()

	if !confirmed {
		return nil
	}

	// Take action based on breakout level
	// Use direction-aware action if enabled
	enableDirectionAdjust := gridConfig.EnableDirectionAdjust
	action := getBreakoutActionWithDirection(breakoutLevel, enableDirectionAdjust)

	// If direction adjustment action, determine the new direction
	if action == BreakoutActionAdjustDirection {
		box, _ := market.GetBoxData(gridConfig.Symbol)
// Get current EMA for slope confirmation
		ctx, err := at.buildGridContext()
		_ = err
		_ = ctx
		newDirection := determineGridDirection(box, at.gridState.CurrentDirection, breakoutLevel, direction)
		return at.executeDirectionAdjustment(newDirection)
	}

	return at.executeBreakoutAction(action)
}

// executeBreakoutAction executes the appropriate action for a breakout
func (at *AutoTrader) executeBreakoutAction(action BreakoutAction) error {
	switch action {
	case BreakoutActionReducePosition:
		// Short box breakout: reduce position to 50%
		logger.Infof("Short box breakout confirmed, reducing position to 50%%")
		at.gridState.mu.Lock()
		at.gridState.PositionReductionPct = 50
		at.gridState.mu.Unlock()
		return nil

	case BreakoutActionPauseGrid:
		// Mid box breakout: pause grid + cancel orders
		logger.Infof("Mid box breakout confirmed, pausing grid and canceling orders")
		at.gridState.mu.Lock()
		at.gridState.IsPaused = true
		at.gridState.mu.Unlock()
		return at.cancelAllGridOrders()

	case BreakoutActionCloseAll:
		// Long box breakout: pause + cancel + close all
		logger.Infof("Long box breakout confirmed, closing all positions")
		at.gridState.mu.Lock()
		at.gridState.IsPaused = true
		at.gridState.mu.Unlock()
		if err := at.cancelAllGridOrders(); err != nil {
			logger.Infof("Failed to cancel orders: %v", err)
		}
		return at.closeAllPositions()

	case BreakoutActionAdjustDirection:
		// Direction adjustment is handled separately via executeDirectionAdjustment
		// This case should not be reached, but handle gracefully
		logger.Infof("Direction adjustment action received via executeBreakoutAction")
		return nil
	}

	return nil
}

// executeDirectionAdjustment handles grid direction changes based on box breakout
func (at *AutoTrader) executeDirectionAdjustment(newDirection market.GridDirection) error {
	at.gridState.mu.RLock()
	oldDirection := at.gridState.CurrentDirection
	at.gridState.mu.RUnlock()

	if oldDirection == newDirection {
		return nil // No change needed
	}

	// Hysteresis cooldown check
	hysteresisMin := at.config.StrategyConfig.GridConfig.DirectionHysteresisMin
	if hysteresisMin <= 0 {
		hysteresisMin = 30
	}
	at.gridState.mu.RLock()
	lastChanged := at.gridState.DirectionChangedAt
	at.gridState.mu.RUnlock()
	if !lastChanged.IsZero() && time.Since(lastChanged) < time.Duration(hysteresisMin)*time.Minute {
		logger.Infof("[Grid] Direction adjustment %s → %s suppressed by hysteresis (%.0f min < %d min cooldown)",
			oldDirection, newDirection, time.Since(lastChanged).Minutes(), hysteresisMin)
		return nil
	}

	logger.Infof("[Grid] Direction adjustment: %s → %s", oldDirection, newDirection)

	// Cancel existing orders before adjusting
	if err := at.cancelAllGridOrders(); err != nil {
		logger.Warnf("[Grid] Failed to cancel orders during direction adjustment: %v", err)
	}

	// Apply the new direction
	return at.adjustGridDirection(newDirection)
}

// closeAllPositions closes all open positions for the grid symbol
func (at *AutoTrader) closeAllPositions() error {
	gridConfig := at.config.StrategyConfig.GridConfig
	if gridConfig == nil {
		return nil
	}

	positions, err := at.trader.GetPositions()
	if err != nil {
		return fmt.Errorf("failed to get positions: %w", err)
	}

	for _, pos := range positions {
		symbol, _ := pos["symbol"].(string)
		if symbol != gridConfig.Symbol {
			continue
		}

		size, _ := pos["positionAmt"].(float64)
		if size == 0 {
			continue
		}

		if size > 0 {
			_, err = at.trader.CloseLong(symbol, size)
		} else {
			_, err = at.trader.CloseShort(symbol, -size)
		}
		if err != nil {
			logger.Infof("Failed to close position: %v", err)
		}
	}

	return nil
}

// checkFalseBreakoutRecovery checks if price has returned to box after breakout
func (at *AutoTrader) checkFalseBreakoutRecovery() error {
	gridConfig := at.config.StrategyConfig.GridConfig
	if gridConfig == nil {
		return nil
	}

	at.gridState.mu.RLock()
	breakoutLevel := at.gridState.BreakoutLevel
	isPaused := at.gridState.IsPaused
	positionReduction := at.gridState.PositionReductionPct
	currentDirection := at.gridState.CurrentDirection
	at.gridState.mu.RUnlock()

	// Only check if we had a breakout or non-neutral direction
	needsRecoveryCheck := breakoutLevel != string(market.BreakoutNone) ||
		positionReduction != 0 ||
		isPaused ||
		(gridConfig.EnableDirectionAdjust && currentDirection != market.GridDirectionNeutral)

	if !needsRecoveryCheck {
		return nil
	}

	// Get current box data
	box, err := market.GetBoxData(gridConfig.Symbol)
	if err != nil {
		return nil
	}

	// Check if price is back inside the long box
	if box.CurrentPrice >= box.LongLower && box.CurrentPrice <= box.LongUpper {
		logger.Infof("Price returned to box, recovering with 50%% position")

		at.gridState.mu.Lock()
		at.gridState.BreakoutLevel = string(market.BreakoutNone)
		at.gridState.BreakoutDirection = ""
		at.gridState.BreakoutConfirmCount = 0
		at.gridState.PositionReductionPct = 50 // Recover at 50%
		at.gridState.IsPaused = false
		at.gridState.mu.Unlock()
	}

	// Check for direction recovery toward neutral (if direction adjustment is enabled)
	if gridConfig.EnableDirectionAdjust && currentDirection != market.GridDirectionNeutral {
		if shouldRecoverDirection(box, currentDirection) {
			newDirection := determineRecoveryDirection(box.CurrentPrice, box, currentDirection)
			if newDirection != currentDirection {
				logger.Infof("[Grid] Direction recovery: %s → %s (price back in short box)",
					currentDirection, newDirection)
				at.adjustGridDirection(newDirection)
			}
		}
	}

	return nil
}

// ============================================================================
// AutoTrader Grid Methods
// ============================================================================

// InitializeGrid initializes the grid state and calculates levels
func (at *AutoTrader) InitializeGrid() error {
	if at.config.StrategyConfig == nil || at.config.StrategyConfig.GridConfig == nil {
		return fmt.Errorf("grid configuration not found")
	}

	gridConfig := at.config.StrategyConfig.GridConfig

	// Use wallet balance (available + margin in positions, excl. unrealized PnL) as total investment
	balance, err := at.trader.GetBalance()
	if err != nil {
		logger.Warnf("[Grid] Failed to get balance for total investment, using config value: %v", err)
	} else {
		walletBal := 0.0
		if w, ok := balance["totalWalletBalance"].(float64); ok {
			walletBal = w
		}
		if walletBal > 0 {
			logger.Infof("[Grid] Using wallet balance as total investment: %.2f USDT (config was: %.2f)", walletBal, gridConfig.TotalInvestment)
			gridConfig.TotalInvestment = walletBal
		}
	}

	at.gridState = NewGridState(gridConfig)

	// Get current market price
	price, err := at.trader.GetMarketPrice(gridConfig.Symbol)
	if err != nil {
		return fmt.Errorf("failed to get market price: %w", err)
	}

	// Calculate grid bounds
	if gridConfig.UseATRBounds {
		// Get ATR for bound calculation
		mktData, err := market.GetWithTimeframes(gridConfig.Symbol, []string{"4h"}, "4h", 20)
		if err != nil {
			logger.Warnf("Failed to get market data for ATR: %v, using default bounds", err)
			at.calculateDefaultBounds(price, gridConfig)
		} else {
			at.calculateATRBounds(price, mktData, gridConfig)
		}
	} else {
		// Use manual bounds
		at.gridState.UpperPrice = gridConfig.UpperPrice
		at.gridState.LowerPrice = gridConfig.LowerPrice
	}

	// Calculate grid spacing
	at.gridState.GridSpacing = (at.gridState.UpperPrice - at.gridState.LowerPrice) / float64(gridConfig.GridCount-1)

	// Initialize grid levels
	at.initializeGridLevels(price, gridConfig)

	at.gridState.IsInitialized = true

	// Restore profit-reduce progress from trade log to prevent re-triggering after restart
	if at.store != nil && gridConfig.EnableProfitReduce {
		for _, side := range []string{"long", "short"} {
			entry, err := at.store.Grid().GetLatestGridTradeLogByAction(at.id, "profit_reduce", side)
			if err == nil && entry != nil {
				// Parse target pct from reason field "target=15% closeAll=false"
				var targetPct float64
				fmt.Sscanf(entry.Reason, "target=%f%%", &targetPct)
				if targetPct > 0 {
					at.gridState.mu.Lock()
					if side == "long" {
						at.gridState.LongProfitReducedPct = targetPct
					} else {
						at.gridState.ShortProfitReducedPct = targetPct
					}
					at.gridState.mu.Unlock()
					logger.Infof("[Grid] Restored %s profit-reduce progress from log: %.0f%%", side, targetPct)
				}
			}
		}
	}

	// Restore T-trade state from trade log on restart
	if at.store != nil && gridConfig.EnableTrappedReduce {
		entry, err := at.store.Grid().GetLatestGridTradeLogByAction(at.id, "ttrade_tag", "")
		if err == nil && entry != nil && entry.OrderID != "" {
			openOrders, oErr := at.trader.GetOpenOrders(gridConfig.Symbol)
			if oErr == nil {
				// Check if the tagged order is still open
				stillOpen := false
				var openSide string
				for _, o := range openOrders {
					if o.OrderID == entry.OrderID {
						stillOpen = true
						openSide = o.Side
						break
					}
				}

				if stillOpen {
					// Order still pending — restore prep state so fill detection continues
					side := "sell"
					if openSide == "BUY" || openSide == "buy" {
						side = "buy"
					}
					at.gridState.mu.Lock()
					at.gridState.TTradePrepOrderID = entry.OrderID
					at.gridState.TTradePrepPrice = entry.Price
					at.gridState.TTradePrepQty = entry.Quantity
					at.gridState.TTradePrepPlacedAt = entry.CreatedAt
					at.gridState.TTradePendingReduceQty = entry.Quantity
					at.gridState.TTradePrepSide = side
					at.gridState.TTradePrepExecuted = false
					at.gridState.mu.Unlock()
					logger.Infof("[Grid] Restored T-trade tag from log: order %s @ %.4f (still open)", entry.OrderID, entry.Price)
				} else {
					// Order no longer open — check if it filled while we were down
					statusMap, sErr := at.trader.GetOrderStatus(gridConfig.Symbol, entry.OrderID)
					if sErr == nil {
						statusStr, _ := statusMap["status"].(string)
						if statusStr == "FILLED" {
							fillPrice := entry.Price
							if avg, ok := statusMap["avgPrice"].(float64); ok && avg > 0 {
								fillPrice = avg
							}
							at.gridState.mu.Lock()
							at.gridState.TTradeReadyToReduce = true
							at.gridState.TTradeReadyReduceQty = entry.Quantity
							at.gridState.TTradeReadyPrepPrice = fillPrice
							at.gridState.mu.Unlock()
							logger.Infof("[Grid] Restored T-trade ready_to_reduce from log: order %s filled @ %.4f (detected on restart)", entry.OrderID, fillPrice)
						}
					}
				}
			}
		}
	}

	// CRITICAL: Set leverage on exchange before trading
	if err := at.trader.SetLeverage(gridConfig.Symbol, gridConfig.Leverage); err != nil {
		logger.Warnf("[Grid] Failed to set leverage %dx on exchange: %v", gridConfig.Leverage, err)
		// Not fatal - continue with default leverage
	} else {
		logger.Infof("[Grid] Leverage set to %dx for %s", gridConfig.Leverage, gridConfig.Symbol)
	}

	logger.Infof("📊 [Grid] Initialized: %d levels, $%.2f - $%.2f, spacing $%.2f",
		gridConfig.GridCount, at.gridState.LowerPrice, at.gridState.UpperPrice, at.gridState.GridSpacing)

	return nil
}

// calculateDefaultBounds calculates default bounds based on price
func (at *AutoTrader) calculateDefaultBounds(price float64, config *store.GridStrategyConfig) {
	// Default: ±3% from current price
	multiplier := 0.03 * float64(config.GridCount) / 10
	at.gridState.UpperPrice = price * (1 + multiplier)
	at.gridState.LowerPrice = price * (1 - multiplier)
}

// calculateATRBounds calculates bounds using ATR
func (at *AutoTrader) calculateATRBounds(price float64, mktData *market.Data, config *store.GridStrategyConfig) {
	atr := 0.0
	if mktData.LongerTermContext != nil {
		atr = mktData.LongerTermContext.ATR14
	}

	if atr <= 0 {
		at.calculateDefaultBounds(price, config)
		return
	}

	multiplier := config.ATRMultiplier
	if multiplier <= 0 {
		multiplier = 2.0
	}

	halfRange := atr * multiplier
	at.gridState.UpperPrice = price + halfRange
	at.gridState.LowerPrice = price - halfRange
}

// initializeGridLevels creates the grid level structure
func (at *AutoTrader) initializeGridLevels(currentPrice float64, config *store.GridStrategyConfig) {
	levels := make([]kernel.GridLevelInfo, config.GridCount)
	totalWeight := 0.0
	weights := make([]float64, config.GridCount)

	// Calculate weights based on distribution
	for i := 0; i < config.GridCount; i++ {
		switch config.Distribution {
		case "gaussian":
			// Gaussian distribution - more weight in the middle
			center := float64(config.GridCount-1) / 2
			sigma := float64(config.GridCount) / 4
			weights[i] = math.Exp(-math.Pow(float64(i)-center, 2) / (2 * sigma * sigma))
		case "pyramid":
			// Pyramid - more weight at bottom
			weights[i] = float64(config.GridCount - i)
		default: // uniform
			weights[i] = 1.0
		}
		totalWeight += weights[i]
	}

	// Create levels
	for i := 0; i < config.GridCount; i++ {
		price := at.gridState.LowerPrice + float64(i)*at.gridState.GridSpacing
		allocatedUSD := config.TotalInvestment * weights[i] / totalWeight

		// Determine initial side (below current price = buy, above = sell)
		side := "buy"
		if price > currentPrice {
			side = "sell"
		}

		levels[i] = kernel.GridLevelInfo{
			Index:        i,
			Price:        price,
			State:        "empty",
			Side:         side,
			AllocatedUSD: allocatedUSD,
		}
	}

	at.gridState.Levels = levels

	// Apply direction-based side assignment if enabled
	if config.EnableDirectionAdjust {
		at.applyGridDirection(currentPrice)
	}
}

// applyGridDirection adjusts grid level sides based on the current direction
// This redistributes buy/sell levels according to the direction bias ratio
func (at *AutoTrader) applyGridDirection(currentPrice float64) {
	config := at.gridState.Config
	direction := at.gridState.CurrentDirection

	// Get bias ratio from config, default to 0.7 (70%/30%)
	biasRatio := config.DirectionBiasRatio
	if biasRatio <= 0 || biasRatio > 1 {
		biasRatio = 0.7
	}

	buyRatio, _ := direction.GetBuySellRatio(biasRatio)

	// Calculate how many levels should be buy vs sell based on direction
	totalLevels := len(at.gridState.Levels)
	targetBuyLevels := int(float64(totalLevels) * buyRatio)

	// For neutral: use price-based assignment (buy below, sell above)
	if direction == market.GridDirectionNeutral {
		for i := range at.gridState.Levels {
			if at.gridState.Levels[i].Price <= currentPrice {
				at.gridState.Levels[i].Side = "buy"
			} else {
				at.gridState.Levels[i].Side = "sell"
			}
		}
		return
	}

	// For long/long_bias: more buy levels
	// For short/short_bias: more sell levels
	switch direction {
	case market.GridDirectionLong:
		// 100% buy - all levels are buy
		for i := range at.gridState.Levels {
			at.gridState.Levels[i].Side = "buy"
		}

	case market.GridDirectionShort:
		// 100% sell - all levels are sell
		for i := range at.gridState.Levels {
			at.gridState.Levels[i].Side = "sell"
		}

	case market.GridDirectionLongBias, market.GridDirectionShortBias:
		// Assign sides based on position relative to current price
		// For long_bias: keep all below as buy, convert some above to buy
		// For short_bias: keep all above as sell, convert some below to sell
		buyCount := 0
		sellCount := 0

		for i := range at.gridState.Levels {
			needMoreBuys := buyCount < targetBuyLevels
			needMoreSells := sellCount < (totalLevels - targetBuyLevels)

			if at.gridState.Levels[i].Price <= currentPrice {
				// Level below or at current price
				if needMoreBuys {
					at.gridState.Levels[i].Side = "buy"
					buyCount++
				} else {
					at.gridState.Levels[i].Side = "sell"
					sellCount++
				}
			} else {
				// Level above current price
				if needMoreSells && direction == market.GridDirectionShortBias {
					at.gridState.Levels[i].Side = "sell"
					sellCount++
				} else if needMoreBuys && direction == market.GridDirectionLongBias {
					at.gridState.Levels[i].Side = "buy"
					buyCount++
				} else if needMoreSells {
					at.gridState.Levels[i].Side = "sell"
					sellCount++
				} else {
					at.gridState.Levels[i].Side = "buy"
					buyCount++
				}
			}
		}
	}

	logger.Infof("[Grid] Applied direction %s: buy_ratio=%.0f%%, levels reconfigured",
		direction, buyRatio*100)
}

// adjustGridDirection handles runtime direction adjustment when breakout is detected
func (at *AutoTrader) adjustGridDirection(newDirection market.GridDirection) error {
	at.gridState.mu.Lock()
	defer at.gridState.mu.Unlock()

	oldDirection := at.gridState.CurrentDirection
	if oldDirection == newDirection {
		return nil // No change needed
	}

	// Hysteresis cooldown: prevent rapid direction oscillation
	hysteresisMin := at.config.StrategyConfig.GridConfig.DirectionHysteresisMin
	if hysteresisMin <= 0 {
		hysteresisMin = 30
	}
	if !at.gridState.DirectionChangedAt.IsZero() &&
		time.Since(at.gridState.DirectionChangedAt) < time.Duration(hysteresisMin)*time.Minute {
		logger.Infof("[Grid] Direction change %s → %s suppressed by hysteresis (%.0f min < %d min cooldown)",
			oldDirection, newDirection, time.Since(at.gridState.DirectionChangedAt).Minutes(), hysteresisMin)
		return nil
	}

	at.gridState.CurrentDirection = newDirection
	at.gridState.DirectionChangedAt = time.Now()
	at.gridState.DirectionChangeCount++

	logger.Infof("[Grid] Direction changed: %s → %s (change count: %d)",
		oldDirection, newDirection, at.gridState.DirectionChangeCount)

	// Get current price for recalculation
	currentPrice, err := at.trader.GetMarketPrice(at.gridState.Config.Symbol)
	if err != nil {
		return fmt.Errorf("failed to get market price: %w", err)
	}

	// Reapply direction to grid levels
	at.applyGridDirection(currentPrice)

	return nil
}

// RunGridCycle executes one grid trading cycle
func (at *AutoTrader) RunGridCycle() error {
	// Check if trader is stopped (early exit to prevent trades after Stop() is called)
	at.isRunningMutex.RLock()
	running := at.isRunning
	at.isRunningMutex.RUnlock()
	if !running {
		logger.Infof("[Grid] Trader is stopped, aborting grid cycle")
		return nil
	}

	if at.gridState == nil || !at.gridState.IsInitialized {
		if err := at.InitializeGrid(); err != nil {
			return fmt.Errorf("failed to initialize grid: %w", err)
		}
	}

	// CRITICAL: Check for breakout before executing any trades
	breakoutType, breakoutPct := at.checkBreakout()
	if breakoutType != BreakoutNone {
		if err := at.handleBreakout(breakoutType, breakoutPct); err != nil {
			return err // Grid paused due to breakout
		}
	}

	// Risk controls (max drawdown, daily loss limit, stop loss) are disabled.

	// Check multi-period box breakout
	if err := at.checkBoxBreakout(); err != nil {
		logger.Infof("Box breakout check error: %v", err)
	}

	// Check for false breakout recovery
	if err := at.checkFalseBreakoutRecovery(); err != nil {
		logger.Infof("False breakout recovery check error: %v", err)
	}

	// Check if grid is paused
	at.gridState.mu.RLock()
	isPaused := at.gridState.IsPaused
	at.gridState.mu.RUnlock()
	if isPaused {
		logger.Infof("[Grid] Grid is paused, skipping cycle")
		return nil
	}

	gridConfig := at.config.StrategyConfig.GridConfig
	lang := at.config.StrategyConfig.Language
	if lang == "" {
		lang = "en"
	}

	// Fetch open orders once per cycle — shared by T-trade checks, state sync, and AI context
	openOrders, err := at.trader.GetOpenOrders(gridConfig.Symbol)
	if err != nil {
		logger.Warnf("[Grid] Failed to get open orders: %v", err)
		openOrders = nil
	}

	// Sync open orders from exchange FIRST so level states are up-to-date
	// before T-trade fill detection runs
	at.syncOpenOrdersFromExchange(openOrders)

	// Check if T-trade buy order has filled → execute deferred reduce if so
	if gridConfig.EnableTrappedReduce {
		at.autoTagTTradeFromExistingOrders(openOrders) // auto-tag nearest grid order as T-trade prep
		at.checkTTradeOrderFillAndReduce(openOrders)
		at.checkTTradeReduceOrderStatus(openOrders)
	}

	// Check profit-based position reduction
	if at.config.StrategyConfig.GridConfig.EnableProfitReduce {
		at.checkProfitReduce()
	}

	// Build grid context
	gridCtx, err := at.buildGridContext()
	if err != nil {
		return fmt.Errorf("failed to build grid context: %w", err)
	}

	// Get AI decisions
	decision, err := kernel.GetGridDecisions(gridCtx, at.mcpClient, at.config.StrategyConfig, lang)
	if err != nil {
		at.gridState.mu.Lock()
		at.gridState.DecisionMemory = append(at.gridState.DecisionMemory, kernel.DecisionSummary{
			Timestamp: time.Now().Format("15:04:05"),
			Action:    "timeout",
			Reasoning: fmt.Sprintf("AI call timed out: %v", err),
		})
		if len(at.gridState.DecisionMemory) > 5 {
			at.gridState.DecisionMemory = at.gridState.DecisionMemory[len(at.gridState.DecisionMemory)-5:]
		}
		at.gridState.mu.Unlock()
		return fmt.Errorf("failed to get grid decisions: %w", err)
	}

	// Check if trader is stopped before executing any decisions (prevent trades after Stop())
	at.isRunningMutex.RLock()
	running = at.isRunning
	at.isRunningMutex.RUnlock()
	if !running {
		logger.Infof("[Grid] Trader stopped before decision execution, aborting grid cycle")
		return nil
	}

	// Execute decisions
	type decisionResult struct {
		d   kernel.Decision
		err error
	}
	results := make([]decisionResult, 0, len(decision.Decisions))
	for _, d := range decision.Decisions {
		// Check if trader is still running before each decision
		at.isRunningMutex.RLock()
		running := at.isRunning
		at.isRunningMutex.RUnlock()
		if !running {
			logger.Infof("[Grid] Trader stopped, skipping remaining %d decisions", len(decision.Decisions))
			break
		}

		err := at.executeGridDecision(&d, gridCtx)
		if err != nil {
			logger.Warnf("[Grid] Failed to execute decision %s: %v", d.Action, err)
		}
		results = append(results, decisionResult{d: d, err: err})
	}

	// Update decision memory
	at.gridState.mu.Lock()
	for _, r := range results {
		if r.d.Action == "hold" {
			continue
		}
		resultStr := "ok"
		if r.err != nil {
			resultStr = "failed: " + r.err.Error()
		}
		summary := kernel.DecisionSummary{
			Timestamp: time.Now().Format("15:04:05"),
			Action:    r.d.Action,
			Reasoning: r.d.Reasoning,
			Price:     r.d.Price,
			Result:    resultStr,
		}
		at.gridState.DecisionMemory = append(at.gridState.DecisionMemory, summary)
	}
	// Keep only the last 5 decisions
	if len(at.gridState.DecisionMemory) > 5 {
		at.gridState.DecisionMemory = at.gridState.DecisionMemory[len(at.gridState.DecisionMemory)-5:]
	}
	at.gridState.mu.Unlock()

	// Sync state with exchange
	at.syncGridState()

	// After AI places new orders, re-fetch open orders and re-run T-trade tagging
	// so the next cycle doesn't miss a taggable order placed this cycle.
	// Only re-tag if T-trade is fully idle (no reduce order placed this cycle).
	if gridConfig.EnableTrappedReduce {
		hasNewOrder := false
		hasReduceOrder := false
		for _, r := range results {
			if r.err == nil && (r.d.Action == "place_buy_limit" || r.d.Action == "place_sell_limit") {
				hasNewOrder = true
			}
			if r.err == nil && (r.d.Action == "reduce_long" || r.d.Action == "reduce_short") {
				hasReduceOrder = true
			}
		}
		if hasNewOrder && !hasReduceOrder {
			at.gridState.mu.RLock()
			reduceOrderPending := at.gridState.TTradeReduceOrderID != ""
			readyToReduce := at.gridState.TTradeReadyToReduce
			at.gridState.mu.RUnlock()
			if !reduceOrderPending && !readyToReduce {
				freshOrders, err := at.trader.GetOpenOrders(gridConfig.Symbol)
				if err == nil {
					at.syncOpenOrdersFromExchange(freshOrders)
					at.autoTagTTradeFromExistingOrders(freshOrders)
				}
			}
		}
	}

	// Save decision record
	at.saveGridDecisionRecord(decision)

	return nil
}

// checkProfitReduce checks per-side unrealized profit and reduces position accordingly:
// - Every ProfitReduceStepPct increment → reduce that % of current position
// - If profit > step*1.2 AND position value < 100 USD → close entire side
func (at *AutoTrader) checkProfitReduce() {
	gridConfig := at.config.StrategyConfig.GridConfig
	symbol := gridConfig.Symbol
	step := gridConfig.ProfitReduceStepPct
	if step <= 0 {
		step = 10.0
	}

	positions, err := at.trader.GetPositions()
	if err != nil {
		return
	}

	type sideInfo struct {
		size            float64
		entryPrice      float64
		markPrice       float64
		unrealizedProfit float64
		side            string // "long" or "short"
	}

	sides := map[string]*sideInfo{}
	for _, pos := range positions {
		sym, _ := pos["symbol"].(string)
		if sym != symbol {
			continue
		}
		posSide, _ := pos["side"].(string)
		if posSide != "long" && posSide != "short" {
			continue
		}
		rawSize, _ := pos["positionAmt"].(float64)
		size := math.Abs(rawSize)
		if size == 0 {
			continue
		}
		entry, _ := pos["entryPrice"].(float64)
		mark, _ := pos["markPrice"].(float64)
		if mark == 0 {
			mark = entry
		}
		upl, _ := pos["unRealizedProfit"].(float64)
		sides[posSide] = &sideInfo{size: size, entryPrice: entry, markPrice: mark, unrealizedProfit: upl, side: posSide}
	}

	gridTrader, ok := at.trader.(GridTrader)
	if !ok {
		gridTrader = NewGridTraderAdapter(at.trader)
	}

	// Compute what actions to take (read lock only)
	type reduceAction struct {
		info            sideInfo
		qty             float64
		closeAll        bool
		targetReducePct float64
	}
	var actions []reduceAction

	at.gridState.mu.RLock()
	for _, info := range sides {
		if info.entryPrice == 0 || info.markPrice == 0 {
			continue
		}
		// Margin-based profit: unrealizedProfit / (positionValue / leverage)
		margin := info.size * info.entryPrice / float64(gridConfig.Leverage)
		if margin == 0 {
			continue
		}
		profitPct := info.unrealizedProfit / margin * 100
		logger.Infof("[Grid] Profit-reduce check: %s entry=%.4f mark=%.4f upl=%.2f margin=%.2f profit=%.2f%%",
			info.side, info.entryPrice, info.markPrice, info.unrealizedProfit, margin, profitPct)

		if profitPct <= 0 {
			actions = append(actions, reduceAction{info: *info, qty: 0, closeAll: false, targetReducePct: -1})
			continue
		}

		positionValue := info.size * info.markPrice
		if profitPct > step*1.2 && positionValue < 100 {
			actions = append(actions, reduceAction{info: *info, qty: info.size, closeAll: true})
			continue
		}

		alreadyReduced := at.gridState.LongProfitReducedPct
		if info.side == "short" {
			alreadyReduced = at.gridState.ShortProfitReducedPct
		}
		targetReducePct := math.Floor(profitPct/step) * step
		if targetReducePct <= alreadyReduced {
			continue
		}
		// Escalating reduce based on current position size at each step
		// Step N×: reduce N×step% of remaining position
		var reduceQty float64
		remaining := info.size
		for s := alreadyReduced + step; s <= targetReducePct; s += step {
			stepPct := (s / step) * (step / 100) // 1×step→step%, 2×step→2×step%...
			reduceQty += remaining * stepPct
			remaining -= remaining * stepPct
		}
		if reduceQty > info.size {
			reduceQty = info.size
		}
		if reduceQty > 0 {
			actions = append(actions, reduceAction{info: *info, qty: reduceQty, targetReducePct: targetReducePct})
		}
	}
	at.gridState.mu.RUnlock()

	// Execute orders outside the lock
	for _, a := range actions {
		info := a.info
		if a.targetReducePct == -1 {
			// Reset tracker
			at.gridState.mu.Lock()
			if info.side == "long" {
				at.gridState.LongProfitReducedPct = 0
			} else {
				at.gridState.ShortProfitReducedPct = 0
			}
			at.gridState.mu.Unlock()
			continue
		}

		orderSide := "SELL"
		posSide := "LONG"
		if info.side == "short" {
			orderSide = "BUY"
			posSide = "SHORT"
		}

		if a.closeAll {
			logger.Infof("[Grid] Profit-reduce: closing entire %s position (value=$%.2f)", info.side, info.size*info.markPrice)
		} else {
			logger.Infof("[Grid] Profit-reduce: %s reducing %.4f (target=%.0f%%)", info.side, a.qty, a.targetReducePct)
		}

		result, err := gridTrader.PlaceLimitOrder(&LimitOrderRequest{
			Symbol:       symbol,
			Side:         orderSide,
			PositionSide: posSide,
			Price:        info.markPrice,
			Quantity:     a.qty,
			Leverage:     gridConfig.Leverage,
			ReduceOnly:   true,
		})
		orderID := ""
		if result != nil {
			orderID = result.OrderID
		}
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		action := "profit_reduce"
		if a.closeAll {
			action = "profit_reduce_close"
		}
		margin := info.size * info.entryPrice / float64(gridConfig.Leverage)
		profitPct := 0.0
		if margin > 0 {
			profitPct = info.unrealizedProfit / margin * 100
		}
		at.logGridTrade("profit_reduce", action, info.side, symbol,
			fmt.Sprintf("target=%.0f%% closeAll=%v", a.targetReducePct, a.closeAll),
			orderID, a.qty, info.markPrice, info.entryPrice, info.markPrice,
			profitPct, info.unrealizedProfit, err == nil, errMsg)
		if err != nil {
			logger.Warnf("[Grid] Profit-reduce %s failed: %v", info.side, err)
			continue
		}

		at.gridState.mu.Lock()
		if a.closeAll {
			if info.side == "long" {
				at.gridState.LongProfitReducedPct = 0
			} else {
				at.gridState.ShortProfitReducedPct = 0
			}
		} else {
			if info.side == "long" {
				at.gridState.LongProfitReducedPct = a.targetReducePct
			} else {
				at.gridState.ShortProfitReducedPct = a.targetReducePct
			}
		}
		at.gridState.mu.Unlock()
	}
}

// buildGridContext builds the context for AI grid decisions
func (at *AutoTrader) buildGridContext() (*kernel.GridContext, error) {
	gridConfig := at.config.StrategyConfig.GridConfig

	// Get market data
	mktData, err := market.GetWithTimeframes(gridConfig.Symbol, []string{"5m", "4h"}, "5m", 50)
	if err != nil {
		return nil, fmt.Errorf("failed to get market data: %w", err)
	}

	// Build base context from market data
	ctx := kernel.BuildGridContextFromMarketData(mktData, gridConfig)

	// Add grid state (single lock for the entire block)
	at.gridState.mu.RLock()
	ctx.Levels = append([]kernel.GridLevelInfo{}, at.gridState.Levels...)
	ctx.UpperPrice = at.gridState.UpperPrice
	ctx.LowerPrice = at.gridState.LowerPrice
	ctx.GridSpacing = at.gridState.GridSpacing
	ctx.IsPaused = at.gridState.IsPaused
	ctx.TotalProfit = at.gridState.TotalProfit
	ctx.TotalTrades = at.gridState.TotalTrades
	ctx.WinningTrades = at.gridState.WinningTrades
	ctx.MaxDrawdown = at.gridState.MaxDrawdown
	ctx.DailyPnL = at.gridState.DailyPnL
	ctx.DecisionHistory = append([]kernel.DecisionSummary{}, at.gridState.DecisionMemory...)

	// Count active orders and filled levels
	for _, level := range at.gridState.Levels {
		if level.State == "pending" {
			ctx.ActiveOrderCount++
		} else if level.State == "filled" {
			ctx.FilledLevelCount++
		}
	}
	at.gridState.mu.RUnlock()

	// Populate distance-to-price for each level so AI can see proximity without calculating
	if ctx.CurrentPrice > 0 {
		for i := range ctx.Levels {
			ctx.Levels[i].DistancePct = (ctx.Levels[i].Price - ctx.CurrentPrice) / ctx.CurrentPrice * 100
		}
	}

	// Get account info
	balance, err := at.trader.GetBalance()
	if err == nil {
		if equity, ok := balance["totalEquity"].(float64); ok {
			ctx.TotalEquity = equity
		}
		if walletBal, ok := balance["totalWalletBalance"].(float64); ok {
			ctx.WalletBalance = walletBal
		}
		if available, ok := balance["availableBalance"].(float64); ok {
			ctx.AvailableBalance = available
		}
		if unrealized, ok := balance["totalUnrealizedProfit"].(float64); ok {
			ctx.UnrealizedPnL = unrealized
		}
	}

	// Get current position
	positions, err := at.trader.GetPositions()
	if err == nil {
		for _, pos := range positions {
			if sym, ok := pos["symbol"].(string); ok && sym == gridConfig.Symbol {
				if size, ok := pos["positionAmt"].(float64); ok {
					ctx.CurrentPosition += size
					if side, ok := pos["side"].(string); ok {
						if side == "long" {
							ctx.LongPosition += size
						} else if side == "short" {
							ctx.ShortPosition += math.Abs(size)
						}
					}
				}
			}
		}
	}

	// Build trapped position info — always populate so AI sees position/loss context.
	// T-trade auto-execution only fires when EnableTrappedReduce is true.
	if ctx.CurrentPrice > 0 {
		ctx.TrappedInfo = at.buildTrappedPositionInfo(ctx.CurrentPrice)
	}

	return ctx, nil
}

// executeGridDecision executes a single grid decision
func (at *AutoTrader) executeGridDecision(d *kernel.Decision, ctx *kernel.GridContext) error {
	logger.Infof("[Grid] AI action: %s | qty=%.4f price=%.2f | reason: %s",
		d.Action, d.Quantity, d.Price, d.Reasoning)
	symbol := at.config.StrategyConfig.GridConfig.Symbol
	at.logGridTrade("ai", d.Action, "", symbol, d.Reasoning, "",
		d.Quantity, d.Price, 0, 0, 0, 0, true, "")
	switch d.Action {
	case "place_buy_limit":
		if err := at.placeGridLimitOrder(d, "BUY"); err != nil {
			return err
		}
		return nil
	case "place_sell_limit":
		if err := at.placeGridLimitOrder(d, "SELL"); err != nil {
			return err
		}
		return nil
	case "cancel_order":
		return at.cancelGridOrder(d)
	case "cancel_all_orders":
		return at.cancelAllGridOrders()
	case "pause_grid":
		return at.pauseGrid(d.Reasoning)
	case "resume_grid":
		return at.resumeGrid()
	case "adjust_grid":
		return at.adjustGrid(d)
	case "hold":
		logger.Infof("[Grid] Holding current state: %s", d.Reasoning)
		return nil
	// Support standard actions for closing positions
	case "close_long":
		_, err := at.trader.CloseLong(d.Symbol, d.Quantity)
		if err == nil {
			at.refreshTotalInvestment()
		}
		return err
	case "close_short":
		_, err := at.trader.CloseShort(d.Symbol, d.Quantity)
		if err == nil {
			at.refreshTotalInvestment()
		}
		return err
	case "reduce_long":
		// Block if a reduce order is already pending (waiting to fill or cancel)
		at.gridState.mu.RLock()
		pendingReduceID := at.gridState.TTradeReduceOrderID
		tTradeIdle := !at.gridState.TTradeReadyToReduce && at.gridState.TTradePrepOrderID == ""
		at.gridState.mu.RUnlock()
		if pendingReduceID != "" {
			logger.Infof("[Grid] reduce_long skipped: reduce order %s already pending", pendingReduceID)
			return nil
		}

		// Block AI-initiated reduce when T-trade is idle — only allow when T-trade is active
		if tTradeIdle {
			logger.Infof("[Grid] reduce_long skipped: T-trade is idle, AI-initiated reduce is disabled")
			return nil
		}

		// Override quantity with the T-trade stored qty — AI must not decide the amount
		at.gridState.mu.RLock()
		tTradeQty := at.gridState.TTradeReadyReduceQty
		tTradePrepPriceLong := at.gridState.TTradeReadyPrepPrice
		at.gridState.mu.RUnlock()
		if tTradeQty > 0 && tTradeQty != d.Quantity {
			logger.Infof("[Grid] reduce_long qty overridden by T-trade: %.4f → %.4f", d.Quantity, tTradeQty)
			d.Quantity = tTradeQty
		}

		// Enforce minimum spread: reduce_long sell price must be at least spreadPct% above prep fill price
		if tTradePrepPriceLong > 0 {
			spreadPctLong := at.config.StrategyConfig.GridConfig.TTradeSpreadPct
			if spreadPctLong < 0.2 {
				spreadPctLong = 0.2
			}
			minPrice := tTradePrepPriceLong * (1 + spreadPctLong/100)
			if d.Price < minPrice {
				logger.Infof("[Grid] reduce_long price enforced: %.4f → %.4f (min %.1f%% above prep %.4f)", d.Price, minPrice, spreadPctLong, tTradePrepPriceLong)
				d.Price = minPrice
			}
		}

		// Close long position with sell limit order
		logger.Infof("[Grid] AI decision: reduce_long qty=%.4f price=%.2f reason=%s", d.Quantity, d.Price, d.Reasoning)
		if gridTrader, ok := at.trader.(GridTrader); ok {
			result, err := gridTrader.PlaceLimitOrder(&types.LimitOrderRequest{
				Symbol:       d.Symbol,
				Side:         "sell",
				PositionSide: "LONG",
				Quantity:     d.Quantity,
				Price:        d.Price,
				Leverage:     at.config.StrategyConfig.GridConfig.Leverage,
				ReduceOnly:   true,
			})
			if err == nil {
				at.gridState.mu.Lock()
				at.gridState.LastTrappedReduceAt = time.Now()
				// Track reduce order; keep TTradeReadyToReduce=true until fill confirmed
				if result != nil {
					at.gridState.TTradeReduceOrderID = result.OrderID
					at.gridState.TTradeReducePlacedAt = time.Now()
					at.gridState.TTradeReduceQty = d.Quantity
					at.gridState.TTradeReducePrice = d.Price
					at.gridState.TTradeReduceSide = "sell"
				}
				at.gridState.mu.Unlock()
			}
			return err
		}
		return fmt.Errorf("trader does not support limit orders")
	case "reduce_short":
		// Block if a reduce order is already pending (waiting to fill or cancel)
		at.gridState.mu.RLock()
		pendingReduceID2 := at.gridState.TTradeReduceOrderID
		tTradeIdle2 := !at.gridState.TTradeReadyToReduce && at.gridState.TTradePrepOrderID == ""
		at.gridState.mu.RUnlock()
		if pendingReduceID2 != "" {
			logger.Infof("[Grid] reduce_short skipped: reduce order %s already pending", pendingReduceID2)
			return nil
		}

		// Block AI-initiated reduce when T-trade is idle — only allow when T-trade is active
		if tTradeIdle2 {
			logger.Infof("[Grid] reduce_short skipped: T-trade is idle, AI-initiated reduce is disabled")
			return nil
		}

		// Override quantity with the T-trade stored qty — AI must not decide the amount
		at.gridState.mu.RLock()
		tTradeQty2 := at.gridState.TTradeReadyReduceQty
		tTradePrepPriceShort := at.gridState.TTradeReadyPrepPrice
		at.gridState.mu.RUnlock()
		if tTradeQty2 > 0 && tTradeQty2 != d.Quantity {
			logger.Infof("[Grid] reduce_short qty overridden by T-trade: %.4f → %.4f", d.Quantity, tTradeQty2)
			d.Quantity = tTradeQty2
		}

		// Enforce minimum spread: reduce_short buy price must be at least spreadPct% below prep fill price
		if tTradePrepPriceShort > 0 {
			spreadPctShort := at.config.StrategyConfig.GridConfig.TTradeSpreadPct
			if spreadPctShort < 0.2 {
				spreadPctShort = 0.2
			}
			maxPrice := tTradePrepPriceShort * (1 - spreadPctShort/100)
			if d.Price > maxPrice {
				logger.Infof("[Grid] reduce_short price enforced: %.4f → %.4f (max %.1f%% below prep %.4f)", d.Price, maxPrice, spreadPctShort, tTradePrepPriceShort)
				d.Price = maxPrice
			}
		}

		// Close short position with buy limit order
		logger.Infof("[Grid] AI decision: reduce_short qty=%.4f price=%.2f reason=%s", d.Quantity, d.Price, d.Reasoning)
		if gridTrader, ok := at.trader.(GridTrader); ok {
			result, err := gridTrader.PlaceLimitOrder(&types.LimitOrderRequest{
				Symbol:       d.Symbol,
				Side:         "buy",
				PositionSide: "SHORT",
				Quantity:     d.Quantity,
				Price:        d.Price,
				Leverage:     at.config.StrategyConfig.GridConfig.Leverage,
				ReduceOnly:   true,
			})
			if err == nil {
				at.gridState.mu.Lock()
				at.gridState.LastTrappedReduceAt = time.Now()
				// Track reduce order; keep TTradeReadyToReduce=true until fill confirmed
				if result != nil {
					at.gridState.TTradeReduceOrderID = result.OrderID
					at.gridState.TTradeReducePlacedAt = time.Now()
					at.gridState.TTradeReduceQty = d.Quantity
					at.gridState.TTradeReducePrice = d.Price
					at.gridState.TTradeReduceSide = "buy"
				}
				at.gridState.mu.Unlock()
			}
			return err
		}
		return fmt.Errorf("trader does not support limit orders")
	default:
		logger.Warnf("[Grid] Unknown action: %s", d.Action)
		return nil
	}
}

// refreshTotalInvestment updates TotalInvestment from wallet balance after a position is closed
func (at *AutoTrader) refreshTotalInvestment() {
	gridConfig := at.config.StrategyConfig.GridConfig
	bal, err := at.trader.GetBalance()
	if err != nil {
		logger.Warnf("[Grid] Failed to refresh total investment: %v", err)
		return
	}
	if walletBal, ok := bal["totalWalletBalance"].(float64); ok && walletBal > 0 {
		old := gridConfig.TotalInvestment
		gridConfig.TotalInvestment = walletBal
		logger.Infof("[Grid] Refreshed total investment after close: %.2f -> %.2f USDT", old, walletBal)
	}
}

// checkTotalPositionLimit checks if adding a new position would exceed total limits.
// side: "BUY" checks long position, "SELL" checks short position independently.
// Returns: (allowed bool, currentPositionValue float64, maxAllowed float64)
func (at *AutoTrader) checkTotalPositionLimit(symbol string, side string, additionalValue float64) (bool, float64, float64) {
	gridConfig := at.config.StrategyConfig.GridConfig

	// Each side (long/short) independently gets up to TotalInvestment × Leverage × MaxPositionSizePct%
	maxPositionSizePct := gridConfig.MaxPositionSizePct
	if maxPositionSizePct <= 0 {
		maxPositionSizePct = 100.0
	}
	maxSidePositionValue := gridConfig.TotalInvestment * float64(gridConfig.Leverage) * maxPositionSizePct / 100

	// Sum position value for the relevant side only
	currentPositionValue := 0.0
	positions, err := at.trader.GetPositions()
	if err == nil {
		for _, pos := range positions {
			sym, _ := pos["symbol"].(string)
			if sym != symbol {
				continue
			}
			posSide, _ := pos["side"].(string)
			posSize, _ := pos["positionAmt"].(float64)
			// Match side: BUY order adds to long, SELL order adds to short
			if (side == "BUY" && posSide == "long") || (side == "SELL" && posSide == "short") {
				markPrice, hasMark := pos["markPrice"].(float64)
				entryPrice, _ := pos["entryPrice"].(float64)
				price := markPrice
				if !hasMark || price == 0 {
					price = entryPrice
				}
				currentPositionValue += math.Abs(posSize) * price
			}
		}
	}

	totalAfterOrder := currentPositionValue + additionalValue
	allowed := totalAfterOrder <= maxSidePositionValue

	return allowed, currentPositionValue, maxSidePositionValue
}

// placeGridLimitOrder places a limit order for grid trading
func (at *AutoTrader) placeGridLimitOrder(d *kernel.Decision, side string) error {
	// Check if trader supports GridTrader interface
	gridTrader, ok := at.trader.(GridTrader)
	if !ok {
		// Fallback to adapter
		gridTrader = NewGridTraderAdapter(at.trader)
	}

	gridConfig := at.config.StrategyConfig.GridConfig

	// CRITICAL: Validate and cap quantity to prevent excessive position sizes
	// This protects against AI miscalculations or leverage misconfigurations
	quantity := d.Quantity
	if d.Price > 0 && gridConfig.TotalInvestment > 0 {
		// Calculate max allowed position value per grid level
		// Each level gets proportional share of total investment
		maxMarginPerLevel := gridConfig.TotalInvestment / float64(gridConfig.GridCount)
		maxPositionValuePerLevel := maxMarginPerLevel * float64(gridConfig.Leverage)
		maxQuantityPerLevel := maxPositionValuePerLevel / d.Price

		// Also get the level's allocated USD for additional validation
		at.gridState.mu.RLock()
		var levelAllocatedUSD float64
		if d.LevelIndex >= 0 && d.LevelIndex < len(at.gridState.Levels) {
			levelAllocatedUSD = at.gridState.Levels[d.LevelIndex].AllocatedUSD
		}
		at.gridState.mu.RUnlock()

		// Use level-specific allocation if available
		if levelAllocatedUSD > 0 {
			levelMaxPositionValue := levelAllocatedUSD * float64(gridConfig.Leverage)
			levelMaxQuantity := levelMaxPositionValue / d.Price
			if levelMaxQuantity < maxQuantityPerLevel {
				maxQuantityPerLevel = levelMaxQuantity
			}
		}

		// Cap quantity if it exceeds the maximum allowed
		if quantity > maxQuantityPerLevel {
			logger.Warnf("[Grid] ⚠️ Quantity %.4f exceeds max allowed %.4f (position_value $%.2f > max $%.2f), capping",
				quantity, maxQuantityPerLevel, quantity*d.Price, maxPositionValuePerLevel)
			quantity = maxQuantityPerLevel
		}

		// Safety check: ensure position value is reasonable (within 2x of intended max as absolute limit)
		positionValue := quantity * d.Price
		absoluteMaxValue := gridConfig.TotalInvestment * float64(gridConfig.Leverage) * 2 // 2x safety margin
		if positionValue > absoluteMaxValue {
			logger.Errorf("[Grid] CRITICAL: Position value $%.2f exceeds absolute max $%.2f! Rejecting order.",
				positionValue, absoluteMaxValue)
			return fmt.Errorf("position value $%.2f exceeds safety limit $%.2f", positionValue, absoluteMaxValue)
		}
	}

	// CRITICAL: Check total position limit before placing order
	orderValue := quantity * d.Price
	allowed, currentValue, maxValue := at.checkTotalPositionLimit(d.Symbol, side, orderValue)
	if !allowed {
		logger.Errorf("[Grid] TOTAL POSITION LIMIT EXCEEDED: current=$%.2f + order=$%.2f > max=$%.2f. Rejecting order.",
			currentValue, orderValue, maxValue)
		return fmt.Errorf("total position value $%.2f would exceed limit $%.2f", currentValue+orderValue, maxValue)
	}

	// In hedge mode: place_buy_limit always opens long, place_sell_limit always opens short.
	// Closing positions is handled exclusively by reduce_long / reduce_short actions.
	positionSide := "LONG"
	if side == "SELL" {
		positionSide = "SHORT"
	}

	req := &LimitOrderRequest{
		Symbol:       d.Symbol,
		Side:         side,
		PositionSide: positionSide,
		Price:        d.Price,
		Quantity:     quantity, // Use validated/capped quantity
		Leverage:   gridConfig.Leverage,
		PostOnly:   gridConfig.UseMakerOnly,
		ReduceOnly: false,
		ClientID:   fmt.Sprintf("grid-%d-%d", d.LevelIndex, time.Now().UnixNano()%1000000),
	}

	result, err := gridTrader.PlaceLimitOrder(req)
	if err != nil {
		return fmt.Errorf("failed to place limit order: %w", err)
	}

	// Update grid level state
	at.gridState.mu.Lock()
	if d.LevelIndex >= 0 && d.LevelIndex < len(at.gridState.Levels) {
		at.gridState.Levels[d.LevelIndex].State = "pending"
		at.gridState.Levels[d.LevelIndex].OrderID = result.OrderID
		at.gridState.Levels[d.LevelIndex].OrderQuantity = d.Quantity
		at.gridState.Levels[d.LevelIndex].OrderPlacedAt = time.Now()
		at.gridState.OrderBook[result.OrderID] = d.LevelIndex
	}
	// T-trade tagging is handled at reduce_position interception time.
	// Normal buy orders are NOT tagged here to avoid false positives.
	at.gridState.mu.Unlock()

	logger.Infof("[Grid] Placed %s limit order at $%.2f, qty=%.4f, level=%d, orderID=%s",
		side, d.Price, d.Quantity, d.LevelIndex, result.OrderID)

	return nil
}

// cancelGridOrder cancels a specific grid order
func (at *AutoTrader) cancelGridOrder(d *kernel.Decision) error {
	gridTrader, ok := at.trader.(GridTrader)
	if !ok {
		gridTrader = NewGridTraderAdapter(at.trader)
	}

	// Resolve order ID: AI provides level_index, not order_id
	orderID := d.OrderID
	if orderID == "" && d.LevelIndex >= 0 {
		at.gridState.mu.RLock()
		if d.LevelIndex < len(at.gridState.Levels) {
			orderID = at.gridState.Levels[d.LevelIndex].OrderID
		}
		at.gridState.mu.RUnlock()
	}
	if orderID == "" {
		return fmt.Errorf("cancel_order: no order ID found for level %d", d.LevelIndex)
	}

	if err := gridTrader.CancelOrder(d.Symbol, orderID); err != nil {
		return fmt.Errorf("failed to cancel order: %w", err)
	}

	// Update state
	at.gridState.mu.Lock()
	if levelIdx, ok := at.gridState.OrderBook[orderID]; ok {
		if levelIdx >= 0 && levelIdx < len(at.gridState.Levels) {
			at.gridState.Levels[levelIdx].State = "empty"
			at.gridState.Levels[levelIdx].OrderID = ""
			at.gridState.Levels[levelIdx].OrderQuantity = 0
			at.gridState.Levels[levelIdx].OrderPlacedAt = time.Time{}
		}
		delete(at.gridState.OrderBook, orderID)
	}
	at.gridState.mu.Unlock()

	logger.Infof("[Grid] Cancelled order: %s (level %d)", orderID, d.LevelIndex)
	return nil
}

// cancelAllGridOrders cancels all grid orders
func (at *AutoTrader) cancelAllGridOrders() error {
	gridConfig := at.config.StrategyConfig.GridConfig

	// Get T-trade order IDs to protect them
	at.gridState.mu.RLock()
	tTradePrepOrderID := at.gridState.TTradePrepOrderID
	tTradeReduceOrderID := at.gridState.TTradeReduceOrderID
	at.gridState.mu.RUnlock()

	// Get all open orders
	openOrders, err := at.trader.GetOpenOrders(gridConfig.Symbol)
	if err != nil {
		return fmt.Errorf("failed to get open orders: %w", err)
	}

	// Cancel orders one by one, skipping T-trade orders
	cancelCount := 0
	for _, order := range openOrders {
		if order.OrderID == tTradePrepOrderID && tTradePrepOrderID != "" {
			logger.Infof("[Grid] Skipping T-trade prep order %s during cancel all", order.OrderID)
			continue
		}
		if order.OrderID == tTradeReduceOrderID && tTradeReduceOrderID != "" {
			logger.Infof("[Grid] Skipping T-trade reduce order %s during cancel all", order.OrderID)
			continue
		}
		if gridTrader, ok := at.trader.(GridTrader); ok {
			if err := gridTrader.CancelOrder(gridConfig.Symbol, order.OrderID); err != nil {
				logger.Warnf("[Grid] Failed to cancel order %s: %v", order.OrderID, err)
			} else {
				cancelCount++
			}
		}
	}

	// Reset all pending levels except T-trade
	at.gridState.mu.Lock()
	for i := range at.gridState.Levels {
		if at.gridState.Levels[i].State == "pending" && at.gridState.Levels[i].OrderID != tTradePrepOrderID {
			at.gridState.Levels[i].State = "empty"
			at.gridState.Levels[i].OrderID = ""
			at.gridState.Levels[i].OrderQuantity = 0
			at.gridState.Levels[i].OrderPlacedAt = time.Time{}
		}
	}
	// Rebuild OrderBook, keeping T-trade order
	newOrderBook := make(map[string]int)
	for i, level := range at.gridState.Levels {
		if level.State == "pending" && level.OrderID != "" {
			newOrderBook[level.OrderID] = i
		}
	}
	at.gridState.OrderBook = newOrderBook

	// Clear T-trade pending reduce since all non-T-trade orders were cancelled
	if at.gridState.TTradePendingReduceQty > 0 {
		logger.Warnf("[Grid] Clearing T-trade pending reduce (%.4f) - reduce orders were cancelled",
			at.gridState.TTradePendingReduceQty)
		at.gridState.TTradePendingReduceQty = 0
	}

	at.gridState.mu.Unlock()

	logger.Infof("[Grid] Cancelled %d orders (protected T-trade order)", cancelCount)
	return nil
}

// pauseGrid pauses grid trading
func (at *AutoTrader) pauseGrid(reason string) error {
	at.cancelAllGridOrders()

	at.gridState.mu.Lock()
	at.gridState.IsPaused = true
	at.gridState.mu.Unlock()

	logger.Infof("[Grid] Paused: %s", reason)
	return nil
}

// resumeGrid resumes grid trading
func (at *AutoTrader) resumeGrid() error {
	at.gridState.mu.Lock()
	at.gridState.IsPaused = false
	at.gridState.mu.Unlock()

	logger.Infof("[Grid] Resumed")
	return nil
}

// adjustGrid adjusts grid parameters
func (at *AutoTrader) adjustGrid(d *kernel.Decision) error {
	// Cancel existing orders first
	at.cancelAllGridOrders()

	gridConfig := at.config.StrategyConfig.GridConfig

	// Get current price
	price, err := at.trader.GetMarketPrice(gridConfig.Symbol)
	if err != nil {
		return fmt.Errorf("failed to get market price: %w", err)
	}

	// Reinitialize grid levels
	at.initializeGridLevels(price, gridConfig)

	logger.Infof("[Grid] Adjusted grid bounds around price $%.2f", price)
	return nil
}

// syncOpenOrdersFromExchange reconciles grid level states with actual open orders on the exchange.
// Called before building AI context so the AI always sees up-to-date order state.
// It does NOT attempt fill detection — that is handled by syncGridState after execution.
func (at *AutoTrader) syncOpenOrdersFromExchange(openOrders []types.OpenOrder) {
	gridConfig := at.config.StrategyConfig.GridConfig

	if openOrders == nil {
		var err error
		openOrders, err = at.trader.GetOpenOrders(gridConfig.Symbol)
		if err != nil {
			logger.Warnf("[Grid] syncOpenOrders: failed to get open orders: %v", err)
			return
		}
	}

	// Build set of active order IDs from exchange
	activeOrderIDs := make(map[string]bool, len(openOrders))
	for _, o := range openOrders {
		activeOrderIDs[o.OrderID] = true
	}

	at.gridState.mu.Lock()
	defer at.gridState.mu.Unlock()

	for i := range at.gridState.Levels {
		level := &at.gridState.Levels[i]
		if level.State != "pending" || level.OrderID == "" {
			continue
		}
		if !activeOrderIDs[level.OrderID] {
			// Grace period: if order was placed very recently, exchange API may not reflect it yet.
			// Skip marking empty for 30 seconds after placement to avoid false resets.
			if !level.OrderPlacedAt.IsZero() && time.Since(level.OrderPlacedAt) < 30*time.Second {
				logger.Debugf("[Grid] syncOpenOrders: level %d order %s not yet visible on exchange (placed %.0fs ago), skipping",
					i, level.OrderID, time.Since(level.OrderPlacedAt).Seconds())
				continue
			}
			// Order is gone from exchange — mark empty so AI knows to re-place it.
			// Fill detection (position accounting) is handled separately in syncGridState.
			logger.Infof("[Grid] syncOpenOrders: level %d order %s no longer open, marking empty",
				i, level.OrderID)
			delete(at.gridState.OrderBook, level.OrderID)
			level.State = "empty"
			level.OrderID = ""
			level.OrderQuantity = 0
			level.OrderPlacedAt = time.Time{}
		}
	}

	// Also register any exchange orders that are not yet tracked in OrderBook
	// (e.g. orders placed outside this session or after a restart)
	for _, o := range openOrders {
		if _, tracked := at.gridState.OrderBook[o.OrderID]; tracked {
			continue
		}
		// Try to match by price to an empty level
		bestIdx := -1
		bestDist := math.MaxFloat64
		for i, level := range at.gridState.Levels {
			if level.State != "empty" {
				continue
			}
			dist := math.Abs(level.Price - o.Price)
			if dist < bestDist {
				bestDist = dist
				bestIdx = i
			}
		}
		// Only adopt if within half a grid spacing
		if bestIdx >= 0 && (at.gridState.GridSpacing <= 0 || bestDist <= at.gridState.GridSpacing*0.5) {
			at.gridState.Levels[bestIdx].State = "pending"
			at.gridState.Levels[bestIdx].OrderID = o.OrderID
			at.gridState.Levels[bestIdx].OrderQuantity = o.Quantity
			at.gridState.OrderBook[o.OrderID] = bestIdx
			logger.Infof("[Grid] syncOpenOrders: adopted untracked order %s → level %d (price=%.2f)",
				o.OrderID, bestIdx, o.Price)
		}
	}

	logger.Infof("[Grid] syncOpenOrders: exchange has %d open orders, grid has %d pending levels",
		len(openOrders), func() int {
			n := 0
			for _, l := range at.gridState.Levels {
				if l.State == "pending" {
					n++
				}
			}
			return n
		}())
}

// syncGridState syncs grid state with exchange
func (at *AutoTrader) syncGridState() {
	gridConfig := at.config.StrategyConfig.GridConfig

	// Get open orders from exchange
	openOrders, err := at.trader.GetOpenOrders(gridConfig.Symbol)
	if err != nil {
		logger.Warnf("[Grid] Failed to get open orders: %v", err)
		return
	}

	// Build set of active order IDs
	activeOrderIDs := make(map[string]bool)
	for _, order := range openOrders {
		activeOrderIDs[order.OrderID] = true
	}

	// Get current positions to verify fills
	positions, err := at.trader.GetPositions()
	currentPositionSize := 0.0
	actualLongSize := 0.0
	actualShortSize := 0.0
	if err != nil {
		logger.Warnf("[Grid] Failed to get positions for state sync: %v", err)
	} else {
		for _, pos := range positions {
			if sym, ok := pos["symbol"].(string); ok && sym == gridConfig.Symbol {
				if size, ok := pos["positionAmt"].(float64); ok {
					currentPositionSize = size
				}
				side, _ := pos["side"].(string)
				rawSize, _ := pos["positionAmt"].(float64)
				absSize := math.Abs(rawSize)
				if side == "long" {
					actualLongSize = absSize
				} else if side == "short" {
					actualShortSize = absSize
				}
			}
		}
	}

	// Pre-fetch order status for disappeared pending orders (outside lock, network calls)
	type orderFillInfo struct {
		avgPrice    float64
		isFilled    bool
		statusKnown bool // false if GetOrderStatus failed — treat as unknown, not cancelled
	}
	fillInfoByOrderID := make(map[string]orderFillInfo)
	at.gridState.mu.RLock()
	for _, level := range at.gridState.Levels {
		if level.State == "pending" && level.OrderID != "" && !activeOrderIDs[level.OrderID] {
			status, err := at.trader.GetOrderStatus(gridConfig.Symbol, level.OrderID)
			if err == nil {
				s, _ := status["status"].(string)
				avg, _ := status["avgPrice"].(float64)
				fillInfoByOrderID[level.OrderID] = orderFillInfo{avgPrice: avg, isFilled: s == "FILLED", statusKnown: true}
			} else {
				// Network failure — mark as unknown so we don't misclassify as cancelled
				fillInfoByOrderID[level.OrderID] = orderFillInfo{statusKnown: false}
			}
		}
	}
	at.gridState.mu.RUnlock()

	// Update levels based on order status
	at.gridState.mu.Lock()
	expectedPositionSize := 0.0
	for _, level := range at.gridState.Levels {
		if level.State == "filled" {
			expectedPositionSize += level.PositionSize
		}
	}

	for i := range at.gridState.Levels {
		level := &at.gridState.Levels[i]
		if level.State == "pending" && level.OrderID != "" {
			if !activeOrderIDs[level.OrderID] {
				// Determine fill vs cancel: prefer GetOrderStatus result, fall back to position heuristic
				info := fillInfoByOrderID[level.OrderID]
				// If status is unknown (network failure), skip this level — retry next cycle
				if !info.statusKnown && !(math.Abs(currentPositionSize) > math.Abs(expectedPositionSize)) {
					logger.Warnf("[Grid] Level %d order %s disappeared but status unknown (network error) — skipping, will retry next cycle", i, level.OrderID)
					continue
				}
				wasFilled := info.isFilled || math.Abs(currentPositionSize) > math.Abs(expectedPositionSize)
				if wasFilled {
					// Use actual fill price from exchange; fall back to level price if unavailable
					entryPrice := info.avgPrice
					if entryPrice <= 0 {
						entryPrice = level.Price
					}
					// Position increased, likely filled
					level.State = "filled"
					level.PositionEntry = entryPrice
					level.PositionSize = level.OrderQuantity
					at.gridState.TotalTrades++
					logger.Infof("[Grid] Level %d order filled at $%.2f (level=$%.2f)", i, entryPrice, level.Price)

					// Check if this was a T-trade prep order - if so, signal AI to place reduce next cycle
					if level.OrderID == at.gridState.TTradePrepOrderID && at.gridState.TTradePendingReduceQty > 0 && !at.gridState.TTradePrepExecuted {
						reduceQty := at.gridState.TTradePendingReduceQty
						prepSide := at.gridState.TTradePrepSide
						prepPrice := at.gridState.TTradePrepPrice
						at.gridState.TTradePrepExecuted = true
						at.gridState.TTradePrepOrderID = ""
						at.gridState.TTradePendingReduceQty = 0
						at.gridState.TTradePrepSide = ""
						at.gridState.TTradePrepPrice = 0
						// Signal AI to place reduce order next cycle (same path as checkTTradeOrderFillAndReduce)
						at.gridState.TTradeReadyToReduce = true
						at.gridState.TTradeReadyReduceQty = reduceQty
						at.gridState.TTradeReadyPrepPrice = prepPrice
						logger.Infof("[Grid] ✅ T-trade prep order filled (%.4f @ $%.2f) → ready_to_reduce (qty=%.4f side=%s)",
							level.OrderQuantity, entryPrice, reduceQty, prepSide)
					}
				} else {
					// Position didn't increase as expected, likely cancelled
					// If this was a T-trade prep order, clear the deferred reduce
					if level.OrderID == at.gridState.TTradePrepOrderID {
						logger.Infof("[Grid] ⚠️ T-trade prep order cancelled (orderID=%s) - clearing deferred reduce (%.4f)",
							level.OrderID, at.gridState.TTradePendingReduceQty)
						at.gridState.TTradePrepOrderID = ""
						at.gridState.TTradePendingReduceQty = 0
						at.gridState.TTradePrepSide = ""
					}
					level.State = "empty"
					level.OrderID = ""
					level.OrderQuantity = 0
					level.OrderPlacedAt = time.Time{}
					logger.Infof("[Grid] Level %d order cancelled/expired", i)
				}
				delete(at.gridState.OrderBook, level.OrderID)
			}
		}
	}

	// Reconcile filled levels against actual exchange positions.
	// If actual long/short size is less than what filled levels claim,
	// reduce level PositionSize proportionally (handles reduce_long/reduce_short fills).
	if actualLongSize >= 0 || actualShortSize >= 0 {
		expectedLong := 0.0
		expectedShort := 0.0
		for _, level := range at.gridState.Levels {
			if level.State == "filled" {
				if level.Side == "buy" {
					expectedLong += level.PositionSize
				} else {
					expectedShort += level.PositionSize
				}
			}
		}

		if expectedLong > 0 && actualLongSize < expectedLong-0.001 {
			ratio := actualLongSize / expectedLong
			logger.Infof("[Grid] Reconcile long: expected=%.4f actual=%.4f ratio=%.4f — scaling down filled levels",
				expectedLong, actualLongSize, ratio)
			for i := range at.gridState.Levels {
				level := &at.gridState.Levels[i]
				if level.State == "filled" && level.Side == "buy" {
					newSize := level.PositionSize * ratio
					if newSize < 0.001 {
						level.State = "empty"
						level.PositionSize = 0
						level.PositionEntry = 0
						level.OrderID = ""
						level.OrderQuantity = 0
						level.OrderPlacedAt = time.Time{}
					} else {
						level.PositionSize = newSize
					}
				}
			}
		}

		if expectedShort > 0 && actualShortSize < expectedShort-0.001 {
			ratio := actualShortSize / expectedShort
			logger.Infof("[Grid] Reconcile short: expected=%.4f actual=%.4f ratio=%.4f — scaling down filled levels",
				expectedShort, actualShortSize, ratio)
			for i := range at.gridState.Levels {
				level := &at.gridState.Levels[i]
				if level.State == "filled" && level.Side == "sell" {
					newSize := level.PositionSize * ratio
					if newSize < 0.001 {
						level.State = "empty"
						level.PositionSize = 0
						level.PositionEntry = 0
						level.OrderID = ""
						level.OrderQuantity = 0
						level.OrderPlacedAt = time.Time{}
					} else {
						level.PositionSize = newSize
					}
				}
			}
		}
	}

	at.gridState.mu.Unlock()

	logger.Debugf("[Grid] Synced state: position=%.4f, orders=%d", currentPositionSize, len(openOrders))

	// Check stop loss
	at.checkAndExecuteStopLoss()

	// Check grid skew
	at.autoAdjustGrid()
}

// saveGridDecisionRecord saves the grid decision to database
func (at *AutoTrader) saveGridDecisionRecord(decision *kernel.FullDecision) {
	if at.store == nil {
		return
	}

	at.cycleNumber++

	record := &store.DecisionRecord{
		TraderID:            at.id,
		CycleNumber:         at.cycleNumber,
		Timestamp:           time.Now().UTC(),
		SystemPrompt:        decision.SystemPrompt,
		InputPrompt:         decision.UserPrompt,
		CoTTrace:            decision.CoTTrace,
		RawResponse:         decision.RawResponse,
		AIRequestDurationMs: decision.AIRequestDurationMs,
		Success:             true,
	}

	if len(decision.Decisions) > 0 {
		decisionJSON, _ := json.MarshalIndent(decision.Decisions, "", "  ")
		record.DecisionJSON = string(decisionJSON)

		// Convert kernel.Decision to store.DecisionAction for frontend display
		for _, d := range decision.Decisions {
			actionRecord := store.DecisionAction{
				Action:     d.Action,
				Symbol:     d.Symbol,
				Quantity:   d.Quantity,
				Leverage:   d.Leverage,
				Price:      d.Price,
				StopLoss:   d.StopLoss,
				TakeProfit: d.TakeProfit,
				Confidence: int(d.Confidence),
				Reasoning:  d.Reasoning,
				Timestamp:  time.Now().UTC(),
				Success:    true, // Grid decisions are executed inline
			}
			record.Decisions = append(record.Decisions, actionRecord)
		}
	}

	record.ExecutionLog = append(record.ExecutionLog, fmt.Sprintf("Grid cycle completed with %d decisions", len(decision.Decisions)))

	if err := at.store.Decision().LogDecision(record); err != nil {
		logger.Warnf("[Grid] Failed to save decision record: %v", err)
	}
}

// IsGridStrategy returns true if current strategy is grid trading
func (at *AutoTrader) IsGridStrategy() bool {
	if at.config.StrategyConfig == nil {
		return false
	}
	return at.config.StrategyConfig.StrategyType == "grid_trading" && at.config.StrategyConfig.GridConfig != nil
}

// checkGridSkew checks if grid is heavily skewed (too many fills on one side)
// Returns: (skewed bool, buyFilledCount int, sellFilledCount int)
func (at *AutoTrader) checkGridSkew() (bool, int, int) {
	at.gridState.mu.RLock()
	defer at.gridState.mu.RUnlock()

	buyFilled := 0
	sellFilled := 0
	buyEmpty := 0
	sellEmpty := 0

	for _, level := range at.gridState.Levels {
		if level.Side == "buy" {
			if level.State == "filled" {
				buyFilled++
			} else if level.State == "empty" {
				buyEmpty++
			}
		} else {
			if level.State == "filled" {
				sellFilled++
			} else if level.State == "empty" {
				sellEmpty++
			}
		}
	}

	// Grid is skewed if one side has 3x more fills than the other
	// or if one side is completely empty
	skewed := false
	if buyFilled > 0 && sellFilled == 0 && sellEmpty > 5 {
		skewed = true // All buys filled, no sells
	} else if sellFilled > 0 && buyFilled == 0 && buyEmpty > 5 {
		skewed = true // All sells filled, no buys
	} else if buyFilled >= 3*sellFilled && buyFilled > 5 {
		skewed = true
	} else if sellFilled >= 3*buyFilled && sellFilled > 5 {
		skewed = true
	}

	return skewed, buyFilled, sellFilled
}

// autoAdjustGrid automatically adjusts grid when heavily skewed
func (at *AutoTrader) autoAdjustGrid() {
	// Don't adjust grid if T-trade is in progress
	at.gridState.mu.RLock()
	hasPendingTTrade := at.gridState.TTradePrepOrderID != ""
	at.gridState.mu.RUnlock()
	if hasPendingTTrade {
		logger.Infof("[Grid] Skipping auto-adjust: T-trade order is pending")
		return
	}

	skewed, buyFilled, sellFilled := at.checkGridSkew()
	if !skewed {
		return
	}

	logger.Warnf("[Grid] Grid heavily skewed: buy_filled=%d, sell_filled=%d. Auto-adjusting...",
		buyFilled, sellFilled)

	gridConfig := at.config.StrategyConfig.GridConfig

	// Get current price
	currentPrice, err := at.trader.GetMarketPrice(gridConfig.Symbol)
	if err != nil {
		logger.Errorf("[Grid] Failed to get price for auto-adjust: %v", err)
		return
	}

	// Check if price is near grid boundary
	at.gridState.mu.RLock()
	upper := at.gridState.UpperPrice
	lower := at.gridState.LowerPrice
	at.gridState.mu.RUnlock()

	// Only adjust if price has moved significantly (>30% of grid range)
	gridRange := upper - lower
	midPrice := (upper + lower) / 2
	priceDeviation := math.Abs(currentPrice - midPrice)

	if priceDeviation < gridRange*0.3 {
		return // Price still near center, don't adjust
	}

	logger.Infof("[Grid] Adjusting grid around new price $%.2f", currentPrice)

	// Cancel existing orders first (before taking the lock for state modification)
	if err := at.cancelAllGridOrders(); err != nil {
		logger.Errorf("[Grid] Failed to cancel orders during auto-adjust: %v", err)
		// Continue with adjustment anyway
	}

	// CRITICAL FIX: Hold lock for the entire adjustment operation to ensure atomicity
	at.gridState.mu.Lock()
	defer at.gridState.mu.Unlock()

	// Preserve filled positions before reinitializing
	filledPositions := make(map[int]kernel.GridLevelInfo)
	for i, level := range at.gridState.Levels {
		if level.State == "filled" {
			filledPositions[i] = level
		}
	}

	// CRITICAL FIX: Recalculate grid bounds centered on current price
	// Use the same logic as InitializeGrid() - either ATR-based or default percentage
	if gridConfig.UseATRBounds {
		// Try to get ATR for bound calculation
		mktData, err := market.GetWithTimeframes(gridConfig.Symbol, []string{"4h"}, "4h", 20)
		if err != nil {
			logger.Warnf("[Grid] Failed to get market data for ATR during adjust: %v, using default bounds", err)
			at.calculateDefaultBoundsLocked(currentPrice, gridConfig)
		} else {
			at.calculateATRBoundsLocked(currentPrice, mktData, gridConfig)
		}
	} else {
		// Use default bounds calculation (scaled by grid count)
		at.calculateDefaultBoundsLocked(currentPrice, gridConfig)
	}

	// Recalculate grid spacing based on new bounds
	at.gridState.GridSpacing = (at.gridState.UpperPrice - at.gridState.LowerPrice) / float64(gridConfig.GridCount-1)

	logger.Infof("[Grid] New bounds: $%.2f - $%.2f, spacing: $%.2f",
		at.gridState.LowerPrice, at.gridState.UpperPrice, at.gridState.GridSpacing)

	// Initialize new grid levels (without lock since we already hold it)
	at.initializeGridLevelsLocked(currentPrice, gridConfig)

	// CRITICAL FIX: Restore filled positions - find closest new level for each filled position
	for _, filledLevel := range filledPositions {
		closestIdx := -1
		closestDist := math.MaxFloat64

		for i, newLevel := range at.gridState.Levels {
			dist := math.Abs(newLevel.Price - filledLevel.PositionEntry)
			if dist < closestDist {
				closestDist = dist
				closestIdx = i
			}
		}

		if closestIdx >= 0 {
			// Restore the filled state to the closest level
			at.gridState.Levels[closestIdx].State = "filled"
			at.gridState.Levels[closestIdx].PositionEntry = filledLevel.PositionEntry
			at.gridState.Levels[closestIdx].PositionSize = filledLevel.PositionSize
			at.gridState.Levels[closestIdx].UnrealizedPnL = filledLevel.UnrealizedPnL
			at.gridState.Levels[closestIdx].OrderID = filledLevel.OrderID
			at.gridState.Levels[closestIdx].OrderQuantity = filledLevel.OrderQuantity
			logger.Infof("[Grid] Restored filled position at level %d (entry $%.2f)", closestIdx, filledLevel.PositionEntry)
		}
	}
}

// calculateDefaultBoundsLocked calculates default bounds (caller must hold lock)
func (at *AutoTrader) calculateDefaultBoundsLocked(price float64, config *store.GridStrategyConfig) {
	// Default: ±3% from current price, scaled by grid count
	multiplier := 0.03 * float64(config.GridCount) / 10
	at.gridState.UpperPrice = price * (1 + multiplier)
	at.gridState.LowerPrice = price * (1 - multiplier)
}

// calculateATRBoundsLocked calculates bounds using ATR (caller must hold lock)
func (at *AutoTrader) calculateATRBoundsLocked(price float64, mktData *market.Data, config *store.GridStrategyConfig) {
	atr := 0.0
	if mktData.LongerTermContext != nil {
		atr = mktData.LongerTermContext.ATR14
	}

	if atr <= 0 {
		at.calculateDefaultBoundsLocked(price, config)
		return
	}

	multiplier := config.ATRMultiplier
	if multiplier <= 0 {
		multiplier = 2.0
	}

	halfRange := atr * multiplier
	at.gridState.UpperPrice = price + halfRange
	at.gridState.LowerPrice = price - halfRange
}

// initializeGridLevelsLocked creates the grid level structure (caller must hold lock)
func (at *AutoTrader) initializeGridLevelsLocked(currentPrice float64, config *store.GridStrategyConfig) {
	levels := make([]kernel.GridLevelInfo, config.GridCount)
	totalWeight := 0.0
	weights := make([]float64, config.GridCount)

	// Calculate weights based on distribution
	for i := 0; i < config.GridCount; i++ {
		switch config.Distribution {
		case "gaussian":
			// Gaussian distribution - more weight in the middle
			center := float64(config.GridCount-1) / 2
			sigma := float64(config.GridCount) / 4
			weights[i] = math.Exp(-math.Pow(float64(i)-center, 2) / (2 * sigma * sigma))
		case "pyramid":
			// Pyramid - more weight at bottom
			weights[i] = float64(config.GridCount - i)
		default: // uniform
			weights[i] = 1.0
		}
		totalWeight += weights[i]
	}

	// Create levels
	for i := 0; i < config.GridCount; i++ {
		price := at.gridState.LowerPrice + float64(i)*at.gridState.GridSpacing
		allocatedUSD := config.TotalInvestment * weights[i] / totalWeight

		// Determine initial side (below current price = buy, above = sell)
		side := "buy"
		if price > currentPrice {
			side = "sell"
		}

		levels[i] = kernel.GridLevelInfo{
			Index:        i,
			Price:        price,
			State:        "empty",
			Side:         side,
			AllocatedUSD: allocatedUSD,
		}
	}

	at.gridState.Levels = levels

	// Apply direction-based side assignment if enabled (note: caller holds lock)
	if config.EnableDirectionAdjust {
		at.applyGridDirectionLocked(currentPrice)
	}
}

// applyGridDirectionLocked adjusts grid level sides based on the current direction (caller must hold lock)
func (at *AutoTrader) applyGridDirectionLocked(currentPrice float64) {
	config := at.gridState.Config
	direction := at.gridState.CurrentDirection

	// Get bias ratio from config, default to 0.7 (70%/30%)
	biasRatio := config.DirectionBiasRatio
	if biasRatio <= 0 || biasRatio > 1 {
		biasRatio = 0.7
	}

	buyRatio, _ := direction.GetBuySellRatio(biasRatio)

	// For neutral: use price-based assignment (buy below, sell above)
	if direction == market.GridDirectionNeutral {
		for i := range at.gridState.Levels {
			if at.gridState.Levels[i].Price <= currentPrice {
				at.gridState.Levels[i].Side = "buy"
			} else {
				at.gridState.Levels[i].Side = "sell"
			}
		}
		return
	}

	totalLevels := len(at.gridState.Levels)
	targetBuyLevels := int(float64(totalLevels) * buyRatio)

	switch direction {
	case market.GridDirectionLong:
		for i := range at.gridState.Levels {
			at.gridState.Levels[i].Side = "buy"
		}

	case market.GridDirectionShort:
		for i := range at.gridState.Levels {
			at.gridState.Levels[i].Side = "sell"
		}

	case market.GridDirectionLongBias, market.GridDirectionShortBias:
		buyCount := 0
		sellCount := 0

		for i := range at.gridState.Levels {
			needMoreBuys := buyCount < targetBuyLevels
			needMoreSells := sellCount < (totalLevels - targetBuyLevels)

			if at.gridState.Levels[i].Price <= currentPrice {
				if needMoreBuys {
					at.gridState.Levels[i].Side = "buy"
					buyCount++
				} else {
					at.gridState.Levels[i].Side = "sell"
					sellCount++
				}
			} else {
				if needMoreSells && direction == market.GridDirectionShortBias {
					at.gridState.Levels[i].Side = "sell"
					sellCount++
				} else if needMoreBuys && direction == market.GridDirectionLongBias {
					at.gridState.Levels[i].Side = "buy"
					buyCount++
				} else if needMoreSells {
					at.gridState.Levels[i].Side = "sell"
					sellCount++
				} else {
					at.gridState.Levels[i].Side = "buy"
					buyCount++
				}
			}
		}
	}
}

// GridRiskInfo contains risk information for frontend display
type GridRiskInfo struct {
	CurrentLeverage     int     `json:"current_leverage"`
	EffectiveLeverage   float64 `json:"effective_leverage"`
	RecommendedLeverage int     `json:"recommended_leverage"`

	CurrentPosition float64 `json:"current_position"`
	MaxPosition     float64 `json:"max_position"`
	PositionPercent float64 `json:"position_percent"`

	LiquidationPrice    float64 `json:"liquidation_price"`
	LiquidationDistance float64 `json:"liquidation_distance"`

	RegimeLevel string `json:"regime_level"`

	ShortBoxUpper float64 `json:"short_box_upper"`
	ShortBoxLower float64 `json:"short_box_lower"`
	MidBoxUpper   float64 `json:"mid_box_upper"`
	MidBoxLower   float64 `json:"mid_box_lower"`
	LongBoxUpper  float64 `json:"long_box_upper"`
	LongBoxLower  float64 `json:"long_box_lower"`
	CurrentPrice  float64 `json:"current_price"`

	BreakoutLevel     string `json:"breakout_level"`
	BreakoutDirection string `json:"breakout_direction"`

	// Grid direction
	CurrentGridDirection  string `json:"current_grid_direction"`
	DirectionChangeCount  int    `json:"direction_change_count"`
	EnableDirectionAdjust bool   `json:"enable_direction_adjust"`
}

// GetGridRiskInfo returns current risk information for frontend display
func (at *AutoTrader) GetGridRiskInfo() *GridRiskInfo {
	gridConfig := at.config.StrategyConfig.GridConfig
	if gridConfig == nil {
		return &GridRiskInfo{}
	}

	at.gridState.mu.RLock()
	defer at.gridState.mu.RUnlock()

	// Get current price
	currentPrice, _ := at.trader.GetMarketPrice(gridConfig.Symbol)

	// Use wallet balance (available + margin in positions, excl. unrealized PnL) as total investment
	leverage := gridConfig.Leverage
	totalInvestment := gridConfig.TotalInvestment
	if bal, err := at.trader.GetBalance(); err == nil {
		if w, ok := bal["totalWalletBalance"].(float64); ok && w > 0 {
			totalInvestment = w
		}
	}

	// Get current position value
	positions, _ := at.trader.GetPositions()
	var currentPositionValue float64
	var currentPositionSize float64
	for _, pos := range positions {
		if sym, _ := pos["symbol"].(string); sym == gridConfig.Symbol {
			size, _ := pos["positionAmt"].(float64)
			// Use mark price for current market value, fallback to entry price
			markPrice, hasMarkPrice := pos["markPrice"].(float64)
			if !hasMarkPrice || markPrice == 0 {
				markPrice, _ = pos["entryPrice"].(float64)
			}
			currentPositionValue = math.Abs(size * markPrice)
			currentPositionSize = size
			break
		}
	}

	effectiveLeverage := 0.0
	if totalInvestment > 0 {
		effectiveLeverage = currentPositionValue / totalInvestment
	}

	// Calculate max position based on regime
	regimeLevel := market.RegimeLevel(at.gridState.CurrentRegimeLevel)
	if regimeLevel == "" {
		regimeLevel = market.RegimeLevelStandard
	}

	// Use default position limit since GridStrategyConfig doesn't have regime-specific limits
	// Default is 70% for standard regime
	maxPositionPct := 70.0
	switch regimeLevel {
	case market.RegimeLevelNarrow:
		maxPositionPct = 40.0
	case market.RegimeLevelStandard:
		maxPositionPct = 70.0
	case market.RegimeLevelWide:
		maxPositionPct = 60.0
	case market.RegimeLevelVolatile:
		maxPositionPct = 40.0
	}

	maxPosition := totalInvestment * maxPositionPct / 100 * float64(leverage)

	// Use default leverage limits since GridStrategyConfig doesn't have regime-specific limits
	recommendedLeverage := leverage
	switch regimeLevel {
	case market.RegimeLevelNarrow:
		recommendedLeverage = min(leverage, 2)
	case market.RegimeLevelStandard:
		recommendedLeverage = min(leverage, 4)
	case market.RegimeLevelWide:
		recommendedLeverage = min(leverage, 3)
	case market.RegimeLevelVolatile:
		recommendedLeverage = min(leverage, 2)
	}

	// Calculate liquidation distance and price only when there's a position
	var liquidationDistance float64
	var liquidationPrice float64
	if currentPositionSize != 0 && currentPrice > 0 {
		liquidationDistance = 100.0 / float64(leverage) * 0.9 // ~90% of theoretical max
		if currentPositionSize > 0 {
			// Long position: liquidation below entry
			liquidationPrice = currentPrice * (1 - liquidationDistance/100)
		} else {
			// Short position: liquidation above entry
			liquidationPrice = currentPrice * (1 + liquidationDistance/100)
		}
	}

	positionPercent := 0.0
	if maxPosition > 0 {
		positionPercent = currentPositionValue / maxPosition * 100
	}

	return &GridRiskInfo{
		CurrentLeverage:     leverage,
		EffectiveLeverage:   effectiveLeverage,
		RecommendedLeverage: recommendedLeverage,

		CurrentPosition: currentPositionValue,
		MaxPosition:     maxPosition,
		PositionPercent: positionPercent,

		LiquidationPrice:    liquidationPrice,
		LiquidationDistance: liquidationDistance,

		RegimeLevel: string(regimeLevel),

		ShortBoxUpper: at.gridState.ShortBoxUpper,
		ShortBoxLower: at.gridState.ShortBoxLower,
		MidBoxUpper:   at.gridState.MidBoxUpper,
		MidBoxLower:   at.gridState.MidBoxLower,
		LongBoxUpper:  at.gridState.LongBoxUpper,
		LongBoxLower:  at.gridState.LongBoxLower,
		CurrentPrice:  currentPrice,

		BreakoutLevel:     at.gridState.BreakoutLevel,
		BreakoutDirection: at.gridState.BreakoutDirection,

		CurrentGridDirection:  string(at.gridState.CurrentDirection),
		DirectionChangeCount:  at.gridState.DirectionChangeCount,
		EnableDirectionAdjust: gridConfig.EnableDirectionAdjust,
	}
}

// checkAndExecuteStopLoss is disabled.
func (at *AutoTrader) checkAndExecuteStopLoss() {
}

// ============================================================================
// Trapped Position Detection & Batch Reduction (被套分批减仓)
// ============================================================================

// logGridTrade writes a structured trade action record to grid_trade_logs.
// source: "ai" | "ttrade" | "profit_reduce" | "profit_drawdown"
func (at *AutoTrader) logGridTrade(source, action, side, symbol, reason, orderID string,
	qty, price, entryPrice, markPrice, marginProfit, unrealizedPL float64, success bool, errMsg string) {
	if at.store == nil {
		return
	}
	entry := &store.GridTradeLogModel{
		InstanceID:   at.id,
		Source:       source,
		Action:       action,
		Symbol:       symbol,
		Side:         side,
		Quantity:     qty,
		Price:        price,
		EntryPrice:   entryPrice,
		MarkPrice:    markPrice,
		MarginProfit: marginProfit,
		UnrealizedPL: unrealizedPL,
		Reason:       reason,
		OrderID:      orderID,
		Success:      success,
		ErrorMsg:     errMsg,
	}
	if err := at.store.Grid().LogGridTrade(entry); err != nil {
		logger.Warnf("[Grid] Failed to write trade log: %v", err)
	}
}

// checkTTradeOrderFillAndReduce checks if the pending T-trade buy order has been filled.
// This is called every cycle BEFORE AI decisions.
// Flow: placeGridLimitOrder (buy) → [wait here each cycle] → fill confirmed → executeTrappedReduce
// autoTagTTradeFromExistingOrders automatically tags the nearest existing grid order as T-trade prep.
// Long trapped → find nearest pending sell order (closest above current price)
// Short trapped → find nearest pending buy order (closest below current price)
// When that order fills naturally, AI places a reduce order to capture the spread.
func (at *AutoTrader) autoTagTTradeFromExistingOrders(openOrders []types.OpenOrder) {
	gridConfig := at.config.StrategyConfig.GridConfig
	if !gridConfig.EnableTrappedReduce {
		return
	}

	at.gridState.mu.RLock()
	alreadyPending := at.gridState.TTradePrepOrderID != ""
	pendingOrderID := at.gridState.TTradePrepOrderID
	readyToReduce := at.gridState.TTradeReadyToReduce
	reduceOrderPending := at.gridState.TTradeReduceOrderID != ""
	at.gridState.mu.RUnlock()

	// Don't re-tag if we're already in ready_to_reduce or a reduce order is pending
	if readyToReduce || reduceOrderPending {
		logger.Infof("[Grid] T-trade auto-tag skipped: already in ready_to_reduce or reduce order pending")
		return
	}

	if alreadyPending {
		// Check if the tagged order is still open
		stillOpen := false
		for _, o := range openOrders {
			if o.OrderID == pendingOrderID {
				stillOpen = true
				break
			}
		}
		if stillOpen {
			return
		}
		// Tagged order no longer open — trigger fill check BEFORE clearing state.
		// checkTTradeOrderFillAndReduce calls GetOrderStatus to confirm fill vs cancel.
		// If filled: sets TTradeReadyToReduce=true and clears TTradePrepOrderID.
		// If cancelled: clears TTradePrepOrderID.
		logger.Infof("[Grid] T-trade: tagged order %s no longer open — checking fill status before re-tagging", pendingOrderID)
		at.checkTTradeOrderFillAndReduce(openOrders)

		// If fill was confirmed, don't re-tag — AI will place reduce next cycle
		at.gridState.mu.RLock()
		nowReadyToReduce := at.gridState.TTradeReadyToReduce
		at.gridState.mu.RUnlock()
		if nowReadyToReduce {
			return
		}

		// Fill not confirmed (cancelled or unknown) — ensure state is cleared before re-tagging
		at.gridState.mu.Lock()
		at.gridState.TTradePrepOrderID = ""
		at.gridState.TTradePrepPrice = 0
		at.gridState.TTradePrepQty = 0
		at.gridState.TTradePendingReduceQty = 0
		at.gridState.TTradePrepSide = ""
		at.gridState.TTradePrepExecuted = false
		at.gridState.mu.Unlock()
	}

	// Build trapped info
	ctx, err := at.buildGridContext()
	if err != nil || ctx == nil || ctx.TrappedInfo == nil || !ctx.TrappedInfo.IsTrapped {
		return
	}
	trapped := ctx.TrappedInfo

	currentPrice := ctx.CurrentPrice

	// Find nearest pending grid order on the appropriate side.
	// Source of truth: exchange openOrders (level.Side can drift after direction
	// adjustments; the exchange order side is always authoritative).
	// Long trapped (price fell): tag nearest BUY below current price — fills on
	// further drop, adds long at lower cost, then reduce_long at t_b > b captures the spread.
	// Short trapped (price rose): tag nearest SELL above current price — fills on
	// further rise, adds short at higher cost, then reduce_short at t_a < a captures the spread.
	at.gridState.mu.RLock()
	gridOrderIDs := make(map[string]int, len(at.gridState.Levels))
	for i, level := range at.gridState.Levels {
		if level.State == "pending" && level.OrderID != "" {
			gridOrderIDs[level.OrderID] = i
		}
	}
	leverage := at.config.StrategyConfig.GridConfig.Leverage
	at.gridState.mu.RUnlock()

	var bestOrderID string
	var bestPrice float64
	var bestSide string
	var bestQty float64

	for _, o := range openOrders {
		// Only consider orders tracked by the grid (skip manual orders)
		levelIdx, ok := gridOrderIDs[o.OrderID]
		if !ok {
			continue
		}
		side := o.Side
		if side == "BUY" {
			side = "buy"
		} else if side == "SELL" {
			side = "sell"
		}
		price := o.Price
		if price <= 0 {
			continue
		}

		if trapped.Side == "buy" && side == "buy" && price < currentPrice {
			// Long trapped: pick the buy closest to current price (highest buy < current)
			if bestOrderID == "" || price > bestPrice {
				bestOrderID = o.OrderID
				bestPrice = price
				bestSide = "buy"
				bestQty = o.Quantity
				if bestQty == 0 {
					at.gridState.mu.RLock()
					lvl := at.gridState.Levels[levelIdx]
					if lvl.AllocatedUSD > 0 {
						bestQty = lvl.AllocatedUSD * float64(leverage) / price
					} else {
						bestQty = lvl.OrderQuantity
					}
					at.gridState.mu.RUnlock()
				}
			}
		} else if trapped.Side == "sell" && side == "sell" && price > currentPrice {
			// Short trapped: pick the sell closest to current price (lowest sell > current)
			if bestOrderID == "" || price < bestPrice {
				bestOrderID = o.OrderID
				bestPrice = price
				bestSide = "sell"
				bestQty = o.Quantity
				if bestQty == 0 {
					at.gridState.mu.RLock()
					lvl := at.gridState.Levels[levelIdx]
					if lvl.AllocatedUSD > 0 {
						bestQty = lvl.AllocatedUSD * float64(leverage) / price
					} else {
						bestQty = lvl.OrderQuantity
					}
					at.gridState.mu.RUnlock()
				}
			}
		}
	}

	if bestOrderID == "" {
		logger.Infof("[Grid] T-trade: trapped (%s, loss=%.2f%%) but no suitable pending grid order found",
			trapped.Side, trapped.LossPct)
		return
	}

	reduceQty := bestQty

	at.gridState.mu.Lock()
	at.gridState.TTradePrepOrderID = bestOrderID
	at.gridState.TTradePrepPrice = bestPrice
	at.gridState.TTradePrepQty = reduceQty
	at.gridState.TTradePrepPlacedAt = time.Now()
	at.gridState.TTradePendingReduceQty = reduceQty
	at.gridState.TTradePrepSide = bestSide
	at.gridState.TTradePrepExecuted = false
	at.gridState.mu.Unlock()

	logger.Infof("[Grid] ✅ T-trade auto-tagged: %s trapped (loss=%.2f%%) → watching order %s @ %.2f, will reduce %.4f on fill",
		trapped.Side, trapped.LossPct, bestOrderID, bestPrice, reduceQty)
	at.logGridTrade("ttrade", "ttrade_tag", trapped.Side, gridConfig.Symbol,
		fmt.Sprintf("tagged order %s @ %.2f, loss=%.2f%%", bestOrderID, bestPrice, trapped.LossPct),
		bestOrderID, reduceQty, bestPrice, 0, 0, 0, trapped.PriceDiffPct, true, "")
}

func (at *AutoTrader) checkTTradeOrderFillAndReduce(openOrders []types.OpenOrder) {
	gridConfig := at.config.StrategyConfig.GridConfig
	if !gridConfig.EnableTrappedReduce {
		return
	}

	at.gridState.mu.RLock()
	pendingOrderID := at.gridState.TTradePrepOrderID
	pendingReduceQty := at.gridState.TTradePendingReduceQty
	buyPrice := at.gridState.TTradePrepPrice
	placedAt := at.gridState.TTradePrepPlacedAt
	prepSide := at.gridState.TTradePrepSide
	at.gridState.mu.RUnlock()

	// Nothing pending
	if pendingOrderID == "" || pendingReduceQty <= 0 {
		return
	}

	// Check if order has timed out — just clear T-trade state, do NOT cancel
	// (the order is a real grid order, cancelling it would break the grid)
	maxWait := 4 * time.Hour
	if !placedAt.IsZero() && time.Since(placedAt) > maxWait {
		logger.Warnf("[Grid] ⚠️ T-trade order %s timed out after %.0f min — clearing T-trade state (order kept alive).",
			pendingOrderID, time.Since(placedAt).Minutes())
		at.gridState.mu.Lock()
		at.gridState.TTradePrepOrderID = ""
		at.gridState.TTradePrepPrice = 0
		at.gridState.TTradePrepQty = 0
		at.gridState.TTradePrepPlacedAt = time.Time{}
		at.gridState.TTradePendingReduceQty = 0
		at.gridState.TTradePrepSide = ""
		at.gridState.mu.Unlock()
		logger.Infof("[Grid] T-trade state cleared due to timeout. Reduce will be re-evaluated next cycle.")
		return
	}

	// Query current open orders to see if the T-trade buy order is still pending
	if openOrders == nil {
		var err error
		openOrders, err = at.trader.GetOpenOrders(gridConfig.Symbol)
		if err != nil {
			logger.Warnf("[Grid] T-trade check: failed to get open orders: %v", err)
			return
		}
	}

	// Check if the order is still open
	stillOpen := false
	for _, o := range openOrders {
		if o.OrderID == pendingOrderID {
			stillOpen = true
			break
		}
	}

	if stillOpen {
		logger.Infof("[Grid] ⏳ T-trade buy order %s still open (price=%.2f, placed %.0f min ago) — waiting for fill before reducing %.4f",
			pendingOrderID, buyPrice, time.Since(placedAt).Minutes(), pendingReduceQty)
		return
	}

	// Order is NO LONGER in open orders → it was filled (or cancelled)!
	// Verify by checking if the level state changed to "filled" in grid state
	filled := false
	at.gridState.mu.RLock()
	for _, level := range at.gridState.Levels {
		if level.OrderID == pendingOrderID && level.State == "filled" {
			filled = true
			break
		}
	}
	at.gridState.mu.RUnlock()

	if !filled {
		// Order disappeared from open orders but level not marked filled yet.
		// Confirm via GetOrderStatus before treating as cancelled — avoids false
		// cancellation when syncOpenOrdersFromExchange missed the fill due to network error.
		statusMap, err := at.trader.GetOrderStatus(gridConfig.Symbol, pendingOrderID)
		if err != nil {
			logger.Warnf("[Grid] T-trade prep order %s disappeared but GetOrderStatus failed (%v) — skipping, will retry next cycle", pendingOrderID, err)
			return
		}
		statusStr, _ := statusMap["status"].(string)
		if statusStr == "FILLED" {
			// Exchange confirms filled — treat same as level.State=="filled"
			filled = true
			if avg, ok := statusMap["avgPrice"].(float64); ok && avg > 0 {
				buyPrice = avg
			}
		} else if statusStr == "CANCELED" || statusStr == "EXPIRED" {
			// Confirmed cancelled
			logger.Warnf("[Grid] T-trade prep order %s was cancelled (status=%s) — clearing state, will re-tag next cycle",
				pendingOrderID, statusStr)
			at.gridState.mu.Lock()
			at.gridState.TTradePrepOrderID = ""
			at.gridState.TTradePrepPrice = 0
			at.gridState.TTradePrepQty = 0
			at.gridState.TTradePrepPlacedAt = time.Time{}
			at.gridState.TTradePendingReduceQty = 0
			at.gridState.TTradePrepSide = ""
			at.gridState.TTradePrepExecuted = false
			at.gridState.mu.Unlock()
			return
		} else {
			// Unknown status — don't clear, retry next cycle
			logger.Warnf("[Grid] T-trade prep order %s has unexpected status=%q — skipping, will retry next cycle", pendingOrderID, statusStr)
			return
		}
	}

	// ✅ T-trade prep order is CONFIRMED FILLED — signal AI to place reduce order
	logger.Infof("[Grid] ✅ T-trade prep order %s FILLED @ %.2f — setting ready_to_reduce (qty=%.4f side=%s)",
		pendingOrderID, buyPrice, pendingReduceQty, prepSide)

	at.gridState.mu.Lock()
	if at.gridState.TTradePrepExecuted {
		at.gridState.mu.Unlock()
		logger.Warnf("[Grid] T-trade reduce already executed for order %s, skipping duplicate", pendingOrderID)
		return
	}
	reduceQty := at.gridState.TTradePendingReduceQty
	prepSide = at.gridState.TTradePrepSide
	at.gridState.TTradePrepExecuted = true
	at.gridState.TTradePrepOrderID = ""
	at.gridState.TTradePrepPrice = 0
	at.gridState.TTradePrepQty = 0
	at.gridState.TTradePrepPlacedAt = time.Time{}
	at.gridState.TTradePendingReduceQty = 0
	at.gridState.TTradePrepSide = ""
	// Signal AI to place reduce order next cycle
	at.gridState.TTradeReadyToReduce = true
	at.gridState.TTradeReadyReduceQty = reduceQty
	at.gridState.TTradeReadyPrepPrice = buyPrice // AI must place reduce at a better price than this
	at.gridState.mu.Unlock()

	at.logGridTrade("ttrade", "ttrade_fill", prepSide, gridConfig.Symbol,
		fmt.Sprintf("prep order %s filled @ %.2f, waiting for AI to reduce %.4f", pendingOrderID, buyPrice, reduceQty),
		pendingOrderID, reduceQty, buyPrice, 0, 0, 0, 0, true, "")
}

// checkTTradeReduceOrderStatus checks if reduce order was cancelled and retries
func (at *AutoTrader) checkTTradeReduceOrderStatus(openOrders []types.OpenOrder) {
	gridConfig := at.config.StrategyConfig.GridConfig
	if !gridConfig.EnableTrappedReduce {
		return
	}

	at.gridState.mu.RLock()
	reduceOrderID := at.gridState.TTradeReduceOrderID
	reducePlacedAt := at.gridState.TTradeReducePlacedAt
	at.gridState.mu.RUnlock()

	if reduceOrderID == "" {
		return
	}

	// Timeout: cancel reduce order if unfilled for too long (price moved away)
	maxWait := 2 * time.Hour
	if !reducePlacedAt.IsZero() && time.Since(reducePlacedAt) > maxWait {
		logger.Warnf("[Grid] ⚠️ T-trade reduce order %s timed out after %.0f min — cancelling",
			reduceOrderID, time.Since(reducePlacedAt).Minutes())
		if canceler, ok := at.trader.(interface {
			CancelOrder(symbol, orderID string) error
		}); ok {
			canceler.CancelOrder(gridConfig.Symbol, reduceOrderID)
		}
		at.gridState.mu.Lock()
		at.gridState.TTradeReduceOrderID = ""
		at.gridState.TTradeReducePlacedAt = time.Time{}
		at.gridState.TTradeReduceQty = 0
		at.gridState.TTradeReducePrice = 0
		at.gridState.TTradeReduceSide = ""
		// Keep TTradeReadyToReduce=true so AI re-places at fresh price next cycle
		at.gridState.mu.Unlock()
		return
	}

	// Check if reduce order still exists
	if openOrders == nil {
		var err error
		openOrders, err = at.trader.GetOpenOrders(gridConfig.Symbol)
		if err != nil {
			return
		}
	}

	stillOpen := false
	for _, o := range openOrders {
		if o.OrderID == reduceOrderID {
			stillOpen = true
			break
		}
	}

	if !stillOpen {
		// Distinguish fill vs cancel via GetOrderStatus
		statusMap, err := at.trader.GetOrderStatus(gridConfig.Symbol, reduceOrderID)
		statusStr, _ := statusMap["status"].(string)
		at.gridState.mu.Lock()
		if err == nil && (statusStr == "CANCELED" || statusStr == "EXPIRED") {
			// Order was cancelled -- re-arm TTradeReadyToReduce so AI replaces it next cycle
			logger.Warnf("[Grid] T-trade reduce order %s was cancelled (status=%s) -- will re-place next cycle",
				reduceOrderID, statusStr)
			at.gridState.TTradeReadyToReduce = true
			at.gridState.TTradeReadyReduceQty = at.gridState.TTradeReduceQty
		} else {
			// Filled (or unknown) -- clear all T-trade state
			logger.Infof("[Grid] T-trade reduce order %s filled (status=%s) -- clearing T-trade state", reduceOrderID, statusStr)
			at.gridState.TTradeReadyToReduce = false
			at.gridState.TTradeReadyReduceQty = 0
			at.gridState.TTradeReadyPrepPrice = 0
			at.gridState.TTradeReduceQty = 0
			at.gridState.TTradeReducePrice = 0
			at.gridState.TTradeReduceSide = ""
		}
		at.gridState.TTradeReduceOrderID = ""
		at.gridState.TTradeReducePlacedAt = time.Time{}
		at.gridState.mu.Unlock()
	}
}

// buildTrappedPositionInfo builds trapped position information for AI context
func (at *AutoTrader) buildTrappedPositionInfo(currentPrice float64) *kernel.TrappedPositionInfo {
	gridConfig := at.config.StrategyConfig.GridConfig

	threshold := gridConfig.TrappedReduceThresholdPct
	if threshold <= 0 {
		threshold = 3.0
	}
	// Use exchange positions as the primary source of truth
	exchangePositions, err := at.trader.GetPositions()
	if err != nil || len(exchangePositions) == 0 {
		return &kernel.TrappedPositionInfo{IsTrapped: false}
	}

	// Aggregate position data by side
	type SideData struct {
		pnl      float64
		size     float64
		entrySum float64
		count    int
	}

	longData := SideData{}
	shortData := SideData{}

	for _, pos := range exchangePositions {
		symbol, _ := pos["symbol"].(string)
		if symbol != gridConfig.Symbol {
			continue
		}

		side, _ := pos["side"].(string)
		size, _ := pos["positionAmt"].(float64)
		entry, _ := pos["entryPrice"].(float64)
		pnl, _ := pos["unRealizedProfit"].(float64)
		if pnl == 0 {
			pnl, _ = pos["unrealized_pnl"].(float64)
		}

		if size <= 0 {
			continue
		}

		if side == "long" {
			longData.pnl += pnl
			longData.size += size
			longData.entrySum += entry * size
			longData.count++
		} else if side == "short" {
			shortData.pnl += pnl
			shortData.size += size
			shortData.entrySum += entry * size
			shortData.count++
		}
	}

	logger.Infof("[Grid] Position summary: LONG(pnl=%.2f, size=%.4f, count=%d) SHORT(pnl=%.2f, size=%.4f, count=%d)",
		longData.pnl, longData.size, longData.count, shortData.pnl, shortData.size, shortData.count)

	// Determine trapped side: the side with worse (more negative) PnL
	var trappedSide string
	var trappedData SideData

	if longData.pnl < shortData.pnl {
		trappedSide = "buy"
		trappedData = longData
	} else if shortData.pnl < longData.pnl {
		trappedSide = "sell"
		trappedData = shortData
	} else if longData.pnl < 0 {
		// Both equal and negative, choose long
		trappedSide = "buy"
		trappedData = longData
	} else {
		// No losses
		logger.Infof("[Grid] No trapped position: both sides profitable or zero")
		return &kernel.TrappedPositionInfo{IsTrapped: false}
	}

	// Validate trapped data
	if trappedData.pnl >= 0 || trappedData.count == 0 {
		logger.Infof("[Grid] No trapped position: trappedSide=%s has pnl=%.2f, count=%d", trappedSide, trappedData.pnl, trappedData.count)
		return &kernel.TrappedPositionInfo{IsTrapped: false}
	}

	// Calculate metrics
	avgEntry := 0.0
	if trappedData.size > 0 {
		avgEntry = trappedData.entrySum / trappedData.size
	}

	lossPct := 0.0
	if avgEntry > 0 {
		lossPct = math.Abs(trappedData.pnl) / (avgEntry * trappedData.size / float64(gridConfig.Leverage)) * 100
	}

	priceDiffPct := 0.0
	if avgEntry > 0 {
		if trappedSide == "sell" {
			priceDiffPct = (currentPrice - avgEntry) / avgEntry * 100
		} else {
			priceDiffPct = (avgEntry - currentPrice) / avgEntry * 100
		}
	}

	isTrapped := lossPct >= threshold
	logger.Infof("[Grid] Trapped check: side=%s, lossPct=%.2f%%, threshold=%.2f%%, isTrapped=%v",
		trappedSide, lossPct, threshold, isTrapped)

	suggestReducePct := 0.0

	at.gridState.mu.RLock()
	lastReduceMinutes := -1
	if !at.gridState.LastTrappedReduceAt.IsZero() {
		lastReduceMinutes = int(time.Since(at.gridState.LastTrappedReduceAt).Minutes())
	}

	tTradePhase := "idle"
	tTradeBuyOrderID := ""
	tTradeBuyPrice := 0.0
	tTradePendingReduce := 0.0
	if at.gridState.TTradeReduceOrderID != "" {
		tTradePhase = "waiting_reduce_fill"
		tTradePendingReduce = at.gridState.TTradeReduceQty
	} else if at.gridState.TTradeReadyToReduce {
		tTradePhase = "ready_to_reduce"
		tTradePendingReduce = at.gridState.TTradeReadyReduceQty
	} else if at.gridState.TTradePrepOrderID != "" {
		tTradePhase = "waiting_buy_fill"
		tTradeBuyOrderID = at.gridState.TTradePrepOrderID
		tTradeBuyPrice = at.gridState.TTradePrepPrice
		tTradePendingReduce = at.gridState.TTradePendingReduceQty
	}
	tTradeReadyPrepPrice := at.gridState.TTradeReadyPrepPrice
	at.gridState.mu.RUnlock()

	return &kernel.TrappedPositionInfo{
		IsTrapped:           isTrapped,
		Side:                trappedSide,
		TotalUnrealizedLoss: trappedData.pnl,
		LossPct:             lossPct,
		ThresholdPct:        threshold,
		TrappedLevelCount:   trappedData.count,
		TrappedPositionSize: trappedData.size,
		AvgEntryPrice:       avgEntry,
		CurrentPrice:        currentPrice,
		PriceDiffPct:        priceDiffPct,
		SuggestReducePct:    suggestReducePct,
		LastReduceMinutes:   lastReduceMinutes,
		TTradePhase:         tTradePhase,
		TTradeReadyPrepPrice: tTradeReadyPrepPrice,
		TTradeBuyOrderID:    tTradeBuyOrderID,
		TTradeBuyPrice:      tTradeBuyPrice,
		TTradePendingReduce: tTradePendingReduce,
	}
}

// executeTrappedReduceSide dispatches to the correct reduce based on T-trade prep side.
// side "buy" = long trapped → reduce_long; side "sell" = short trapped → reduce_short
func (at *AutoTrader) executeTrappedReduceSide(quantity float64, side string) error {
	if side == "sell" {
		return at.executeTrappedReduceShort(quantity)
	}
	return at.executeTrappedReduce(quantity)
}

// executeTrappedReduceShort executes a batch reduction of trapped SHORT positions.
// Called after a short T-trade sell prep order fills.
func (at *AutoTrader) executeTrappedReduceShort(quantity float64) error {
	gridConfig := at.config.StrategyConfig.GridConfig

	currentPrice, err := at.trader.GetMarketPrice(gridConfig.Symbol)
	if err != nil {
		return fmt.Errorf("failed to get market price for short trapped reduce: %w", err)
	}

	at.gridState.mu.RLock()
	tTradeSellPrice := at.gridState.TTradePrepPrice
	at.gridState.mu.RUnlock()

	exchangePositions, err := at.trader.GetPositions()
	if err != nil {
		return fmt.Errorf("failed to get positions: %w", err)
	}

	var shortSize float64
	for _, pos := range exchangePositions {
		symbol, _ := pos["symbol"].(string)
		if symbol != gridConfig.Symbol {
			continue
		}
		side, _ := pos["side"].(string)
		if side != "short" {
			continue
		}
		shortSize, _ = pos["positionAmt"].(float64)
	}

	if shortSize <= 0 {
		logger.Infof("[Grid] Short trapped reduce: no short position found")
		return nil
	}

	closeQty := quantity
	if closeQty <= 0 {
		closeQty = shortSize
	}

	// Buy lower than the T-trade sell price to capture the spread
	var reducePrice float64
	if tTradeSellPrice > 0 {
		reducePrice = tTradeSellPrice * (1 - 0.005) // 0.5% below sell prep price
	} else {
		reducePrice = currentPrice * 0.998
	}

	logger.Infof("[Grid] Placing short trapped reduce: qty=%.4f price=%.2f (T-trade sell=%.2f)",
		closeQty, reducePrice, tTradeSellPrice)

	gridTrader, ok := at.trader.(GridTrader)
	if !ok {
		gridTrader = NewGridTraderAdapter(at.trader)
	}
	result, err := gridTrader.PlaceLimitOrder(&LimitOrderRequest{
		Symbol:     gridConfig.Symbol,
		Side:       "BUY",
		Quantity:   closeQty,
		Price:      reducePrice,
		Leverage:   gridConfig.Leverage,
		ReduceOnly: true,
	})
	if err != nil {
		return fmt.Errorf("short trapped reduce order failed: %w", err)
	}
	at.gridState.mu.Lock()
	if result != nil {
		at.gridState.TTradeReduceOrderID = result.OrderID
		at.gridState.TTradeReducePlacedAt = time.Now()
	}
	at.gridState.LastTrappedReduceAt = time.Now()
	at.gridState.TrappedReduceCount++
	at.gridState.mu.Unlock()
	at.refreshTotalInvestment()
	logger.Infof("[Grid] ✅ Short trapped reduce placed: %.4f @ %.2f", closeQty, reducePrice)
	return nil
}

// executeTrappedReduce executes a batch reduction of trapped positions
// quantity: total contracts to close (0 = use suggested batch %)
func (at *AutoTrader) executeTrappedReduce(quantity float64) error {
	gridConfig := at.config.StrategyConfig.GridConfig

	currentPrice, err := at.trader.GetMarketPrice(gridConfig.Symbol)
	if err != nil {
		return fmt.Errorf("failed to get market price for trapped reduce: %w", err)
	}


	// Collect filled long levels sorted by loss (worst first)
	type levelLoss struct {
		index   int
		size    float64
		entry   float64
		lossAmt float64
	}
	var losses []levelLoss

	at.gridState.mu.RLock()
	for i, level := range at.gridState.Levels {
		if level.State != "filled" || level.PositionEntry <= 0 || level.PositionSize <= 0 {
			continue
		}
		var loss float64
		if level.Side == "buy" {
			loss = (currentPrice - level.PositionEntry) * level.PositionSize
		} else {
			loss = (level.PositionEntry - currentPrice) * level.PositionSize
		}
		if loss < 0 {
			losses = append(losses, levelLoss{
				index:   i,
				size:    level.PositionSize,
				entry:   level.PositionEntry,
				lossAmt: loss,
			})
		}
	}
	at.gridState.mu.RUnlock()

	if len(losses) == 0 {
		// Grid Levels have no trapped positions - fall back to exchange positions directly
		logger.Infof("[Grid] Trapped reduce: no trapped grid levels found, checking exchange positions...")
		exchangePositions, posErr := at.trader.GetPositions()
		if posErr != nil {
			logger.Errorf("[Grid] Trapped reduce: failed to get exchange positions: %v", posErr)
			return nil
		}

		// Find the most-trapped side (largest loss)
		mostLosingSide := ""
		mostLoss := 0.0
		mostLossSize := 0.0
		mostLossEntry := 0.0
		mostLossPnL := 0.0
		for _, pos := range exchangePositions {
			symbol, _ := pos["symbol"].(string)
			if symbol != gridConfig.Symbol {
				continue
			}
			side, _ := pos["side"].(string)
			posSize, _ := pos["positionAmt"].(float64)
			entryPrice, _ := pos["entryPrice"].(float64)
			pnl, _ := pos["unRealizedProfit"].(float64)
			if pnl == 0 {
				pnl, _ = pos["unrealized_pnl"].(float64)
			}
			if pnl < mostLoss && posSize > 0 {
				mostLoss = pnl
				mostLosingSide = side
				mostLossSize = posSize
				mostLossEntry = entryPrice
				mostLossPnL = pnl
			}
		}

		if mostLosingSide == "" || mostLossSize <= 0 {
			logger.Infof("[Grid] Trapped reduce: no losing exchange positions found")
			return nil
		}

		closeQty := quantity
		if closeQty <= 0 {
			closeQty = mostLossSize
		}
		if closeQty > mostLossSize {
			closeQty = mostLossSize
		}

		logger.Infof("[Grid] Trapped reduce (exchange): closing %.4f of %s position (entry=%.2f, size=%.4f, pnl=%.2f)",
			closeQty, mostLosingSide, mostLossEntry, mostLossSize, mostLossPnL)

		// Calculate limit price for reduce order based on T-trade buy price
		at.gridState.mu.RLock()
		tTradeBuyPrice := at.gridState.TTradePrepPrice
		at.gridState.mu.RUnlock()

		var reducePrice float64
		if tTradeBuyPrice > 0 {
			// Use T-trade buy price with spread
			spreadPct := 0.5 // 0.5% spread for profit
			if mostLosingSide == "long" {
				// Long reduce: sell higher than T-trade buy price
				reducePrice = tTradeBuyPrice * (1 + spreadPct/100)
			} else {
				// Short reduce: buy lower than T-trade sell price
				reducePrice = tTradeBuyPrice * (1 - spreadPct/100)
			}
		} else {
			// Fallback: use current price with small spread
			if mostLosingSide == "long" {
				reducePrice = currentPrice * 1.002
			} else {
				reducePrice = currentPrice * 0.998
			}
		}

		logger.Infof("[Grid] Placing limit reduce order: side=%s qty=%.4f price=%.2f (T-trade price=%.2f)",
			mostLosingSide, closeQty, reducePrice, tTradeBuyPrice)

		var closeErr error
		var reduceOrderResult *types.LimitOrderResult
		if gridTrader, ok := at.trader.(GridTrader); ok {
			var orderSide string
			if mostLosingSide == "long" {
				orderSide = "sell"
			} else {
				orderSide = "buy"
			}
			reduceOrderResult, closeErr = gridTrader.PlaceLimitOrder(&types.LimitOrderRequest{
				Symbol:   gridConfig.Symbol,
				Side:     orderSide,
				Quantity: closeQty,
				Price:    reducePrice,
				Leverage: gridConfig.Leverage,
			})
		} else {
			return fmt.Errorf("trader does not support limit orders")
		}
		if closeErr != nil {
			logger.Errorf("[Grid] Trapped reduce (exchange): failed to close %s position: %v", mostLosingSide, closeErr)
			return closeErr
		}

		// Save reduce order ID for protection
		reduceOrderID := ""
		if reduceOrderResult != nil {
			reduceOrderID = reduceOrderResult.OrderID
		}

		at.gridState.mu.Lock()
		at.gridState.TTradeReduceOrderID = reduceOrderID
		at.gridState.TTradeReducePlacedAt = time.Now()
		at.gridState.LastTrappedReduceAt = time.Now()
		at.gridState.TrappedReduceCount++
		at.gridState.DailyPnL += mostLossPnL * (closeQty / mostLossSize)
		at.gridState.TotalProfit += mostLossPnL * (closeQty / mostLossSize)
		at.gridState.TotalTrades++
		at.gridState.mu.Unlock()
		at.refreshTotalInvestment()
		logger.Infof("[Grid] ✅ Trapped reduce (exchange): closed %.4f %s contracts (entry=%.2f, current=%.2f)",
			closeQty, mostLosingSide, mostLossEntry, currentPrice)
		return nil
	}

	// Sort by loss amount (most negative first = worst loss)
	for i := 0; i < len(losses)-1; i++ {
		for j := i + 1; j < len(losses); j++ {
			if losses[j].lossAmt < losses[i].lossAmt {
				losses[i], losses[j] = losses[j], losses[i]
			}
		}
	}

	// If quantity not specified, use full trapped position size
	if quantity <= 0 {
		totalSize := 0.0
		for _, l := range losses {
			totalSize += l.size
		}
		quantity = totalSize
	}

	// Close positions worst-first until quantity is fulfilled
	remaining := quantity
	closedTotal := 0.0
	for _, l := range losses {
		if remaining <= 0 {
			break
		}
		closeSize := l.size
		if closeSize > remaining {
			closeSize = remaining
		}

		// Determine side from the level (buy level = long position)
		at.gridState.mu.RLock()
		side := at.gridState.Levels[l.index].Side
		at.gridState.mu.RUnlock()

		var closeErr error
		// Calculate limit price for reduce order
		at.gridState.mu.RLock()
		tTradeBuyPrice := at.gridState.TTradePrepPrice
		at.gridState.mu.RUnlock()

		var reducePrice float64
		if tTradeBuyPrice > 0 {
			spreadPct := 0.5
			if side == "buy" {
				reducePrice = tTradeBuyPrice * (1 + spreadPct/100)
			} else {
				reducePrice = tTradeBuyPrice * (1 - spreadPct/100)
			}
		} else {
			if side == "buy" {
				reducePrice = currentPrice * 1.002
			} else {
				reducePrice = currentPrice * 0.998
			}
		}

		if gridTrader, ok := at.trader.(GridTrader); ok {
			var orderSide string
			if side == "buy" {
				orderSide = "sell"
			} else {
				orderSide = "buy"
			}
			_, closeErr = gridTrader.PlaceLimitOrder(&types.LimitOrderRequest{
				Symbol:   gridConfig.Symbol,
				Side:     orderSide,
				Quantity: closeSize,
				Price:    reducePrice,
				Leverage: gridConfig.Leverage,
			})
		} else {
			closeErr = fmt.Errorf("trader does not support limit orders")
		}

		if closeErr != nil {
			logger.Errorf("[Grid] Trapped reduce: failed to close level %d: %v", l.index, closeErr)
			continue
		}

		realizedLoss := (currentPrice - l.entry) * closeSize
		if side != "buy" {
			realizedLoss = (l.entry - currentPrice) * closeSize
		}

		at.gridState.mu.Lock()
		if closeSize >= at.gridState.Levels[l.index].PositionSize*0.99 {
			// Full close of this level
			at.gridState.Levels[l.index].State = "empty"
			at.gridState.Levels[l.index].PositionSize = 0
		} else {
			// Partial close
			at.gridState.Levels[l.index].PositionSize -= closeSize
		}
		at.gridState.DailyPnL += realizedLoss
		at.gridState.TotalProfit += realizedLoss
		at.gridState.TotalTrades++
		at.gridState.mu.Unlock()

		remaining -= closeSize
		closedTotal += closeSize
		logger.Infof("[Grid] Trapped reduce: closed %.4f of level %d (entry=%.2f, current=%.2f, loss=%.2f)",
			closeSize, l.index, l.entry, currentPrice, realizedLoss)
	}

	if closedTotal > 0 {
		at.gridState.mu.Lock()
		at.gridState.LastTrappedReduceAt = time.Now()
		at.gridState.TrappedReduceCount++
		at.gridState.mu.Unlock()
		at.refreshTotalInvestment()
		logger.Infof("[Grid] ✅ Trapped reduce completed: closed %.4f contracts (reduce #%d)",
			closedTotal, at.gridState.TrappedReduceCount)
	}

	return nil
}

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
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ============================================================================
// Grid Trading State Management
// ============================================================================

// TTradePrepEntry tracks a single tagged grid order waiting to fill as a T-trade prep.
type TTradePrepEntry struct {
	OrderID           string
	Price             float64
	Qty               float64
	Side              string // "buy" or "sell"
	TaggedAt          time.Time
	ReduceQueued      bool // true once reduce has been dispatched (prevents double-execution)
	FillAlreadyLogged bool // true when restored after a crash: fill was already logged before restart
}

// TTradeReduceEntry tracks a placed reduce limit order resulting from a T-trade prep fill.
type TTradeReduceEntry struct {
	ReduceOrderID string
	PrepOrderID   string  // which prep order triggered this
	PrepFillPrice float64 // fill price of prep (spread is relative to this)
	ReducePrice   float64 // limit price the reduce was placed at
	SpreadPct     float64 // spread used when placing
	Qty           float64
	Side          string // "sell" (reduce_long) or "buy" (reduce_short)
	PrepSide      string // original prep side ("buy" or "sell")
	PlacedAt      time.Time
}

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

	// Trapped position reduction tracking (被套减仓追踪)
	LastTrappedReduceAt time.Time // time of last batch reduction
	TrappedReduceCount  int       // total number of batch reductions performed

	// T-trade state machine (T字操作状态机)
	// All qualifying grid orders on the trapped side are tagged simultaneously.
	// Each fills independently and auto-places its own reduce order.
	TTradePrepOrders   map[string]*TTradePrepEntry   // prep orders waiting to fill, keyed by order ID
	TTradeReduceOrders map[string]*TTradeReduceEntry // active reduce orders, keyed by reduce order ID
	TTradePrepSide     string                        // current trapped direction: "buy" or "sell"

	// Profit-based reduce tracking (per side)
	LongProfitReducedPct  float64 // cumulative % already reduced for long (multiples of 10)
	ShortProfitReducedPct float64 // cumulative % already reduced for short (multiples of 10)

	// ProfitReduceOrderIDs tracks reduce-only orders placed by checkProfitReduce
	// that are still (believed to be) open on the exchange, so cancelAllGridOrders
	// can skip them the same way it already skips T-trade reduce orders — without
	// this, a resetGrid/investment-refresh cycle would cancel them with no
	// mechanism to re-place the lost reduce intent (checkProfitReduce only
	// re-fires once profit reaches a *new* step, not on the same step again).
	ProfitReduceOrderIDs map[string]bool

	// Periodic investment refresh
	LastInvestmentRefreshAt time.Time
}

// NewGridState creates a new grid state
func NewGridState(config *store.GridStrategyConfig) *GridState {
	return &GridState{
		Config:               config,
		Levels:               make([]kernel.GridLevelInfo, 0),
		OrderBook:            make(map[string]int),
		TTradePrepOrders:     make(map[string]*TTradePrepEntry),
		TTradeReduceOrders:   make(map[string]*TTradeReduceEntry),
		ProfitReduceOrderIDs: make(map[string]bool),
	}
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

	// Use wallet balance for cross margin, committed margin for isolated margin
	balance, err := at.trader.GetBalance()
	if err != nil {
		logger.Warnf("[Grid] Failed to get balance for total investment, using config value: %v", err)
	} else {
		if inv := at.investmentFromBalance(balance); inv > 0 {
			logger.Infof("[Grid] Using %s investment: %.2f USDT (config was: %.2f)",
				map[bool]string{true: "cross-margin wallet", false: "isolated committed margin"}[at.config.IsCrossMargin],
				inv, gridConfig.TotalInvestment)
			gridConfig.TotalInvestment = inv
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

	// Restore profit-reduce progress from trade log to prevent re-triggering after restart.
	// Only restore if the position has NOT been closed since the last reduce
	// (a close event means the position was reset and tracker should start from 0).
	if at.store != nil && gridConfig.EnableProfitReduce {
		for _, side := range []string{"long", "short"} {
			reduceEntry, err := at.store.Grid().GetLatestGridTradeLogByAction(at.id, "profit_reduce", side)
			if err != nil || reduceEntry == nil {
				continue
			}
			// Check if a close or reset event happened AFTER the last reduce
			closeEntry, _ := at.store.Grid().GetLatestGridTradeLogByAction(at.id, "profit_reduce_close", side)
			drawdownEntry, _ := at.store.Grid().GetLatestGridTradeLogByAction(at.id, "profit_drawdown_close", side)
			resetEntry, _ := at.store.Grid().GetLatestGridTradeLogByAction(at.id, "profit_reduce_reset", side)
			closedAfter := false
			if closeEntry != nil && closeEntry.CreatedAt.After(reduceEntry.CreatedAt) {
				closedAfter = true
			}
			if drawdownEntry != nil && drawdownEntry.CreatedAt.After(reduceEntry.CreatedAt) {
				closedAfter = true
			}
			if resetEntry != nil && resetEntry.CreatedAt.After(reduceEntry.CreatedAt) {
				closedAfter = true
			}
			if closedAfter {
				logger.Infof("[Grid] Skipping %s profit-reduce restore: position was closed/reset after last reduce", side)
				continue
			}
			var pos, targetPct float64
			// reason format: "pos=X.XXXX target=YY% closeAll=false"
			fmt.Sscanf(reduceEntry.Reason, "pos=%f target=%f%%", &pos, &targetPct)
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

	// Restore T-trade state from trade log on restart.
	// Only restores orders that FILLED while the system was down but haven't had a reduce placed yet.
	// Pending orders are skipped — ttradeTagOrders re-tags them on the next cycle.
	// Cancelled/expired orders are skipped.
	if at.store != nil && gridConfig.EnableTrappedReduce {
		tagEntries, _ := at.store.Grid().GetGridTradeLogsByAction(at.id, "ttrade_tag", 50)
		if len(tagEntries) > 0 {
			openOrders, oErr := at.trader.GetOpenOrders(gridConfig.Symbol)
			openOrderMap := make(map[string]types.OpenOrder)
			if oErr == nil {
				for _, o := range openOrders {
					openOrderMap[o.OrderID] = o
				}
			}

			restored := 0
			for _, entry := range tagEntries {
				if entry.OrderID == "" {
					continue
				}
				// Only restore tags within the 3h T-trade window
				if time.Since(entry.CreatedAt) > 3*time.Hour {
					break // entries are newest-first, so all subsequent are older
				}
				// Skip if the full T-trade cycle already completed — reduce order filled
				reduceEntry, _ := at.store.Grid().GetGridTradeLogByActionAndOrderID(at.id, "ttrade_reduce", entry.OrderID)
				if reduceEntry != nil && reduceEntry.CreatedAt.After(entry.CreatedAt) {
					continue // reduce already filled, nothing to restore
				}
				// Skip if reduce was already placed (even if not yet filled) — restore to
				// TTradeReduceOrders so ttradeRepairOrders monitors the pending reduce.
				reducePlacedEntry, _ := at.store.Grid().GetGridTradeLogByActionAndOrderID(at.id, "ttrade_reduce_placed", entry.OrderID)
				if reducePlacedEntry != nil && reducePlacedEntry.CreatedAt.After(entry.CreatedAt) {
					reduceOrderID := reducePlacedEntry.RelatedOrderID
					if reduceOrderID != "" {
						prepSide := reducePlacedEntry.Side
						reduceSide := "sell"
						if prepSide == "sell" {
							reduceSide = "buy"
						}
						at.gridState.mu.Lock()
						at.gridState.TTradeReduceOrders[reduceOrderID] = &TTradeReduceEntry{
							ReduceOrderID: reduceOrderID,
							PrepOrderID:   entry.OrderID,
							PrepFillPrice: reducePlacedEntry.EntryPrice,
							ReducePrice:   reducePlacedEntry.Price,
							SpreadPct:     gridConfig.TTradeSpreadPct,
							Qty:           reducePlacedEntry.Quantity,
							Side:          reduceSide,
							PrepSide:      prepSide,
							PlacedAt:      reducePlacedEntry.CreatedAt,
						}
						at.gridState.mu.Unlock()
						logger.Infof("[Grid] Restored T-trade reduce order %s (prep=%s) into monitoring", reduceOrderID, entry.OrderID)
					}
					continue
				}
				// Also check if prep fill was logged
				fillEntry, _ := at.store.Grid().GetGridTradeLogByActionAndOrderID(at.id, "ttrade_fill", entry.OrderID)
				fillAlreadyLogged := fillEntry != nil && fillEntry.CreatedAt.After(entry.CreatedAt)

				// Skip orders that are still pending — ttradeTagOrders will re-tag them
				if _, stillOpen := openOrderMap[entry.OrderID]; stillOpen {
					continue
				}

				// Order not in open list — only restore if it actually FILLED while down
				statusMap, sErr := at.trader.GetOrderStatus(gridConfig.Symbol, entry.OrderID)
				if sErr != nil {
					continue
				}
				statusStr, _ := statusMap["status"].(string)
				if statusStr != "FILLED" {
					continue // cancelled/expired — don't restore
				}
				fillPrice := entry.Price
				if avg, ok := statusMap["avgPrice"].(float64); ok && avg > 0 {
					fillPrice = avg
				}
				side := entry.Side
				if side == "" {
					side = "sell"
				}
				at.gridState.mu.Lock()
				at.gridState.TTradePrepOrders[entry.OrderID] = &TTradePrepEntry{
					OrderID:           entry.OrderID,
					Price:             fillPrice,
					Qty:               entry.Quantity,
					Side:              side,
					TaggedAt:          entry.CreatedAt,
					FillAlreadyLogged: fillAlreadyLogged,
				}
				at.gridState.mu.Unlock()
				logger.Infof("[Grid] Restored T-trade filled prep: order %s @ %.4f — reduce will be placed next cycle", entry.OrderID, fillPrice)
				restored++
			}
			if restored > 0 {
				logger.Infof("[Grid] T-trade recovery: restored %d prep orders", restored)
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

	// Start WebSocket if the exchange supports it — provides live-push caches for
	// balance, positions, and market price instead of REST polling each cycle.
	type wsStarter interface {
		StartWS(symbol string, primaryTf string) error
	}
	type wsCallbackSetter interface {
		SetWSCallbacks(onPosition func([]map[string]interface{}), onOrder, onKlineClose func())
	}
	triggerTf := gridConfig.AITriggerTf
	if triggerTf == "" {
		triggerTf = "5m"
	}
	if starter, ok := at.trader.(wsStarter); ok {
		if err := starter.StartWS(gridConfig.Symbol, triggerTf); err != nil {
			logger.Warnf("[Grid] OKX WS start failed (falling back to REST): %v", err)
		} else {
			logger.Infof("[Grid] OKX WS started for %s", gridConfig.Symbol)

			// Wire WS push events to the appropriate channels
			if setter, ok := at.trader.(wsCallbackSetter); ok {
				notifyPosition := func(positions []map[string]interface{}) {
					if at.wsPosUpdateCh != nil {
						select {
						case at.wsPosUpdateCh <- positions:
						default:
						}
					}
				}
				notifyScan := func() {
					if at.wsScanCh != nil {
						select {
						case at.wsScanCh <- struct{}{}:
						default:
						}
					}
					// Notify SSE subscribers (order markers / price lines on dashboard chart)
					if at.OnOrderUpdate != nil {
						at.OnOrderUpdate()
					}
				}
				notifyGridCycle := func() {
					if at.wsGridCycleCh != nil {
						atomic.StoreInt64(&at.wsLastKlineClose, time.Now().UnixNano())
						select {
						case at.wsGridCycleCh <- struct{}{}:
						default:
						}
					}
				}
				setter.SetWSCallbacks(notifyPosition, notifyScan, notifyGridCycle)
				logger.Infof("[Grid] event-driven mode: AI cycle on %s kline close", triggerTf)
			}
		}
	}

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

	// Check if grid is paused
	at.gridState.mu.RLock()
	isPaused := at.gridState.IsPaused
	at.gridState.mu.RUnlock()
	gridConfig := at.config.StrategyConfig.GridConfig
	lang := at.config.StrategyConfig.Language
	if lang == "" {
		lang = "en"
	}

	// Breakout detection is disabled, but refresh box data each cycle for the risk panel.
	if box, err := market.GetBoxData(gridConfig.Symbol); err == nil {
		at.gridState.mu.Lock()
		at.gridState.ShortBoxUpper = box.ShortUpper
		at.gridState.ShortBoxLower = box.ShortLower
		at.gridState.MidBoxUpper = box.MidUpper
		at.gridState.MidBoxLower = box.MidLower
		at.gridState.LongBoxUpper = box.LongUpper
		at.gridState.LongBoxLower = box.LongLower
		at.gridState.mu.Unlock()
	}

	// Fetch open orders once per cycle — shared by T-trade checks, state sync, and AI context
	openOrders, err := at.trader.GetOpenOrders(gridConfig.Symbol)
	if err != nil {
		logger.Warnf("[Grid] Failed to get open orders: %v", err)
		openOrders = nil
	}

	// Sync open orders from exchange FIRST so level states are up-to-date
	// before T-trade fill detection runs
	at.syncExchangeState(openOrders, false)

	// T-trade and profit-reduce run regardless of pause state — system-level operations
	at.RunTTradeScan(openOrders)

	if at.config.StrategyConfig.GridConfig.EnableProfitReduce {
		at.checkProfitReduce(nil)
	}

	if gridConfig.EnableInvestmentRefresh {
		at.checkInvestmentRefresh()
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
	const maxOrdersPerCycle = 8
	const maxCancelsPerCycle = 3
	orderCount := 0
	cancelCount := 0
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

		isOrderAction := d.Action == "place_buy_limit" || d.Action == "place_sell_limit"

		// Skip order placement if grid is paused
		if isOrderAction && isPaused {
			logger.Infof("[Grid] Skipping %s: grid is paused", d.Action)
			continue
		}

		// Skip order placement if available balance is too low to avoid cascading exchange errors
		if isOrderAction && gridCtx.AvailableBalance < 1.0 {
			logger.Warnf("[Grid] Skipping %s: available balance $%.2f insufficient", d.Action, gridCtx.AvailableBalance)
			break
		}

		// Cap order placements per cycle to avoid rate limits and runaway AI decisions
		if isOrderAction && orderCount >= maxOrdersPerCycle {
			logger.Warnf("[Grid] Skipping remaining order decisions: hit per-cycle limit (%d)", maxOrdersPerCycle)
			break
		}

		// Cap cancel_order per cycle
		if d.Action == "cancel_order" && cancelCount >= maxCancelsPerCycle {
			logger.Warnf("[Grid] Skipping cancel_order: hit per-cycle cancel limit (%d)", maxCancelsPerCycle)
			continue
		}

		err := at.executeGridDecision(&d, gridCtx)
		if err != nil {
			logger.Warnf("[Grid] Failed to execute decision %s: %v", d.Action, err)
		}
		results = append(results, decisionResult{d: d, err: err})
		if isOrderAction {
			orderCount++
		}
		if d.Action == "cancel_order" {
			cancelCount++
		}
	}

	// Update decision memory
	at.gridState.mu.Lock()
	for _, r := range results {
		// Skip hold unless it has meaningful reasoning (e.g. zero-balance analysis)
		if r.d.Action == "hold" && len(r.d.Reasoning) < 20 {
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
	at.syncExchangeState(nil, true)

	// After AI places new orders, re-run T-trade tagging so new orders can be picked up.
	if gridConfig.EnableTrappedReduce {
		hasNewOrder := false
		for _, r := range results {
			if r.err == nil && (r.d.Action == "place_buy_limit" || r.d.Action == "place_sell_limit") {
				hasNewOrder = true
				break
			}
		}
		if hasNewOrder {
			freshOrders, err := at.trader.GetOpenOrders(gridConfig.Symbol)
			if err == nil {
				at.syncExchangeState(freshOrders, false)
				at.ttradeTagOrders(freshOrders)

				// Check if any newly placed order filled immediately (taker fill).
				// Such orders never appear in freshOrders, so ttradeTagOrders skips them.
				freshOrderIDs := make(map[string]bool, len(freshOrders))
				for _, o := range freshOrders {
					freshOrderIDs[o.OrderID] = true
				}
				// Get current price — prefer WS cache
				if currentPrice, priceErr := at.trader.GetMarketPrice(gridConfig.Symbol); priceErr == nil && currentPrice > 0 {
					longInfo, shortInfo, tErr := at.buildTTradeContext(currentPrice)
					if tErr == nil && (longInfo.Active || shortInfo.Active) {
						at.gridState.mu.RLock()
						for _, r := range results {
							if r.err != nil {
								continue
							}
							if r.d.Action != "place_buy_limit" && r.d.Action != "place_sell_limit" {
								continue
							}
							if r.d.LevelIndex < 0 || r.d.LevelIndex >= len(at.gridState.Levels) {
								continue
							}
							orderID := at.gridState.Levels[r.d.LevelIndex].OrderID
							if orderID == "" || freshOrderIDs[orderID] {
								continue
							}
							orderSide := "buy"
							if r.d.Action == "place_sell_limit" {
								orderSide = "sell"
							}
							qualifies := (longInfo.Active && orderSide == "buy") ||
								(shortInfo.Active && orderSide == "sell")
							if !qualifies {
								continue
							}
							qty := at.gridState.Levels[r.d.LevelIndex].OrderQuantity
							at.gridState.mu.RUnlock()
							statusMap, sErr := at.trader.GetOrderStatus(gridConfig.Symbol, orderID)
							if sErr == nil {
								statusStr, _ := statusMap["status"].(string)
								if statusStr == "FILLED" {
									fillPrice := r.d.Price
									if avg, ok := statusMap["avgPrice"].(float64); ok && avg > 0 {
										fillPrice = avg
									}
									// Use actual executed quantity, not the planned level quantity
									if execQty, ok := statusMap["executedQty"].(float64); ok && execQty > 0 {
										qty = execQty
									}
									logger.Infof("[Grid] 🏷 T-trade immediate fill detected: %s %s @ %.4f — placing reduce", orderID, orderSide, fillPrice)
									at.logGridTrade("ttrade", "ttrade_fill", orderSide, gridConfig.Symbol,
										fmt.Sprintf("prep %s filled @ %.4f (immediate taker fill)", orderID, fillPrice),
										orderID, qty, fillPrice, 0, 0, 0, 0, true, "")
									// Add to TTradePrepOrders as fallback so ttradeProcessFills
									// can re-place the reduce on the next cycle if this goroutine fails.
									// ReduceQueued=true prevents double-placement if goroutine succeeds first.
									// Also mark level as filled so syncExchangeState late-detect doesn't
									// re-fire and dispatch a duplicate reduce.
									at.gridState.mu.Lock()
									at.gridState.Levels[r.d.LevelIndex].State = "filled"
									at.gridState.TTradePrepOrders[orderID] = &TTradePrepEntry{
										OrderID:           orderID,
										Price:             fillPrice,
										Qty:               qty,
										Side:              orderSide,
										TaggedAt:          time.Now(),
										FillAlreadyLogged: true,
										ReduceQueued:      true,
									}
									at.gridState.mu.Unlock()
									go func(side string, fp float64, q float64, prepID string) {
										ok := at.placeTTradeReduceOrder(side, fp, q, prepID)
										at.gridState.mu.Lock()
										if ok {
											// Success — remove prep so ttradeProcessFills doesn't retry
											delete(at.gridState.TTradePrepOrders, prepID)
										} else {
											// Failed — clear ReduceQueued so ttradeProcessFills retries next cycle
											if p, exists := at.gridState.TTradePrepOrders[prepID]; exists {
												p.ReduceQueued = false
											}
										}
										at.gridState.mu.Unlock()
									}(orderSide, fillPrice, qty, orderID)
								}
							}
							at.gridState.mu.RLock()
						}
						at.gridState.mu.RUnlock()
					}
				}
			}
		}
	}

	// Save decision record
	at.saveGridDecisionRecord(decision)

	return nil
}

// getMinOrderSize returns the minimum order size for the grid symbol via the exchange.
// Returns 0 if unavailable (non-OKX exchanges or instrument fetch failure).
func (at *AutoTrader) getMinOrderSize(symbol string) (float64, error) {
	type minSizer interface {
		GetMinOrderSize(symbol string) (float64, error)
	}
	if ms, ok := at.trader.(minSizer); ok {
		return ms.GetMinOrderSize(symbol)
	}
	return 0, nil
}

// checkProfitReduce checks per-side unrealized profit and reduces position accordingly:
// - Every ProfitReduceStepPct increment → reduce that % of current position
// - If profit > step*1.2 AND position value < 100 USD → close entire side
// positions may be passed directly from a WS push; if nil, fetched via GetPositions.
func (at *AutoTrader) checkProfitReduce(positions []map[string]interface{}) {
	gridConfig := at.config.StrategyConfig.GridConfig
	symbol := gridConfig.Symbol
	step := gridConfig.ProfitReduceStepPct
	if step <= 0 {
		step = 10.0
	}

	if positions == nil {
		var err error
		positions, err = at.trader.GetPositions()
		if err != nil {
			return
		}
	}

	type sideInfo struct {
		size             float64
		entryPrice       float64
		markPrice        float64
		unrealizedProfit float64
		uplRatio         float64 // exchange-provided ratio (upl/margin); 0 if unavailable
		side             string
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
		uplRatio, _ := pos["uplRatio"].(float64)
		sides[posSide] = &sideInfo{size: size, entryPrice: entry, markPrice: mark, unrealizedProfit: upl, uplRatio: uplRatio, side: posSide}
	}

	// Profit tracker resets happen only when profitPct <= 0 (below), or via manual profit_reduce_reset event.
	// Do NOT reset here based on position absence — a t-trade or brief API lag can temporarily make a
	// position disappear, which would spuriously clear alreadyReduced and allow duplicate reduces.

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
		if info.entryPrice == 0 {
			continue
		}
		// Use exchange-provided uplRatio when available (already upl/margin).
		// Fall back to manual calculation if not provided.
		var profitPct float64
		if info.uplRatio != 0 {
			profitPct = info.uplRatio * 100
		} else {
			margin := info.size * info.entryPrice / float64(gridConfig.Leverage)
			if margin == 0 {
				continue
			}
			profitPct = info.unrealizedProfit / margin * 100
		}
		logger.Debugf("[Grid] Profit-reduce check: %s entry=%.4f mark=%.4f upl=%.2f uplRatio=%.4f profit=%.2f%%",
			info.side, info.entryPrice, info.markPrice, info.unrealizedProfit, info.uplRatio, profitPct)

		if profitPct <= 0 {
			actions = append(actions, reduceAction{info: *info, qty: 0, closeAll: false, targetReducePct: -1})
			continue
		}

		positionValue := info.size * info.markPrice
		if gridConfig.EnableSmallPositionClose && profitPct > step*2 && positionValue < 100 {
			actions = append(actions, reduceAction{info: *info, qty: info.size, closeAll: true})
			continue
		}

		alreadyReduced := at.gridState.LongProfitReducedPct
		if info.side == "short" {
			alreadyReduced = at.gridState.ShortProfitReducedPct
		}
		targetReducePct := math.Floor(profitPct/step) * step

		// Debug logging to track state
		logger.Debugf("[Grid] Profit-reduce %s: profitPct=%.2f%% targetStep=%.0f%% alreadyReduced=%.0f%% step=%.0f%%",
			info.side, profitPct, targetReducePct, alreadyReduced, step)

		// Bug fix: Prevent multiple triggers at same step level
		// We should only trigger once per step. If alreadyReduced is already at this step or higher,
		// we should skip. This prevents the scenario where:
		// 1. Profit is 18.6%, triggers at 18% step, reduces position
		// 2. Profit rises to 21.4% (still within 18-24% range)
		// 3. Without this check, it would trigger again at 18% step
		// The correct behavior: only trigger again when profit reaches next step (24%)
		if targetReducePct <= alreadyReduced {
			logger.Debugf("[Grid] Profit-reduce %s: skipping — already reduced at %.0f%% (current target %.0f%%)",
				info.side, alreadyReduced, targetReducePct)
			continue
		}
		// Reduce qty = position × (step_number × step%) × multiplier
		// e.g. at step 3 with step=6%: 75.1 × 18% × 0.1 = 1.3518
		multiplier := gridConfig.ProfitReduceMultiplier
		if multiplier <= 0 {
			multiplier = 1.0
		}
		reduceQty := info.size * (targetReducePct / 100) * multiplier
		if reduceQty > info.size {
			reduceQty = info.size
		}
		// Enforce exchange minimum lot size — skip if calculated qty is too small to place
		if minSize, err := at.getMinOrderSize(gridConfig.Symbol); err == nil && minSize > 0 && reduceQty < minSize {
			logger.Infof("[Grid] Profit-reduce %s: calculated qty %.4f below min size %.4f — skipping",
				info.side, reduceQty, minSize)
			continue
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
			// Reset tracker and floor — only log if it was actually non-zero
			at.gridState.mu.Lock()
			var prev float64
			if info.side == "long" {
				prev = at.gridState.LongProfitReducedPct
				at.gridState.LongProfitReducedPct = 0
			} else {
				prev = at.gridState.ShortProfitReducedPct
				at.gridState.ShortProfitReducedPct = 0
			}
			at.gridState.mu.Unlock()
			if prev > 0 {
				at.logGridTrade("profit_reduce", "profit_reduce_reset", info.side, symbol,
					"profit went to zero/negative — reset tracker", "", 0, 0, 0, 0, 0, 0, true, "")
			}
			continue
		}

		orderSide := "SELL"
		posSide := "LONG"
		if info.side == "short" {
			orderSide = "BUY"
			posSide = "SHORT"
		}

		// Check if there's already an existing reduce order for this side to prevent duplicate orders.
		// Bug fix: Profit reduce can trigger multiple times in short succession if the reduce order
		// doesn't fill immediately. We check for any pending reduce-only order on the same side
		// to avoid placing duplicate orders that would over-reduce the position.
		//
		// Since OpenOrder doesn't have ReduceOnly field, we infer it from:
		// 1. Order direction opposite to position (LONG position → SELL order, SHORT → BUY)
		// 2. Order price near mark price (reduce orders are placed at mark price)
		openOrders, err := at.trader.GetOpenOrders(symbol)
		if err == nil {
			hasPendingReduce := false
			at.gridState.mu.RLock()
			ttradeReduceIDs := make(map[string]bool, len(at.gridState.TTradeReduceOrders))
			for id := range at.gridState.TTradeReduceOrders {
				ttradeReduceIDs[id] = true
			}
			gridLevelOrderIDs := make(map[string]bool, len(at.gridState.Levels))
			for _, level := range at.gridState.Levels {
				if level.OrderID != "" {
					gridLevelOrderIDs[level.OrderID] = true
				}
			}
			at.gridState.mu.RUnlock()
			for _, order := range openOrders {
				if ttradeReduceIDs[order.OrderID] {
					continue // T-trade reduce order — not a profit-reduce
				}
				if gridLevelOrderIDs[order.OrderID] {
					continue // Grid level order placed by AI — not a profit-reduce
				}
				// Check if this order is likely a reduce order based on direction and price
				// For long position: reduce orders are SELL
				// For short position: reduce orders are BUY
				isReduceDirection := (info.side == "long" && order.Side == "SELL") ||
					(info.side == "short" && order.Side == "BUY")

				if isReduceDirection {
					// Check if price is close to mark price (within 1%)
					// Reduce orders are typically placed at or near mark price
					priceDiff := math.Abs(order.Price-info.markPrice) / info.markPrice
					if priceDiff < 0.01 {
						logger.Debugf("[Grid] Profit-reduce: skipping %s reduce — order %s already exists (%.4f @ %.4f)",
							info.side, order.OrderID, order.Quantity, order.Price)
						hasPendingReduce = true
						break
					}
				}
			}
			if hasPendingReduce {
				continue
			}
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
			at.gridState.mu.Lock()
			at.gridState.ProfitReduceOrderIDs[orderID] = true
			at.gridState.mu.Unlock()
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
			fmt.Sprintf("pos=%.4f target=%.0f%% closeAll=%v", info.size, a.targetReducePct, a.closeAll),
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
			logger.Infof("[Grid] Profit-reduce %s: state updated to 0%% (closeAll)", info.side)
		} else {
			newPct := a.targetReducePct
			oldPct := at.gridState.LongProfitReducedPct
			if info.side == "short" {
				oldPct = at.gridState.ShortProfitReducedPct
			}
			if info.side == "long" {
				at.gridState.LongProfitReducedPct = newPct
			} else {
				at.gridState.ShortProfitReducedPct = newPct
			}
			logger.Infof("[Grid] Profit-reduce %s: state updated from %.0f%% to %.0f%%",
				info.side, oldPct, newPct)
		}
		at.gridState.mu.Unlock()
	}
}

// ResetProfitTracker manually resets the profit-reduce tracker for a side.
// Called via API; writes a log entry so restart recovery skips restore.
func (at *AutoTrader) ResetProfitTracker(side string) {
	if at.gridState == nil {
		return
	}
	gridConfig := at.config.StrategyConfig.GridConfig
	at.gridState.mu.Lock()
	if side == "long" {
		at.gridState.LongProfitReducedPct = 0
	} else {
		at.gridState.ShortProfitReducedPct = 0
	}
	at.gridState.mu.Unlock()
	logger.Infof("[Grid] Profit-reduce tracker manually reset: %s", side)
	at.logGridTrade("profit_reduce", "profit_reduce_reset", side,
		gridConfig.Symbol, "manual reset via dashboard", "", 0, 0, 0, 0, 0, 0, true, "")
}

// buildGridContext builds the context for AI grid decisions
func (at *AutoTrader) buildGridContext() (*kernel.GridContext, error) {
	gridConfig := at.config.StrategyConfig.GridConfig

	// Get market data — prefer WS kline cache to avoid REST polling each cycle
	primaryTf := gridConfig.AITriggerTf
	if primaryTf == "" {
		primaryTf = "5m"
	}
	type wsKlineProvider interface {
		GetWSKlines(symbol, tf string) ([]market.Kline, bool)
	}
	var mktData *market.Data
	var err error
	if provider, ok := at.trader.(wsKlineProvider); ok {
		klinesP, okP := provider.GetWSKlines(gridConfig.Symbol, primaryTf)
		klines4h, ok4h := provider.GetWSKlines(gridConfig.Symbol, "4h")
		if okP && ok4h && len(klinesP) >= 50 && len(klines4h) >= 50 {
			mktData, err = market.BuildFromKlines(
				map[string][]market.Kline{primaryTf: klinesP, "4h": klines4h},
				primaryTf, 50, gridConfig.Symbol)
		}
	}
	if mktData == nil {
		mktData, err = market.GetWithTimeframes(gridConfig.Symbol, []string{primaryTf, "4h"}, primaryTf, 50)
	}
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

	// Populate distance-to-price and recalculate side for empty levels based on current price.
	// Level.Side is set at initialization and becomes stale as price moves; empty levels must
	// reflect the current price to prevent the AI from skipping levels that crossed sides.
	if ctx.CurrentPrice > 0 {
		for i := range ctx.Levels {
			ctx.Levels[i].DistancePct = (ctx.Levels[i].Price - ctx.CurrentPrice) / ctx.CurrentPrice * 100
			if ctx.Levels[i].State == "empty" {
				if ctx.Levels[i].Price > ctx.CurrentPrice {
					ctx.Levels[i].Side = "sell"
				} else {
					ctx.Levels[i].Side = "buy"
				}
			}
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

	// Populate protected T-trade order IDs — only reduce orders are protected from AI
	// cancellation. Falls back to DB when memory is empty (post-restart) so protection
	// doesn't silently vanish after a deploy/restart.
	for id := range at.activeTTradeReduceOrderIDs() {
		ctx.TTradeProtectedOrderIDs = append(ctx.TTradeProtectedOrderIDs, id)
	}

	return ctx, nil
}

// executeGridDecision executes a single grid decision
func (at *AutoTrader) executeGridDecision(d *kernel.Decision, ctx *kernel.GridContext) error {
	// Normalize hallucinated action prefixes (e.g. "place_place_buy_limit" → "place_buy_limit")
	for _, canonical := range []string{"place_buy_limit", "place_sell_limit", "cancel_order", "cancel_all_orders", "reduce_long", "reduce_short", "hold"} {
		if d.Action != canonical && strings.HasSuffix(d.Action, canonical) {
			d.Action = canonical
		}
	}
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
		logger.Warnf("[Grid] AI requested pause_grid — ignored (use system breakout mechanism)")
		return nil
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
		// Block if T-trade is active — reduces are auto-placed when prep orders fill
		at.gridState.mu.RLock()
		tTradeActive := len(at.gridState.TTradePrepOrders) > 0 || len(at.gridState.TTradeReduceOrders) > 0
		at.gridState.mu.RUnlock()
		if tTradeActive {
			logger.Infof("[Grid] reduce_long skipped: T-trade is active, reduces are auto-placed")
			return nil
		}
		return fmt.Errorf("reduce_long not available: T-trade is not active")
	case "reduce_short":
		// Block if T-trade is active — reduces are auto-placed when prep orders fill
		at.gridState.mu.RLock()
		tTradeActive2 := len(at.gridState.TTradePrepOrders) > 0 || len(at.gridState.TTradeReduceOrders) > 0
		at.gridState.mu.RUnlock()
		if tTradeActive2 {
			logger.Infof("[Grid] reduce_short skipped: T-trade is active, reduces are auto-placed")
			return nil
		}
		return fmt.Errorf("reduce_short not available: T-trade is not active")
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
	inv := at.investmentFromBalance(bal)
	if inv > 0 {
		old := gridConfig.TotalInvestment
		gridConfig.TotalInvestment = inv
		logger.Infof("[Grid] Refreshed total investment after close: %.2f -> %.2f USDT", old, inv)
	}
}

// investmentFromBalance extracts the effective total investment from a balance map.
// Cross margin: totalWalletBalance (entire account equity excl. unrealized PnL).
// Isolated margin: totalEquity (committed margin + unrealized PnL + free balance).
func (at *AutoTrader) investmentFromBalance(bal map[string]interface{}) float64 {
	if at.config.IsCrossMargin {
		if w, ok := bal["totalWalletBalance"].(float64); ok && w > 0 {
			return w
		}
		return 0
	}
	// Isolated: use total equity (includes free balance + committed margin + unrealized PnL)
	if equity, ok := bal["totalEquity"].(float64); ok && equity > 0 {
		return equity
	}
	return 0
}

// checkInvestmentRefresh refreshes TotalInvestment from wallet balance on a configurable interval.
func (at *AutoTrader) checkInvestmentRefresh() {
	gridConfig := at.config.StrategyConfig.GridConfig
	days := gridConfig.InvestmentRefreshDays
	if days <= 0 {
		days = 2
	}
	interval := time.Duration(days) * 24 * time.Hour

	at.gridState.mu.RLock()
	last := at.gridState.LastInvestmentRefreshAt
	at.gridState.mu.RUnlock()

	if !last.IsZero() && time.Since(last) < interval {
		return
	}

	bal, err := at.trader.GetBalance()
	if err != nil {
		logger.Warnf("[Grid] Investment refresh: failed to get balance: %v", err)
		// Reset timer so the outage period doesn't count toward the refresh interval
		at.gridState.mu.Lock()
		at.gridState.LastInvestmentRefreshAt = time.Now()
		at.gridState.mu.Unlock()
		return
	}
	inv := at.investmentFromBalance(bal)
	if inv <= 0 {
		return
	}
	old := gridConfig.TotalInvestment
	gridConfig.TotalInvestment = inv

	at.gridState.mu.Lock()
	at.gridState.LastInvestmentRefreshAt = time.Now()
	at.gridState.mu.Unlock()

	logger.Infof("[Grid] Periodic investment refresh: %.2f -> %.2f USDT (interval=%dd)", old, inv, days)

	// Skip grid reset if T-trade is in progress
	at.gridState.mu.RLock()
	hasPendingTTrade := len(at.gridState.TTradePrepOrders) > 0 || len(at.gridState.TTradeReduceOrders) > 0
	at.gridState.mu.RUnlock()
	if hasPendingTTrade {
		logger.Infof("[Grid] Investment refresh: skipping grid reset — T-trade order in progress")
		return
	}

	currentPrice, err := at.trader.GetMarketPrice(gridConfig.Symbol)
	if err != nil {
		logger.Warnf("[Grid] Investment refresh: failed to get price for grid reset: %v", err)
		return
	}
	logger.Infof("[Grid] Investment refresh: resetting grid around $%.2f", currentPrice)
	at.resetGrid(currentPrice)
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

	// Dedup: skip if any pending grid level already has an order at this price.
	// Catches AI hallucinations that place two orders at the same price in one cycle.
	at.gridState.mu.RLock()
	for _, level := range at.gridState.Levels {
		if level.State == "pending" && level.OrderID != "" && math.Abs(level.Price-d.Price)/d.Price < 0.001 {
			at.gridState.mu.RUnlock()
			logger.Warnf("[Grid] Dedup: skipping %s @ %.4f — pending order %s already at this price",
				side, d.Price, level.OrderID)
			return nil
		}
	}
	at.gridState.mu.RUnlock()

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
		Leverage:     gridConfig.Leverage,
		PostOnly:     gridConfig.UseMakerOnly,
		ReduceOnly:   false,
		ClientID:     fmt.Sprintf("grid-%d-%d", d.LevelIndex, time.Now().UnixNano()%1000000),
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
		// Record the actual placed (validated/capped) quantity, not d.Quantity
		// (the AI's original request) — if the cap above reduced quantity, this
		// level's tracked OrderQuantity must match what's really resting on the
		// exchange. A stale/inflated OrderQuantity here corrupts every downstream
		// consumer that trusts it as fill-quantity ground truth (position-size
		// bookkeeping on fill, and T-trade's late-detect reduce-quantity fallback).
		at.gridState.Levels[d.LevelIndex].OrderQuantity = quantity
		at.gridState.Levels[d.LevelIndex].OrderPlacedAt = time.Now()
		at.gridState.OrderBook[result.OrderID] = d.LevelIndex
	}
	// T-trade tagging is handled at reduce_position interception time.
	// Normal buy orders are NOT tagged here to avoid false positives.
	at.gridState.mu.Unlock()

	logger.Infof("[Grid] Placed %s limit order at $%.2f, qty=%.4f (requested %.4f), level=%d, orderID=%s",
		side, d.Price, quantity, d.Quantity, d.LevelIndex, result.OrderID)

	return nil
}

// cancelGridOrder cancels a specific grid order
func (at *AutoTrader) cancelGridOrder(d *kernel.Decision) error {
	gridTrader, ok := at.trader.(GridTrader)
	if !ok {
		gridTrader = NewGridTraderAdapter(at.trader)
	}

	// Resolve order ID: prefer order_id from decision, then level_index lookup, then price match
	orderID := d.OrderID
	if orderID == "" && d.LevelIndex >= 0 {
		at.gridState.mu.RLock()
		if d.LevelIndex < len(at.gridState.Levels) {
			orderID = at.gridState.Levels[d.LevelIndex].OrderID
		}
		at.gridState.mu.RUnlock()
	}
	// Fallback: match by price when AI only provided price (legacy behavior)
	if orderID == "" && d.Price > 0 {
		at.gridState.mu.RLock()
		for _, level := range at.gridState.Levels {
			if level.State == "pending" && level.OrderID != "" &&
				math.Abs(level.Price-d.Price)/d.Price < 0.001 { // within 0.1%
				orderID = level.OrderID
				break
			}
		}
		at.gridState.mu.RUnlock()
	}
	if orderID == "" {
		return fmt.Errorf("cancel_order: no order ID found (level=%d price=%.2f)", d.LevelIndex, d.Price)
	}

	// Protect T-trade reduce orders — prep/tag orders can be cancelled by AI.
	// Falls back to DB when memory is empty (post-restart) so protection survives restarts.
	if at.activeTTradeReduceOrderIDs()[orderID] {
		logger.Warnf("[Grid] cancel_order blocked: order %s is a protected T-trade reduce order",
			orderID)
		return nil
	}

	// Protect profit-reduce orders the same way — cancelling one here leaves
	// no mechanism to re-place the lost reduce intent (checkProfitReduce only
	// re-fires once profit reaches a *new* step, not the same step again).
	if at.activeProfitReduceOrderIDs()[orderID] {
		logger.Warnf("[Grid] cancel_order blocked: order %s is a protected profit-reduce order",
			orderID)
		return nil
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

// activeTTradePrepOrderIDs returns prep (tag) order IDs still awaiting fill. Reads in-memory
// state first; if empty (e.g. right after a process restart, since this map is never
// persisted), falls back to reconstructing the active set from grid_trade_logs.
func (at *AutoTrader) activeTTradePrepOrderIDs() map[string]bool {
	at.gridState.mu.RLock()
	ids := make(map[string]bool, len(at.gridState.TTradePrepOrders))
	for id := range at.gridState.TTradePrepOrders {
		ids[id] = true
	}
	at.gridState.mu.RUnlock()

	if len(ids) == 0 && at.store != nil {
		since3h := time.Now().Add(-3 * time.Hour)
		// Active preps: ttrade_tag without ttrade_fill/ttrade_cancel
		if tagEntries, _ := at.store.Grid().GetGridTradeLogsByActionSince(at.id, "ttrade_tag", since3h); len(tagEntries) > 0 {
			for _, e := range tagEntries {
				if e.OrderID == "" {
					continue
				}
				if fill, _ := at.store.Grid().GetGridTradeLogByActionAndOrderID(at.id, "ttrade_fill", e.OrderID); fill != nil {
					continue
				}
				if cancel, _ := at.store.Grid().GetGridTradeLogByActionAndOrderID(at.id, "ttrade_cancel", e.OrderID); cancel != nil {
					continue
				}
				ids[e.OrderID] = true
			}
		}
		if len(ids) > 0 {
			logger.Infof("[Grid] activeTTradePrepOrderIDs: restored %d prep order IDs from DB (post-restart protection)", len(ids))
		}
	}
	return ids
}

// activeTTradeReduceOrderIDs returns reduce order IDs still awaiting fill. Reads in-memory
// state first; if empty (e.g. right after a process restart, since this map is never
// persisted), falls back to reconstructing the active set from grid_trade_logs.
func (at *AutoTrader) activeTTradeReduceOrderIDs() map[string]bool {
	at.gridState.mu.RLock()
	ids := make(map[string]bool, len(at.gridState.TTradeReduceOrders))
	for id := range at.gridState.TTradeReduceOrders {
		ids[id] = true
	}
	at.gridState.mu.RUnlock()

	if len(ids) == 0 && at.store != nil {
		since24h := time.Now().Add(-24 * time.Hour)
		// Active reduces: ttrade_reduce_placed without ttrade_reduce
		if placedEntries, _ := at.store.Grid().GetGridTradeLogsByActionSince(at.id, "ttrade_reduce_placed", since24h); len(placedEntries) > 0 {
			for _, e := range placedEntries {
				reduceID := e.RelatedOrderID
				if reduceID == "" {
					continue
				}
				if reduce, _ := at.store.Grid().GetGridTradeLogByActionAndOrderID(at.id, "ttrade_reduce", e.OrderID); reduce != nil {
					continue
				}
				ids[reduceID] = true
			}
		}
		if len(ids) > 0 {
			logger.Infof("[Grid] activeTTradeReduceOrderIDs: restored %d reduce order IDs from DB (post-restart protection)", len(ids))
		}
	}
	return ids
}

// activeTTradeProtectedIDs returns the set of order IDs (prep + reduce) that must not be
// cancelled by grid maintenance.
func (at *AutoTrader) activeTTradeProtectedIDs() map[string]bool {
	protectedIDs := at.activeTTradePrepOrderIDs()
	for id := range at.activeTTradeReduceOrderIDs() {
		protectedIDs[id] = true
	}
	return protectedIDs
}

// activeProfitReduceOrderIDs returns reduce-only order IDs placed by
// checkProfitReduce that are still tracked as open, so grid maintenance
// (cancelAllGridOrders / resetGrid) doesn't cancel them out from under a
// stepped or full-close reduce with no mechanism to re-place the lost intent.
// Unlike the T-trade helpers, this has no DB fallback: checkProfitReduce runs
// frequently (every WS position push) and syncExchangeState prunes stale
// entries every cycle, so the in-memory map is expected to stay accurate
// without needing to survive a restart the way T-trade's less-frequent
// tag/fill/reduce cycle does.
func (at *AutoTrader) activeProfitReduceOrderIDs() map[string]bool {
	at.gridState.mu.RLock()
	defer at.gridState.mu.RUnlock()
	ids := make(map[string]bool, len(at.gridState.ProfitReduceOrderIDs))
	for id := range at.gridState.ProfitReduceOrderIDs {
		ids[id] = true
	}
	return ids
}

// cancelAllGridOrders cancels all grid orders
func (at *AutoTrader) cancelAllGridOrders() error {
	gridConfig := at.config.StrategyConfig.GridConfig

	// Build set of T-trade + profit-reduce order IDs to protect (memory + DB
	// fallback for post-restart on the T-trade side; profit-reduce is
	// memory-only, see activeProfitReduceOrderIDs).
	protectedIDs := at.activeTTradeProtectedIDs()
	for id := range at.activeProfitReduceOrderIDs() {
		protectedIDs[id] = true
	}

	// Get all open orders
	openOrders, err := at.trader.GetOpenOrders(gridConfig.Symbol)
	if err != nil {
		return fmt.Errorf("failed to get open orders: %w", err)
	}

	// Cancel orders, skipping T-trade orders
	toCancel := make([]string, 0, len(openOrders))
	for _, order := range openOrders {
		if protectedIDs[order.OrderID] {
			logger.Infof("[Grid] Skipping T-trade order %s during cancel all", order.OrderID)
			continue
		}
		toCancel = append(toCancel, order.OrderID)
	}

	cancelCount := 0
	type batchCanceler interface {
		CancelOrdersBatch(symbol string, orderIDs []string) int
	}
	if bc, ok := at.trader.(batchCanceler); ok {
		cancelCount = bc.CancelOrdersBatch(gridConfig.Symbol, toCancel)
	} else if gridTrader, ok := at.trader.(GridTrader); ok {
		for _, id := range toCancel {
			if err := gridTrader.CancelOrder(gridConfig.Symbol, id); err != nil {
				logger.Warnf("[Grid] Failed to cancel order %s: %v", id, err)
			} else {
				cancelCount++
			}
		}
	}

	// Reset all pending levels except T-trade prep orders
	at.gridState.mu.Lock()
	for i := range at.gridState.Levels {
		if at.gridState.Levels[i].State == "pending" && !protectedIDs[at.gridState.Levels[i].OrderID] {
			at.gridState.Levels[i].State = "empty"
			at.gridState.Levels[i].OrderID = ""
			at.gridState.Levels[i].OrderQuantity = 0
			at.gridState.Levels[i].OrderPlacedAt = time.Time{}
		}
	}
	// Rebuild OrderBook, keeping T-trade orders
	newOrderBook := make(map[string]int)
	for i, level := range at.gridState.Levels {
		if level.State == "pending" && level.OrderID != "" {
			newOrderBook[level.OrderID] = i
		}
	}
	at.gridState.OrderBook = newOrderBook
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

	// Recalculate bounds centered on current price (same logic as autoAdjustGrid)
	at.gridState.mu.Lock()
	if gridConfig.UseATRBounds {
		mktData, err := market.GetWithTimeframes(gridConfig.Symbol, []string{"4h"}, "4h", 20)
		if err != nil {
			logger.Warnf("[Grid] Failed to get ATR for adjust_grid, using default bounds: %v", err)
			at.calculateDefaultBoundsLocked(price, gridConfig)
		} else {
			at.calculateATRBoundsLocked(price, mktData, gridConfig)
		}
	} else {
		at.calculateDefaultBoundsLocked(price, gridConfig)
	}
	at.gridState.GridSpacing = (at.gridState.UpperPrice - at.gridState.LowerPrice) / float64(gridConfig.GridCount-1)
	at.gridState.mu.Unlock()

	at.initializeGridLevels(price, gridConfig)

	logger.Infof("[Grid] Adjusted grid bounds around price $%.2f: $%.2f - $%.2f",
		price, at.gridState.LowerPrice, at.gridState.UpperPrice)
	return nil
}

// syncExchangeState reconciles grid level states with the exchange.
// If openOrders is nil, open orders are fetched from the exchange.
// Handles: cancellation detection (pending→empty), fill detection (pending→filled),
// position size reconciliation, adopt of untracked orders, and T-trade reduce dispatch.
// If runPostChecks is true, also runs stop-loss check and grid skew auto-adjust.
func (at *AutoTrader) syncExchangeState(openOrders []types.OpenOrder, runPostChecks bool) {
	gridConfig := at.config.StrategyConfig.GridConfig

	if openOrders == nil {
		var err error
		openOrders, err = at.trader.GetOpenOrders(gridConfig.Symbol)
		if err != nil {
			logger.Warnf("[Grid] syncExchangeState: failed to get open orders: %v", err)
			return
		}
	}

	activeOrderIDs := make(map[string]bool, len(openOrders))
	for _, o := range openOrders {
		activeOrderIDs[o.OrderID] = true
	}

	// Prune ProfitReduceOrderIDs entries no longer open on the exchange (filled
	// or cancelled) so the map doesn't grow unbounded and cancelAllGridOrders'
	// protection set stays accurate to what's actually still resting.
	at.gridState.mu.Lock()
	for id := range at.gridState.ProfitReduceOrderIDs {
		if !activeOrderIDs[id] {
			delete(at.gridState.ProfitReduceOrderIDs, id)
		}
	}
	at.gridState.mu.Unlock()

	// Fetch positions for fill detection and reconciliation
	positions, err := at.trader.GetPositions()
	currentPositionSize := 0.0
	actualLongSize := 0.0
	actualShortSize := 0.0
	if err != nil {
		logger.Warnf("[Grid] syncExchangeState: failed to get positions: %v", err)
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

	// Pre-fetch order status for disappeared pending orders (outside lock — network calls)
	type orderFillInfo struct {
		avgPrice    float64
		executedQty float64 // real filled quantity from the exchange; 0 if unavailable
		isFilled    bool
		statusKnown bool // false if GetOrderStatus failed — treat as unknown, not cancelled
	}
	fillInfoByOrderID := make(map[string]orderFillInfo)
	at.gridState.mu.RLock()
	for _, level := range at.gridState.Levels {
		if level.State != "pending" || level.OrderID == "" || activeOrderIDs[level.OrderID] {
			continue
		}
		// Grace period: skip recently placed orders — exchange API may not reflect them yet
		if !level.OrderPlacedAt.IsZero() && time.Since(level.OrderPlacedAt) < 30*time.Second {
			continue
		}
		status, err := at.trader.GetOrderStatus(gridConfig.Symbol, level.OrderID)
		if err == nil {
			s, _ := status["status"].(string)
			avg, _ := status["avgPrice"].(float64)
			execQty, _ := status["executedQty"].(float64)
			fillInfoByOrderID[level.OrderID] = orderFillInfo{avgPrice: avg, executedQty: execQty, isFilled: s == "FILLED", statusKnown: true}
		} else {
			fillInfoByOrderID[level.OrderID] = orderFillInfo{statusKnown: false}
		}
	}
	at.gridState.mu.RUnlock()

	// Collect T-trade reduces to dispatch after releasing the lock
	type pendingReduce struct {
		side           string
		fillPrice, qty float64
		prepOrderID    string
	}
	var pendingReduces []pendingReduce

	// Collect cancelled T-trade preps to log after releasing the lock — without a
	// terminal ttrade_cancel event, a tagged-but-never-filled prep has no way to
	// leave the frontend's T-trade panel (it groups by order_id and only advances
	// past "标记" on a later action), so it lingers forever as a dead entry.
	type cancelledPrep struct {
		orderID, side string
		price, qty    float64
	}
	var cancelledPreps []cancelledPrep

	at.gridState.mu.Lock()

	expectedPositionSize := 0.0
	for _, level := range at.gridState.Levels {
		if level.State == "filled" {
			expectedPositionSize += level.PositionSize
		}
	}

	for i := range at.gridState.Levels {
		level := &at.gridState.Levels[i]
		if level.State != "pending" || level.OrderID == "" || activeOrderIDs[level.OrderID] {
			continue
		}
		// Grace period (re-checked under lock)
		if !level.OrderPlacedAt.IsZero() && time.Since(level.OrderPlacedAt) < 30*time.Second {
			logger.Debugf("[Grid] syncExchangeState: level %d order %s not yet visible (placed %.0fs ago), skipping",
				i, level.OrderID, time.Since(level.OrderPlacedAt).Seconds())
			continue
		}
		info := fillInfoByOrderID[level.OrderID]
		if !info.statusKnown && !(math.Abs(currentPositionSize) > math.Abs(expectedPositionSize)) {
			logger.Warnf("[Grid] Level %d order %s disappeared but status unknown (network error) — skipping, will retry next cycle", i, level.OrderID)
			continue
		}
		wasFilled := info.isFilled || math.Abs(currentPositionSize) > math.Abs(expectedPositionSize)
		if wasFilled {
			entryPrice := info.avgPrice
			if entryPrice <= 0 {
				entryPrice = level.Price
			}
			// Prefer the exchange's reported executedQty (ground truth, also correct
			// for partial fills) over level.OrderQuantity (what was requested/placed,
			// which is only a reliable proxy for a full fill and was historically a
			// source of bugs when it drifted from what actually got placed/filled).
			fillQty := info.executedQty
			if fillQty <= 0 {
				fillQty = level.OrderQuantity
			}
			level.State = "filled"
			level.PositionEntry = entryPrice
			level.PositionSize = fillQty
			at.gridState.TotalTrades++
			logger.Infof("[Grid] Level %d order filled at $%.2f (level=$%.2f)", i, entryPrice, level.Price)

			if prep, ok := at.gridState.TTradePrepOrders[level.OrderID]; ok && !prep.ReduceQueued {
				prep.ReduceQueued = true
				delete(at.gridState.TTradePrepOrders, level.OrderID)
				reduceQty := fillQty
				if reduceQty <= 0 {
					reduceQty = prep.Qty
				}
				pendingReduces = append(pendingReduces, pendingReduce{
					side: prep.Side, fillPrice: entryPrice, qty: reduceQty, prepOrderID: level.OrderID,
				})
				logger.Infof("[Grid] ✅ T-trade prep order filled (%.4f @ $%.2f side=%s) — will place reduce after unlock",
					reduceQty, entryPrice, prep.Side)
			} else if !ok && gridConfig.EnableTrappedReduce {
				// Order filled but was never tagged — too fast for ttradeTagOrders to catch.
				// Use the pre-fill estimated position size so we don't incorrectly dispatch
				// reduces for fills that occurred while the threshold wasn't yet met.
				orderSide := strings.ToLower(level.Side)
				qty := fillQty
				tThreshold := gridConfig.TTradePositionThresholdPct
				if tThreshold <= 0 {
					tThreshold = 30.0
				}
				totalInv := gridConfig.TotalInvestment
				lev := float64(gridConfig.Leverage)
				var active bool
				if orderSide == "sell" && totalInv > 0 && lev > 0 {
					// Subtract this fill's qty to estimate the position size before this fill.
					preFillSize := actualShortSize - qty
					if preFillSize < 0 {
						preFillSize = 0
					}
					posValue := preFillSize * entryPrice / lev
					active = posValue/totalInv*100 >= tThreshold
				} else if orderSide == "buy" && totalInv > 0 && lev > 0 {
					preFillSize := actualLongSize - qty
					if preFillSize < 0 {
						preFillSize = 0
					}
					posValue := preFillSize * entryPrice / lev
					active = posValue/totalInv*100 >= tThreshold
				}
				if active {
					logger.Infof("[Grid] 🏷 T-trade late-detect: order %s (%s @ $%.4f qty=%.4f) filled before tagging — placing reduce",
						level.OrderID, orderSide, entryPrice, qty)
					pendingReduces = append(pendingReduces, pendingReduce{
						side: orderSide, fillPrice: entryPrice, qty: qty, prepOrderID: level.OrderID,
					})
				}
			}
		} else {
			if prep, ok := at.gridState.TTradePrepOrders[level.OrderID]; ok {
				logger.Infof("[Grid] ⚠️ T-trade prep order cancelled (orderID=%s) — removing from prep map", level.OrderID)
				cancelledPreps = append(cancelledPreps, cancelledPrep{orderID: level.OrderID, side: prep.Side, price: prep.Price, qty: prep.Qty})
				delete(at.gridState.TTradePrepOrders, level.OrderID)
			}
			level.State = "empty"
			level.OrderID = ""
			level.OrderQuantity = 0
			level.OrderPlacedAt = time.Time{}
			logger.Infof("[Grid] Level %d order cancelled/expired", i)
		}
		delete(at.gridState.OrderBook, level.OrderID)
	}

	// Reconcile filled levels against actual exchange positions.
	// If actual long/short size is less than what filled levels claim,
	// scale down PositionSize proportionally (handles reduce_long/reduce_short fills).
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

	// Adopt untracked exchange orders (e.g. placed outside this session or after a restart)
	for _, o := range openOrders {
		if _, tracked := at.gridState.OrderBook[o.OrderID]; tracked {
			continue
		}
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
		if bestIdx >= 0 && (at.gridState.GridSpacing <= 0 || bestDist <= at.gridState.GridSpacing*0.5) {
			at.gridState.Levels[bestIdx].State = "pending"
			at.gridState.Levels[bestIdx].OrderID = o.OrderID
			at.gridState.Levels[bestIdx].OrderQuantity = o.Quantity
			at.gridState.OrderBook[o.OrderID] = bestIdx
			logger.Infof("[Grid] syncExchangeState: adopted untracked order %s → level %d (price=%.2f)",
				o.OrderID, bestIdx, o.Price)
		}
	}

	at.gridState.mu.Unlock()

	// Dispatch T-trade auto-reduces collected during the lock (must be outside lock)
	for _, pr := range pendingReduces {
		go at.placeTTradeReduceOrder(pr.side, pr.fillPrice, pr.qty, pr.prepOrderID)
	}

	// Log terminal ttrade_cancel for preps cancelled/expired before filling, so the
	// T-trade panel (grouped by order_id) can drop them instead of showing a
	// dead "标记" entry forever.
	for _, cp := range cancelledPreps {
		at.logGridTrade("ttrade", "ttrade_cancel", cp.side, gridConfig.Symbol,
			"prep order cancelled/expired before fill", cp.orderID, cp.qty, cp.price, 0, 0, 0, 0, true, "")
	}

	pendingCount := 0
	for _, l := range at.gridState.Levels {
		if l.State == "pending" {
			pendingCount++
		}
	}
	logger.Infof("[Grid] syncExchangeState: exchange=%d open orders, grid=%d pending levels, position=%.4f",
		len(openOrders), pendingCount, currentPositionSize)

	if runPostChecks {
		at.autoAdjustGrid()
	}
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

// resetGrid cancels all orders, recalculates bounds around currentPrice, reinitializes levels,
// and restores any filled positions to the nearest new level. Caller must NOT hold the lock.
func (at *AutoTrader) resetGrid(currentPrice float64) {
	gridConfig := at.config.StrategyConfig.GridConfig

	if err := at.cancelAllGridOrders(); err != nil {
		logger.Errorf("[Grid] resetGrid: cancel orders failed: %v", err)
	}

	at.gridState.mu.Lock()
	defer at.gridState.mu.Unlock()

	filledPositions := make(map[int]kernel.GridLevelInfo)
	for i, level := range at.gridState.Levels {
		if level.State == "filled" {
			filledPositions[i] = level
		}
	}

	if gridConfig.UseATRBounds {
		mktData, err := market.GetWithTimeframes(gridConfig.Symbol, []string{"4h"}, "4h", 20)
		if err != nil {
			logger.Warnf("[Grid] resetGrid: ATR fetch failed, using default bounds: %v", err)
			at.calculateDefaultBoundsLocked(currentPrice, gridConfig)
		} else {
			at.calculateATRBoundsLocked(currentPrice, mktData, gridConfig)
		}
	} else {
		at.calculateDefaultBoundsLocked(currentPrice, gridConfig)
	}
	at.gridState.GridSpacing = (at.gridState.UpperPrice - at.gridState.LowerPrice) / float64(gridConfig.GridCount-1)
	logger.Infof("[Grid] New bounds: $%.2f - $%.2f, spacing: $%.2f",
		at.gridState.LowerPrice, at.gridState.UpperPrice, at.gridState.GridSpacing)

	at.initializeGridLevelsLocked(currentPrice, gridConfig)

	for _, filledLevel := range filledPositions {
		closestIdx := -1
		closestDist := math.MaxFloat64
		for i, newLevel := range at.gridState.Levels {
			if dist := math.Abs(newLevel.Price - filledLevel.PositionEntry); dist < closestDist {
				closestDist = dist
				closestIdx = i
			}
		}
		if closestIdx >= 0 {
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

// autoAdjustGrid automatically adjusts grid when heavily skewed
func (at *AutoTrader) autoAdjustGrid() {
	// Don't adjust grid if T-trade is in progress
	at.gridState.mu.RLock()
	hasPendingTTrade := len(at.gridState.TTradePrepOrders) > 0 || len(at.gridState.TTradeReduceOrders) > 0
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

	currentPrice, err := at.trader.GetMarketPrice(gridConfig.Symbol)
	if err != nil {
		logger.Errorf("[Grid] Failed to get price for auto-adjust: %v", err)
		return
	}

	at.gridState.mu.RLock()
	upper := at.gridState.UpperPrice
	lower := at.gridState.LowerPrice
	at.gridState.mu.RUnlock()

	gridRange := upper - lower
	midPrice := (upper + lower) / 2
	if math.Abs(currentPrice-midPrice) < gridRange*0.3 {
		return
	}

	logger.Infof("[Grid] Adjusting grid around new price $%.2f", currentPrice)
	at.resetGrid(currentPrice)
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

	// Profit reduce tracker
	LongProfitReducedPct  float64 `json:"long_profit_reduced_pct"`
	ShortProfitReducedPct float64 `json:"short_profit_reduced_pct"`
	ProfitReduceStep      float64 `json:"profit_reduce_step"`
}

// GetGridRiskInfo returns current risk information for frontend display
func (at *AutoTrader) GetGridRiskInfo() *GridRiskInfo {
	gridConfig := at.config.StrategyConfig.GridConfig
	if gridConfig == nil || at.gridState == nil {
		return &GridRiskInfo{}
	}

	at.gridState.mu.RLock()
	defer at.gridState.mu.RUnlock()

	// Get current price
	currentPrice, _ := at.trader.GetMarketPrice(gridConfig.Symbol)

	// Use wallet balance (available + margin in positions, excl. unrealized PnL) as total investment
	leverage := gridConfig.Leverage
	totalInvestment := gridConfig.TotalInvestment

	// Get current position value — sum both LONG and SHORT sides
	positions, _ := at.trader.GetPositions()
	var currentPositionValue float64
	var currentPositionSize float64
	for _, pos := range positions {
		if sym, _ := pos["symbol"].(string); sym == gridConfig.Symbol {
			size, _ := pos["positionAmt"].(float64)
			markPrice, hasMarkPrice := pos["markPrice"].(float64)
			if !hasMarkPrice || markPrice == 0 {
				markPrice, _ = pos["entryPrice"].(float64)
			}
			currentPositionValue += math.Abs(size * markPrice)
			if currentPositionSize == 0 {
				currentPositionSize = size
			}
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

		LongProfitReducedPct:  at.gridState.LongProfitReducedPct,
		ShortProfitReducedPct: at.gridState.ShortProfitReducedPct,
		ProfitReduceStep:      gridConfig.ProfitReduceStepPct,
	}
}

// ============================================================================
// Trapped Position Detection & Batch Reduction (被套分批减仓)
// ============================================================================

// logGridTrade writes a structured trade action record to grid_trade_logs.
// source: "ai" | "ttrade" | "profit_reduce" | "profit_drawdown"
// relatedOrderID is optional (variadic to avoid touching every call site): pass a second
// order ID the entry references (e.g. the reduce order ID for a ttrade_reduce_placed row
// keyed by the prep OrderID), stored structured instead of embedded in the Reason text.
func (at *AutoTrader) logGridTrade(source, action, side, symbol, reason, orderID string,
	qty, price, entryPrice, markPrice, marginProfit, unrealizedPL float64, success bool, errMsg string, relatedOrderID ...string) {
	if at.store == nil {
		return
	}
	var related string
	if len(relatedOrderID) > 0 {
		related = relatedOrderID[0]
	}
	entry := &store.GridTradeLogModel{
		InstanceID:     at.id,
		Source:         source,
		Action:         action,
		Symbol:         symbol,
		Side:           side,
		Quantity:       qty,
		Price:          price,
		EntryPrice:     entryPrice,
		MarkPrice:      markPrice,
		MarginProfit:   marginProfit,
		UnrealizedPL:   unrealizedPL,
		Reason:         reason,
		OrderID:        orderID,
		RelatedOrderID: related,
		Success:        success,
		ErrorMsg:       errMsg,
	}
	if err := at.store.Grid().LogGridTrade(entry); err != nil {
		logger.Warnf("[Grid] Failed to write trade log: %v", err)
	}
}

// ttradeTagOrders tags ALL qualifying open grid orders as T-trade preps.
// Long trapped: tag every pending BUY below current price.
// Short trapped: tag every pending SELL above current price.
// On each cycle, stale entries (timed out or price moved out of range) are removed.
// When a new grid order is placed and a better candidate appears, it is added to the map.
func (at *AutoTrader) ttradeTagOrders(openOrders []types.OpenOrder) {
	gridConfig := at.config.StrategyConfig.GridConfig
	if !gridConfig.EnableTrappedReduce {
		return
	}

	// Get current price — prefer WS cache
	currentPrice, err := at.trader.GetMarketPrice(gridConfig.Symbol)
	if err != nil || currentPrice <= 0 {
		return
	}

	// Check per-side position size threshold
	longInfo, shortInfo, err := at.buildTTradeContext(currentPrice)
	if err != nil {
		logger.Warnf("[Grid] T-trade: buildTTradeContext failed (%v) — skipping to preserve preps", err)
		return
	}

	if !longInfo.Active && !shortInfo.Active {
		// Neither side exceeds threshold — clear all stale preps
		at.gridState.mu.Lock()
		if len(at.gridState.TTradePrepOrders) > 0 {
			at.gridState.TTradePrepOrders = make(map[string]*TTradePrepEntry)
		}
		at.gridState.mu.Unlock()
		return
	}

	// Build gridOrderIDs map to filter manual orders
	at.gridState.mu.RLock()
	gridOrderIDs := make(map[string]int, len(at.gridState.Levels))
	for i, level := range at.gridState.Levels {
		if level.State == "pending" && level.OrderID != "" {
			gridOrderIDs[level.OrderID] = i
		}
	}
	leverage := gridConfig.Leverage
	at.gridState.mu.RUnlock()

	openOrderIDs := make(map[string]bool, len(openOrders))
	for _, o := range openOrders {
		openOrderIDs[o.OrderID] = true
	}

	maxWait := 3 * time.Hour

	type timedOutEntry struct {
		id   string
		prep *TTradePrepEntry
	}
	var timedOut []timedOutEntry

	at.gridState.mu.Lock()
	// Clean up stale preps — per-prep side check, not global trapped side
	for id, prep := range at.gridState.TTradePrepOrders {
		if !prep.TaggedAt.IsZero() && time.Since(prep.TaggedAt) > maxWait {
			timedOut = append(timedOut, timedOutEntry{id, prep})
			delete(at.gridState.TTradePrepOrders, id)
			continue
		}
		if !openOrderIDs[id] {
			// Disappeared — handled by ttradeProcessFills
			continue
		}
		// Remove only if the position side is no longer active.
		// Do NOT remove based on price — the order is still open (in openOrderIDs),
		// and removing it right as price reaches the order level would cause the fill
		// to go unhandled (no reduce placed). The 3-hour timeout handles truly stale preps.
		if prep.Side == "buy" {
			if !longInfo.Active {
				logger.Infof("[Grid] T-trade prep %s (buy) @ %.2f removed — long no longer active", id, prep.Price)
				delete(at.gridState.TTradePrepOrders, id)
			}
		} else if prep.Side == "sell" {
			if !shortInfo.Active {
				logger.Infof("[Grid] T-trade prep %s (sell) @ %.2f removed — short no longer active", id, prep.Price)
				delete(at.gridState.TTradePrepOrders, id)
			}
		}
	}

	// Tag qualifying orders for active sides
	type taggedEntry struct {
		orderID string
		side    string
		price   float64
		qty     float64
	}
	var newlyTagged []taggedEntry
	for _, o := range openOrders {
		levelIdx, ok := gridOrderIDs[o.OrderID]
		if !ok {
			continue
		}
		if _, alreadyTagged := at.gridState.TTradePrepOrders[o.OrderID]; alreadyTagged {
			continue
		}
		side := strings.ToLower(o.Side)
		posSide := strings.ToUpper(o.PositionSide)
		// Skip reduce-only / closing orders
		if side == "buy" && posSide == "SHORT" {
			continue
		}
		if side == "sell" && posSide == "LONG" {
			continue
		}
		price := o.Price
		if price <= 0 {
			continue
		}
		qualifies := false
		if longInfo.Active && side == "buy" && price <= currentPrice {
			qualifies = true
		}
		if shortInfo.Active && side == "sell" && price >= currentPrice {
			qualifies = true
		}
		if !qualifies {
			continue
		}
		qty := o.Quantity
		if qty == 0 {
			lvl := at.gridState.Levels[levelIdx]
			if lvl.AllocatedUSD > 0 {
				qty = lvl.AllocatedUSD * float64(leverage) / price
			} else {
				qty = lvl.OrderQuantity
			}
		}
		at.gridState.TTradePrepOrders[o.OrderID] = &TTradePrepEntry{
			OrderID:  o.OrderID,
			Price:    price,
			Qty:      qty,
			Side:     side,
			TaggedAt: time.Now(),
		}
		newlyTagged = append(newlyTagged, taggedEntry{o.OrderID, side, price, qty})
	}
	at.gridState.mu.Unlock()

	// Handle timed-out entries
	for _, e := range timedOut {
		logger.Infof("[Grid] T-trade prep %s timed out — checking fill status before discarding", e.id)
		statusMap, sErr := at.trader.GetOrderStatus(gridConfig.Symbol, e.id)
		if sErr == nil {
			statusStr, _ := statusMap["status"].(string)
			if statusStr == "FILLED" {
				fillPrice := e.prep.Price
				if avg, ok := statusMap["avgPrice"].(float64); ok && avg > 0 {
					fillPrice = avg
				}
				logger.Infof("[Grid] T-trade prep %s FILLED just before timeout — placing reduce", e.id)
				at.logGridTrade("ttrade", "ttrade_fill", e.prep.Side, gridConfig.Symbol,
					fmt.Sprintf("prep %s filled @ %.2f (detected at timeout)", e.id, fillPrice),
					e.id, e.prep.Qty, fillPrice, 0, 0, 0, 0, true, "")
				// Mark level filled so syncExchangeState late-detect doesn't re-fire.
				at.gridState.mu.Lock()
				for i := range at.gridState.Levels {
					if at.gridState.Levels[i].OrderID == e.id {
						at.gridState.Levels[i].State = "filled"
						break
					}
				}
				at.gridState.mu.Unlock()
				go at.placeTTradeReduceOrder(e.prep.Side, fillPrice, e.prep.Qty, e.id)
			} else {
				logger.Infof("[Grid] T-trade prep %s timed out (status=%s) — removing", e.id, statusStr)
			}
		}
	}

	if len(newlyTagged) > 0 {
		at.gridState.mu.RLock()
		total := len(at.gridState.TTradePrepOrders)
		at.gridState.mu.RUnlock()
		// Build reason string
		reason := ""
		if longInfo.Active {
			reason += fmt.Sprintf("long=%.1f%%", longInfo.PositionPct)
		}
		if shortInfo.Active {
			if reason != "" {
				reason += " "
			}
			reason += fmt.Sprintf("short=%.1f%%", shortInfo.PositionPct)
		}
		logger.Infof("[Grid] T-trade: tagged %d new orders (%s), total tagged=%d", len(newlyTagged), reason, total)
		for _, e := range newlyTagged {
			at.logGridTrade("ttrade", "ttrade_tag", e.side, gridConfig.Symbol,
				reason, e.orderID, e.qty, e.price, 0, 0, 0, 0, true, "")
		}
	}
}

// ttradeProcessFills checks ALL tagged T-trade prep orders for fills.
// For each filled order, auto-places a reduce limit order using the spread config.
func (at *AutoTrader) ttradeProcessFills(openOrders []types.OpenOrder) {
	gridConfig := at.config.StrategyConfig.GridConfig
	if !gridConfig.EnableTrappedReduce {
		return
	}

	at.gridState.mu.RLock()
	if len(at.gridState.TTradePrepOrders) == 0 {
		at.gridState.mu.RUnlock()
		return
	}
	// Copy prep map to iterate without holding lock
	prepCopy := make(map[string]*TTradePrepEntry, len(at.gridState.TTradePrepOrders))
	for id, p := range at.gridState.TTradePrepOrders {
		prepCopy[id] = p
	}
	at.gridState.mu.RUnlock()

	// Build open order ID set
	openOrderIDs := make(map[string]bool, len(openOrders))
	for _, o := range openOrders {
		openOrderIDs[o.OrderID] = true
	}

	maxWait := 3 * time.Hour

	for orderID, prep := range prepCopy {
		// Timeout check — verify fill status before discarding
		if !prep.TaggedAt.IsZero() && time.Since(prep.TaggedAt) > maxWait {
			statusMap, err := at.trader.GetOrderStatus(gridConfig.Symbol, orderID)
			at.gridState.mu.Lock()
			delete(at.gridState.TTradePrepOrders, orderID)
			at.gridState.mu.Unlock()
			if err == nil {
				statusStr, _ := statusMap["status"].(string)
				if statusStr == "FILLED" {
					fillPrice := prep.Price
					if avg, ok := statusMap["avgPrice"].(float64); ok && avg > 0 {
						fillPrice = avg
					}
					logger.Infof("[Grid] T-trade prep %s FILLED at timeout check — placing reduce", orderID)
					at.logGridTrade("ttrade", "ttrade_fill", prep.Side, gridConfig.Symbol,
						fmt.Sprintf("prep %s filled @ %.2f (timeout check)", orderID, fillPrice),
						orderID, prep.Qty, fillPrice, 0, 0, 0, 0, true, "")
					// Mark level filled only on confirmed fill — not on cancel/expire.
					at.gridState.mu.Lock()
					for i := range at.gridState.Levels {
						if at.gridState.Levels[i].OrderID == orderID {
							at.gridState.Levels[i].State = "filled"
							break
						}
					}
					at.gridState.mu.Unlock()
					go at.placeTTradeReduceOrder(prep.Side, fillPrice, prep.Qty, orderID)
				} else {
					logger.Warnf("[Grid] ⚠️ T-trade prep %s timed out (status=%s) — removing (order kept alive)", orderID, statusStr)
				}
			} else {
				logger.Warnf("[Grid] ⚠️ T-trade prep %s timed out — GetOrderStatus failed, removing", orderID)
			}
			continue
		}

		if openOrderIDs[orderID] {
			// Still open — check against gridState filled flag
			filled := false
			at.gridState.mu.RLock()
			for _, level := range at.gridState.Levels {
				if level.OrderID == orderID && level.State == "filled" {
					filled = true
					break
				}
			}
			at.gridState.mu.RUnlock()
			if !filled {
				continue
			}
		}

		// Order not in open orders — confirm via GetOrderStatus
		statusMap, err := at.trader.GetOrderStatus(gridConfig.Symbol, orderID)
		if err != nil {
			logger.Warnf("[Grid] T-trade prep %s GetOrderStatus failed: %v — will retry", orderID, err)
			continue
		}
		statusStr, _ := statusMap["status"].(string)

		switch statusStr {
		case "FILLED":
			fillPrice := prep.Price
			if avg, ok := statusMap["avgPrice"].(float64); ok && avg > 0 {
				fillPrice = avg
			}
			logger.Infof("[Grid] ✅ T-trade prep %s FILLED @ %.2f (qty=%.4f side=%s) — placing reduce",
				orderID, fillPrice, prep.Qty, prep.Side)
			at.gridState.mu.Lock()
			if p, exists := at.gridState.TTradePrepOrders[orderID]; exists && !p.ReduceQueued {
				p.ReduceQueued = true
				// Mark the grid level as filled so syncExchangeState doesn't
				// re-detect this fill and trigger a duplicate reduce via late-detect.
				for i := range at.gridState.Levels {
					if at.gridState.Levels[i].OrderID == orderID {
						at.gridState.Levels[i].State = "filled"
						break
					}
				}
				// Copy fields before releasing lock — p may be modified by concurrent ttradeTagOrders
				fillLogged := p.FillAlreadyLogged
				side := p.Side
				qty := p.Qty
				at.gridState.mu.Unlock()
				if !fillLogged {
					at.logGridTrade("ttrade", "ttrade_fill", prep.Side, gridConfig.Symbol,
						fmt.Sprintf("prep %s filled @ %.2f", orderID, fillPrice),
						orderID, prep.Qty, fillPrice, 0, 0, 0, 0, true, "")
				}
				go func(side string, fp float64, qty float64, prepID string) {
					ok := at.placeTTradeReduceOrder(side, fp, qty, prepID)
					at.gridState.mu.Lock()
					if ok {
						delete(at.gridState.TTradePrepOrders, prepID)
					} else {
						if p, exists := at.gridState.TTradePrepOrders[prepID]; exists {
							p.ReduceQueued = false
						}
					}
					at.gridState.mu.Unlock()
				}(side, fillPrice, qty, orderID)
			} else {
				at.gridState.mu.Unlock()
			}
		case "CANCELED", "EXPIRED":
			logger.Warnf("[Grid] T-trade prep %s was cancelled (status=%s) — removing", orderID, statusStr)
			at.gridState.mu.Lock()
			delete(at.gridState.TTradePrepOrders, orderID)
			at.gridState.mu.Unlock()
		}
	}
}

// placeTTradeReduceOrder auto-places a reduce-only limit order after a T-trade
// prep fills, at fillPrice offset by a spread percentage. Returns true if the
// order was successfully placed. overrideSpreadPct is optional (variadic to
// avoid touching the four call sites that want the live-configured spread):
// pass a value to pin the exact spread used — e.g. when re-placing a
// cancelled-with-remainder reduce order, so it lands at the same price as
// the original even if gridConfig.TTradeSpreadPct has since changed.
func (at *AutoTrader) placeTTradeReduceOrder(prepSide string, fillPrice float64, qty float64, prepOrderID string, overrideSpreadPct ...float64) bool {
	gridConfig := at.config.StrategyConfig.GridConfig
	spreadPct := gridConfig.TTradeSpreadPct
	if len(overrideSpreadPct) > 0 {
		spreadPct = overrideSpreadPct[0]
	}
	if spreadPct < 0.2 {
		spreadPct = 0.2
	}

	var orderSide, posSide string
	var reducePrice float64
	if prepSide == "buy" {
		// Long trapped: prep was a buy → reduce_long → sell above fill price
		orderSide = "sell"
		posSide = "LONG"
		reducePrice = fillPrice * (1 + spreadPct/100)
	} else {
		// Short trapped: prep was a sell → reduce_short → buy below fill price
		orderSide = "buy"
		posSide = "SHORT"
		reducePrice = fillPrice * (1 - spreadPct/100)
	}

	logger.Infof("[Grid] T-trade auto-reduce: %s @ %.4f (fill=%.4f spread=%.1f%% qty=%.4f)",
		orderSide, reducePrice, fillPrice, spreadPct, qty)

	gridTrader, ok := at.trader.(GridTrader)
	if !ok {
		gridTrader = NewGridTraderAdapter(at.trader)
	}
	result, err := gridTrader.PlaceLimitOrder(&types.LimitOrderRequest{
		Symbol:       gridConfig.Symbol,
		Side:         orderSide,
		PositionSide: posSide,
		Quantity:     qty,
		Price:        reducePrice,
		Leverage:     gridConfig.Leverage,
		ReduceOnly:   true,
	})

	orderID := ""
	if err != nil {
		logger.Warnf("[Grid] T-trade auto-reduce failed: %v", err)
		return false
	} else if result != nil {
		orderID = result.OrderID
		// Log placement immediately so restart recovery knows a reduce is already queued
		// (ttrade_reduce is logged at fill time; this prevents duplicate reduce on restart)
		at.logGridTrade("ttrade", "ttrade_reduce_placed", prepSide, gridConfig.Symbol,
			fmt.Sprintf("reduce order %s placed for prep %s fill=%.4f spread=%.1f%%", orderID, prepOrderID, fillPrice, spreadPct),
			prepOrderID, qty, reducePrice, fillPrice, 0, 0, 0, true, "", orderID)
		at.gridState.mu.Lock()
		at.gridState.TTradeReduceOrders[orderID] = &TTradeReduceEntry{
			ReduceOrderID: orderID,
			PrepOrderID:   prepOrderID,
			PrepFillPrice: fillPrice,
			ReducePrice:   reducePrice,
			SpreadPct:     spreadPct,
			Qty:           qty,
			Side:          orderSide,
			PrepSide:      prepSide,
			PlacedAt:      time.Now(),
		}
		at.gridState.LastTrappedReduceAt = time.Now()
		at.gridState.mu.Unlock()
		logger.Infof("[Grid] ✅ T-trade reduce order placed: %s @ %.4f", orderID, reducePrice)
		return true
	}
	return false
}

// ttradeRepairOrders monitors all active T-trade reduce orders.
// Cancels timed-out ones and removes filled ones; re-places cancelled ones.
func (at *AutoTrader) ttradeRepairOrders(openOrders []types.OpenOrder) {
	gridConfig := at.config.StrategyConfig.GridConfig
	if !gridConfig.EnableTrappedReduce {
		return
	}

	at.gridState.mu.RLock()
	if len(at.gridState.TTradeReduceOrders) == 0 {
		at.gridState.mu.RUnlock()
		return
	}
	reduceCopy := make(map[string]*TTradeReduceEntry, len(at.gridState.TTradeReduceOrders))
	for id, r := range at.gridState.TTradeReduceOrders {
		reduceCopy[id] = r
	}
	at.gridState.mu.RUnlock()

	openOrderIDs := make(map[string]bool, len(openOrders))
	for _, o := range openOrders {
		openOrderIDs[o.OrderID] = true
	}

	for reduceID, entry := range reduceCopy {
		if openOrderIDs[reduceID] {
			continue // still pending on exchange
		}

		// Disappeared — check status
		statusMap, err := at.trader.GetOrderStatus(gridConfig.Symbol, reduceID)
		if err != nil {
			continue
		}
		statusStr, _ := statusMap["status"].(string)
		at.gridState.mu.Lock()
		switch statusStr {
		case "FILLED":
			fillPrice := entry.PrepFillPrice
			if avg, ok := statusMap["avgPrice"].(float64); ok && avg > 0 {
				fillPrice = avg
			}
			logger.Infof("[Grid] ✅ T-trade reduce %s FILLED @ %.4f — clearing", reduceID, fillPrice)
			delete(at.gridState.TTradeReduceOrders, reduceID)
			// Reset the prep level to empty so the AI and ttradeSupplementOrder see it as available
			for i := range at.gridState.Levels {
				if at.gridState.Levels[i].OrderID == entry.PrepOrderID {
					at.gridState.Levels[i].State = "empty"
					at.gridState.Levels[i].OrderID = ""
					break
				}
			}
			at.gridState.mu.Unlock()
			at.logGridTrade("ttrade", "ttrade_reduce", entry.PrepSide, gridConfig.Symbol,
				fmt.Sprintf("auto-reduce from prep %s fill=%.4f spread=%.1f%%", entry.PrepOrderID, entry.PrepFillPrice, entry.SpreadPct),
				entry.PrepOrderID, entry.Qty, fillPrice, entry.PrepFillPrice, 0, 0, 0, true, "")
			// Supplement new prep — check threshold first, then place.
			// Don't call ttradeTagOrders here: ttradeSupplementOrder already writes
			// directly to TTradePrepOrders, and a follow-up ttradeTagOrders call could
			// remove the newly placed prep if the reduce dropped position below threshold.
			go at.ttradeSupplementOrder(entry.PrepSide)
			continue
		case "CANCELED", "EXPIRED":
			at.gridState.mu.Unlock()
			// A CANCELED/EXPIRED order can still have a nonzero executedQty if it
			// was partially filled before being cancelled. That portion already
			// reduced the real position — re-placing the full original Qty would
			// double-count it (over-reduce). Only re-place the remainder.
			executedQty := 0.0
			if v, ok := statusMap["executedQty"].(float64); ok {
				executedQty = v
			}
			if executedQty > 0 {
				fillPrice := entry.ReducePrice
				if avg, ok := statusMap["avgPrice"].(float64); ok && avg > 0 {
					fillPrice = avg
				}
				logger.Infof("[Grid] T-trade reduce %s partially filled (%.4f of %.4f) before cancel @ %.4f — logging partial, re-placing remainder",
					reduceID, executedQty, entry.Qty, fillPrice)
				at.logGridTrade("ttrade", "ttrade_reduce", entry.PrepSide, gridConfig.Symbol,
					fmt.Sprintf("partial auto-reduce from prep %s fill=%.4f spread=%.1f%% (cancelled with remainder)", entry.PrepOrderID, entry.PrepFillPrice, entry.SpreadPct),
					entry.PrepOrderID, executedQty, fillPrice, entry.PrepFillPrice, 0, 0, 0, true, "")
			}
			remainingQty := entry.Qty - executedQty
			if remainingQty <= 0 {
				logger.Infof("[Grid] T-trade reduce %s fully filled via partial fills before cancel — clearing, no remainder to re-place", reduceID)
				at.gridState.mu.Lock()
				delete(at.gridState.TTradeReduceOrders, reduceID)
				for i := range at.gridState.Levels {
					if at.gridState.Levels[i].OrderID == entry.PrepOrderID {
						at.gridState.Levels[i].State = "empty"
						at.gridState.Levels[i].OrderID = ""
						break
					}
				}
				at.gridState.mu.Unlock()
				go at.ttradeSupplementOrder(entry.PrepSide)
				continue
			}
			logger.Warnf("[Grid] T-trade reduce %s cancelled — re-placing remainder %.4f at the same price (spread=%.1f%%)", reduceID, remainingQty, entry.SpreadPct)
			cancelledPrepSide := "buy"
			if entry.Side == "buy" {
				cancelledPrepSide = "sell"
			}
			// Pin the original spread so the re-placed order lands at exactly
			// entry.ReducePrice, even if gridConfig.TTradeSpreadPct has since
			// changed — this makes it possible to tell from the price alone
			// that this is a revival of the same reduce intent, not a new one.
			ok := at.placeTTradeReduceOrder(cancelledPrepSide, entry.PrepFillPrice, remainingQty, entry.PrepOrderID, entry.SpreadPct)
			if ok {
				// Remove old entry only after successful re-placement
				at.gridState.mu.Lock()
				delete(at.gridState.TTradeReduceOrders, reduceID)
				at.gridState.mu.Unlock()
			} else {
				logger.Warnf("[Grid] T-trade reduce re-placement failed — will retry next scan")
			}
			continue
		}
		at.gridState.mu.Unlock()
	}
}

// ttradeSupplementOrder immediately places and tags a new T-trade prep order at the
// empty grid level closest to the current price, so the next T-trade cycle begins
// without waiting for the next ttradeTagOrders pass.
func (at *AutoTrader) ttradeSupplementOrder(prepSide string) {
	gridConfig := at.config.StrategyConfig.GridConfig
	if !gridConfig.EnableTrappedReduce {
		return
	}

	// Get current price — prefer WS cache
	currentPrice, err := at.trader.GetMarketPrice(gridConfig.Symbol)
	if err != nil || currentPrice <= 0 {
		logger.Warnf("[Grid] T-trade re-tag: failed to get current price: %v", err)
		return
	}

	// Check threshold — skip if position already dropped below threshold after reduce
	longInfo, shortInfo, err := at.buildTTradeContext(currentPrice)
	if err != nil {
		logger.Warnf("[Grid] T-trade re-tag: buildTTradeContext failed: %v", err)
		return
	}
	if prepSide == "buy" && !longInfo.Active {
		logger.Infof("[Grid] T-trade re-tag: long position below threshold after reduce — skipping")
		return
	}
	if prepSide == "sell" && !shortInfo.Active {
		logger.Infof("[Grid] T-trade re-tag: short position below threshold after reduce — skipping")
		return
	}

	gridTrader, ok := at.trader.(GridTrader)
	if !ok {
		gridTrader = NewGridTraderAdapter(at.trader)
	}

	// Find nearest empty level on the correct side
	at.gridState.mu.Lock()
	bestIdx := -1
	bestDist := math.MaxFloat64
	for i, level := range at.gridState.Levels {
		if level.State != "empty" {
			continue
		}
		// buy-side prep: empty level below current price
		// sell-side prep: empty level above current price
		if prepSide == "buy" && level.Price >= currentPrice {
			continue
		}
		if prepSide == "sell" && level.Price <= currentPrice {
			continue
		}
		dist := math.Abs(level.Price - currentPrice)
		if dist < bestDist {
			bestDist = dist
			bestIdx = i
		}
	}
	if bestIdx < 0 {
		at.gridState.mu.Unlock()
		logger.Infof("[Grid] T-trade re-tag: no empty %s level found near %.2f", prepSide, currentPrice)
		return
	}
	level := at.gridState.Levels[bestIdx]
	qty := math.Round(level.AllocatedUSD*float64(gridConfig.Leverage)/level.Price*10000) / 10000
	at.gridState.mu.Unlock()

	orderSide := "BUY"
	posSide := "LONG"
	if prepSide == "sell" {
		orderSide = "SELL"
		posSide = "SHORT"
	}

	result, err := gridTrader.PlaceLimitOrder(&LimitOrderRequest{
		Symbol:       gridConfig.Symbol,
		Side:         orderSide,
		PositionSide: posSide,
		Price:        level.Price,
		Quantity:     qty,
		Leverage:     gridConfig.Leverage,
		PostOnly:     gridConfig.UseMakerOnly,
	})
	if err != nil {
		logger.Warnf("[Grid] T-trade re-tag: failed to place order at %.2f: %v", level.Price, err)
		return
	}

	orderID := result.OrderID
	at.gridState.mu.Lock()
	at.gridState.Levels[bestIdx].State = "pending"
	at.gridState.Levels[bestIdx].OrderID = orderID
	at.gridState.Levels[bestIdx].OrderQuantity = qty
	at.gridState.Levels[bestIdx].OrderPlacedAt = time.Now()
	at.gridState.OrderBook[orderID] = bestIdx
	at.gridState.TTradePrepOrders[orderID] = &TTradePrepEntry{
		OrderID:  orderID,
		Price:    level.Price,
		Qty:      qty,
		Side:     prepSide,
		TaggedAt: time.Now(),
	}
	at.gridState.mu.Unlock()

	logger.Infof("[Grid] T-trade re-tag: placed %s @ %.2f qty=%.4f (level %d) immediately after reduce fill",
		prepSide, level.Price, qty, bestIdx)
	at.logGridTrade("ttrade", "ttrade_tag", prepSide, gridConfig.Symbol,
		fmt.Sprintf("re-tag after reduce fill level=%d price=%.2f", bestIdx, level.Price),
		orderID, qty, level.Price, 0, 0, 0, 0, true, "")
}

// RunTTradeScan executes the complete T-trade scan sequence:
// 1. ttradeTagOrders:   tag qualifying open orders → build TTradePrepOrders map
// 2. ttradeRepairOrders: fix broken orders (cancelled/timed-out)
// 3. ttradeProcessFills: detect prep fills → place reduces
func (at *AutoTrader) RunTTradeScan(openOrders []types.OpenOrder) {
	gridConfig := at.config.StrategyConfig.GridConfig
	if gridConfig == nil || !gridConfig.EnableTrappedReduce {
		return
	}
	at.ttradeTagOrders(openOrders)
	at.ttradeRepairOrders(openOrders)
	at.ttradeProcessFills(openOrders)
}

// tTradeSideInfo holds per-side T-trade activation state based on position size.
type tTradeSideInfo struct {
	Active       bool
	PositionSize float64 // in base asset
	PositionPct  float64 // as % of totalInvestment
	AvgEntry     float64
}

// buildTTradeContext returns per-side T-trade active status based on position size threshold.
// A side is "active" when its position value (notional / leverage) >= TTradePositionThresholdPct of total investment.
func (at *AutoTrader) buildTTradeContext(currentPrice float64) (longInfo, shortInfo tTradeSideInfo, err error) {
	gridConfig := at.config.StrategyConfig.GridConfig
	threshold := gridConfig.TTradePositionThresholdPct
	if threshold <= 0 {
		threshold = 30.0
	}
	// Use configured TotalInvestment as denominator so that unrealized PnL
	// (gains or losses) on other positions doesn't distort the threshold check.
	totalInvestment := gridConfig.TotalInvestment
	if totalInvestment <= 0 {
		err = fmt.Errorf("total investment is zero")
		return
	}

	positions, err := at.trader.GetPositions()
	if err != nil {
		return
	}
	if len(positions) == 0 {
		// Empty positions likely means transient API failure — treat as unknown, not "no position"
		err = fmt.Errorf("GetPositions returned empty list")
		return
	}

	found := false
	for _, pos := range positions {
		symbol, _ := pos["symbol"].(string)
		if symbol != gridConfig.Symbol {
			continue
		}
		found = true
		side, _ := pos["side"].(string)
		size, _ := pos["positionAmt"].(float64)
		entry, _ := pos["entryPrice"].(float64)
		if size <= 0 || entry <= 0 {
			continue
		}
		posValue := size * entry / float64(gridConfig.Leverage)
		posPct := posValue / totalInvestment * 100

		active := posPct >= threshold
		if side == "long" {
			longInfo = tTradeSideInfo{
				Active:       active,
				PositionSize: size,
				PositionPct:  posPct,
				AvgEntry:     entry,
			}
			if !active {
				logger.Infof("[Grid] T-trade LONG inactive: size=%.4f entry=%.2f posValue=$%.2f (%.1f%% < threshold %.1f%% of $%.0f TotalInvestment)",
					size, entry, posValue, posPct, threshold, totalInvestment)
			}
		} else if side == "short" {
			shortInfo = tTradeSideInfo{
				Active:       active,
				PositionSize: size,
				PositionPct:  posPct,
				AvgEntry:     entry,
			}
			if !active {
				logger.Infof("[Grid] T-trade SHORT inactive: size=%.4f entry=%.2f posValue=$%.2f (%.1f%% < threshold %.1f%% of $%.0f TotalInvestment)",
					size, entry, posValue, posPct, threshold, totalInvestment)
			}
		}
	}
	if !found {
		// Symbol not found in positions — treat as unknown to avoid clearing active preps
		err = fmt.Errorf("symbol %s not found in positions", gridConfig.Symbol)
	}
	return
}

// buildTrappedContext fetches current price and trapped position info.
func (at *AutoTrader) buildTrappedContext() (float64, *kernel.TrappedPositionInfo, error) {
	gridConfig := at.config.StrategyConfig.GridConfig
	currentPrice, err := at.trader.GetMarketPrice(gridConfig.Symbol)
	if err != nil {
		return 0, nil, err
	}
	if currentPrice <= 0 {
		return 0, nil, fmt.Errorf("invalid current price")
	}
	trapped := at.buildTrappedPositionInfo(currentPrice)
	return currentPrice, trapped, nil
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
	if len(at.gridState.TTradeReduceOrders) > 0 {
		tTradePhase = "waiting_reduce_fill"
		for _, r := range at.gridState.TTradeReduceOrders {
			tTradePendingReduce += r.Qty
		}
	} else if len(at.gridState.TTradePrepOrders) > 0 {
		tTradePhase = "waiting_buy_fill"
		for _, p := range at.gridState.TTradePrepOrders {
			tTradePendingReduce += p.Qty
			if tTradeBuyOrderID == "" {
				tTradeBuyOrderID = p.OrderID
				tTradeBuyPrice = p.Price
			}
		}
	}
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
		TTradeBuyOrderID:    tTradeBuyOrderID,
		TTradeBuyPrice:      tTradeBuyPrice,
		TTradePendingReduce: tTradePendingReduce,
	}
}

// executeTrappedReduceSide dispatches to the correct reduce based on T-trade prep side.
// side "buy" = long trapped → reduce_long; side "sell" = short trapped → reduce_short

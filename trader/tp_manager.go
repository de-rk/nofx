package trader

import (
	"fmt"
	"nofx/logger"
	"nofx/store"
	"sync"
	"time"
)

// TPManager manages partial take profit execution
type TPManager struct {
	trader        Trader
	store         *store.Store
	traderID      string
	activeLevels  map[string][]store.TPLevel // symbol -> tp levels
	mu            sync.RWMutex
	stopCh        chan struct{}
	wg            sync.WaitGroup
}

// NewTPManager creates a new take profit manager
func NewTPManager(trader Trader, st *store.Store, traderID string) *TPManager {
	return &TPManager{
		trader:       trader,
		store:        st,
		traderID:     traderID,
		activeLevels: make(map[string][]store.TPLevel),
		stopCh:       make(chan struct{}),
	}
}

// SetTPLevels sets take profit levels for a position
func (m *TPManager) SetTPLevels(symbol string, levels []store.TPLevel) {
	if len(levels) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeLevels[symbol] = levels
	logger.Infof("[TPManager] Set %d TP levels for %s", len(levels), symbol)
}

// ClearTPLevels clears take profit levels for a symbol
func (m *TPManager) ClearTPLevels(symbol string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.activeLevels, symbol)
}

// Start starts monitoring positions for take profit triggers
func (m *TPManager) Start() {
	m.wg.Add(1)
	go m.monitorLoop()
}

// Stop stops the monitoring
func (m *TPManager) Stop() {
	close(m.stopCh)
	m.wg.Wait()
}

func (m *TPManager) monitorLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.checkAndExecute()
		}
	}
}

func (m *TPManager) checkAndExecute() {
	positions, err := m.trader.GetPositions()
	if err != nil {
		logger.Errorf("[TPManager] Failed to get positions: %v", err)
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, pos := range positions {
		levels, exists := m.activeLevels[pos.Symbol]
		if !exists || len(levels) == 0 {
			continue
		}

		pnlPct := ((pos.MarkPrice - pos.EntryPrice) / pos.EntryPrice) * 100
		if pos.Side == "short" {
			pnlPct = -pnlPct
		}

		for i := range levels {
			if levels[i].Executed || pnlPct < levels[i].Pct {
				continue
			}

			closeQty := pos.Quantity * levels[i].CloseRatio / 100
			if closeQty < 0.001 {
				continue
			}

			logger.Infof("[TPManager] Executing TP level %d for %s: %.2f%% profit, closing %.2f%%",
				i+1, pos.Symbol, pnlPct, levels[i].CloseRatio)

			var action string
			if pos.Side == "long" {
				action = "reduce_long"
			} else {
				action = "reduce_short"
			}
			_, err := m.trader.PlaceOrder(pos.Symbol, action, closeQty, 0, 0)
			if err != nil {
				logger.Errorf("[TPManager] Failed to execute TP: %v", err)
				continue
			}

			levels[i].Executed = true
			m.activeLevels[pos.Symbol] = levels
		}

		// Clean up if all levels executed
		allExecuted := true
		for _, level := range levels {
			if !level.Executed {
				allExecuted = false
				break
			}
		}
		if allExecuted {
			delete(m.activeLevels, pos.Symbol)
		}
	}
}

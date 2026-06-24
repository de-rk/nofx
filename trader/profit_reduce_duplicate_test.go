package trader

import (
	"nofx/store"
	"nofx/trader/types"
	"testing"
)

// TestProfitReduceNoDuplicateOrders verifies that checkProfitReduce does not place
// duplicate reduce orders when an existing reduce order is already pending.
func TestProfitReduceNoDuplicateOrders(t *testing.T) {
	// This test validates the fix for the bug where profit_reduce would trigger
	// multiple times in short succession, placing duplicate reduce orders.

	mockTrader := &MockGridTraderWithOrders{
		positions: []map[string]interface{}{
			{
				"symbol":           "HYPEUSDT",
				"side":             "short",
				"positionAmt":      -109.8,
				"entryPrice":       50.0,
				"markPrice":        60.67,
				"unRealizedProfit": 19.0, // ~19% profit
			},
		},
		openOrders: []types.OpenOrder{
			{
				OrderID:  "existing-reduce-order",
				Symbol:   "HYPEUSDT",
				Side:     "BUY",         // SHORT position reduce = BUY
				Price:    60.66,         // Near mark price (60.67)
				Quantity: 1.9764,
			},
		},
		placedOrders: []string{},
	}

	config := AutoTraderConfig{
		StrategyConfig: &store.StrategyConfig{
			GridConfig: &store.GridStrategyConfig{
				Symbol:                 "HYPEUSDT",
				Leverage:               3,
				ProfitReduceStepPct:    10.0,
				ProfitReduceMultiplier: 1.0,
				EnableSmallPositionClose: false,
			},
		},
	}

	at := &AutoTrader{
		trader: mockTrader,
		config: config,
		gridState: &GridState{
			Config:                config.StrategyConfig.GridConfig,
			ShortProfitReducedPct: 8.0, // Already reduced at 8%, next trigger would be 10%
		},
	}

	// Run profit reduce check
	at.checkProfitReduce()

	// Verify no new orders were placed (since one already exists)
	if len(mockTrader.placedOrders) > 0 {
		t.Errorf("Expected no new orders to be placed, but got %d orders: %v",
			len(mockTrader.placedOrders), mockTrader.placedOrders)
	}
}

// TestProfitReduceSameStepNoRetrigger verifies that profit reduce does not re-trigger
// at the same step level even if profit increases within that step range.
func TestProfitReduceSameStepNoRetrigger(t *testing.T) {
	// Scenario: 
	// - Step size is 6% (ProfitReduceStepPct)
	// - Already reduced at 18% step (ShortProfitReducedPct = 18)
	// - Current profit is 21.4% (which is still in 18-24% range)
	// - Should NOT trigger another reduce at 18% step
	// - Should only trigger when profit reaches 24% (next step)

	mockTrader := &MockGridTraderWithOrders{
		positions: []map[string]interface{}{
			{
				"symbol":           "HYPEUSDT",
				"side":             "short",
				"positionAmt":      -100.8,
				"entryPrice":       50.0,
				"markPrice":        60.42,
				"unRealizedProfit": 107.0, // ~21.4% profit (107 / (100.8*50/3) * 100)
			},
		},
		openOrders: []types.OpenOrder{},
		placedOrders: []string{},
	}

	config := AutoTraderConfig{
		StrategyConfig: &store.StrategyConfig{
			GridConfig: &store.GridStrategyConfig{
				Symbol:                 "HYPEUSDT",
				Leverage:               3,
				ProfitReduceStepPct:    6.0,  // 6% steps: 6%, 12%, 18%, 24%, ...
				ProfitReduceMultiplier: 0.1,
				EnableSmallPositionClose: false,
			},
		},
	}

	at := &AutoTrader{
		trader: mockTrader,
		config: config,
		gridState: &GridState{
			Config:                config.StrategyConfig.GridConfig,
			ShortProfitReducedPct: 18.0, // Already triggered at 18% step
		},
	}

	// Run profit reduce check
	at.checkProfitReduce()

	// Verify NO new orders were placed (profit 21.4% should not re-trigger at 18% step)
	if len(mockTrader.placedOrders) > 0 {
		t.Errorf("Expected no new orders (profit 21.4%% should not re-trigger at 18%% step), but got %d orders: %v",
			len(mockTrader.placedOrders), mockTrader.placedOrders)
	}

	// Verify state was NOT changed
	if at.gridState.ShortProfitReducedPct != 18.0 {
		t.Errorf("Expected state to remain at 18%%, but got %.0f%%", at.gridState.ShortProfitReducedPct)
	}
}

// TestProfitReduceNextStep verifies that profit reduce triggers correctly at the next step.
func TestProfitReduceNextStep(t *testing.T) {
	// Scenario:
	// - Already reduced at 18% step
	// - Current profit is 25% (exceeds 24% threshold)
	// - Should trigger reduce at 24% step

	mockTrader := &MockGridTraderWithOrders{
		positions: []map[string]interface{}{
			{
				"symbol":           "HYPEUSDT",
				"side":             "short",
				"positionAmt":      -100.0,
				"entryPrice":       50.0,
				"markPrice":        62.5,
				"unRealizedProfit": 125.0, // ~25% profit
			},
		},
		openOrders: []types.OpenOrder{},
		placedOrders: []string{},
	}

	config := AutoTraderConfig{
		StrategyConfig: &store.StrategyConfig{
			GridConfig: &store.GridStrategyConfig{
				Symbol:                 "HYPEUSDT",
				Leverage:               3,
				ProfitReduceStepPct:    6.0,
				ProfitReduceMultiplier: 0.1,
				EnableSmallPositionClose: false,
			},
		},
	}

	at := &AutoTrader{
		trader: mockTrader,
		config: config,
		gridState: &GridState{
			Config:                config.StrategyConfig.GridConfig,
			ShortProfitReducedPct: 18.0, // Already at 18%
		},
	}

	// Run profit reduce check
	at.checkProfitReduce()

	// Verify ONE new order was placed (should trigger at 24% step)
	if len(mockTrader.placedOrders) != 1 {
		t.Errorf("Expected 1 order to be placed (profit 25%% should trigger at 24%% step), but got %d orders",
			len(mockTrader.placedOrders))
	}

	// Verify state was updated to 24%
	if at.gridState.ShortProfitReducedPct != 24.0 {
		t.Errorf("Expected state to update to 24%%, but got %.0f%%", at.gridState.ShortProfitReducedPct)
	}
}

// MockGridTraderWithOrders extends mock to track open orders
type MockGridTraderWithOrders struct {
	positions    []map[string]interface{}
	openOrders   []types.OpenOrder
	placedOrders []string
}

func (m *MockGridTraderWithOrders) GetPositions() ([]map[string]interface{}, error) {
	return m.positions, nil
}

func (m *MockGridTraderWithOrders) GetOpenOrders(symbol string) ([]types.OpenOrder, error) {
	return m.openOrders, nil
}

func (m *MockGridTraderWithOrders) PlaceLimitOrder(req *LimitOrderRequest) (*OrderResult, error) {
	orderID := "new-order-" + string(rune(len(m.placedOrders)))
	m.placedOrders = append(m.placedOrders, orderID)
	return &OrderResult{OrderID: orderID}, nil
}

func (m *MockGridTraderWithOrders) GetMarketPrice(symbol string) (float64, error) {
	return 60.67, nil
}

func (m *MockGridTraderWithOrders) GetBalance() (map[string]interface{}, error) {
	return map[string]interface{}{
		"availableBalance": 1000.0,
	}, nil
}

func (m *MockGridTraderWithOrders) SetLeverage(symbol string, leverage int) error {
	return nil
}

func (m *MockGridTraderWithOrders) CloseLong(symbol string, quantity float64) (map[string]interface{}, error) {
	return nil, nil
}

func (m *MockGridTraderWithOrders) CloseShort(symbol string, quantity float64) (map[string]interface{}, error) {
	return nil, nil
}

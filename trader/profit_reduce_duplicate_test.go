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

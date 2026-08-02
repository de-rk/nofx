package trader

import (
	"nofx/market"
	"testing"
)

// ============================================================================
// Grid Direction Tests
// ============================================================================

func TestGetBuySellRatio(t *testing.T) {
	tests := []struct {
		name      string
		direction market.GridDirection
		biasRatio float64
		wantBuy   float64
		wantSell  float64
	}{
		{"neutral", market.GridDirectionNeutral, 0.7, 0.5, 0.5},
		{"long", market.GridDirectionLong, 0.7, 1.0, 0.0},
		{"short", market.GridDirectionShort, 0.7, 0.0, 1.0},
		{"long_bias_default", market.GridDirectionLongBias, 0.7, 0.7, 0.3},
		{"short_bias_default", market.GridDirectionShortBias, 0.7, 0.3, 0.7},
		{"long_bias_custom", market.GridDirectionLongBias, 0.8, 0.8, 0.2},
		{"short_bias_custom", market.GridDirectionShortBias, 0.8, 0.2, 0.8},
		{"invalid_bias_uses_default", market.GridDirectionLongBias, 0, 0.7, 0.3},
		{"negative_bias_uses_default", market.GridDirectionLongBias, -1, 0.7, 0.3},
	}

	const tolerance = 0.0001
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buy, sell := tt.direction.GetBuySellRatio(tt.biasRatio)
			buyDiff := buy - tt.wantBuy
			sellDiff := sell - tt.wantSell
			if buyDiff < -tolerance || buyDiff > tolerance || sellDiff < -tolerance || sellDiff > tolerance {
				t.Errorf("GetBuySellRatio(%v, %v) = (%v, %v), want (%v, %v)",
					tt.direction, tt.biasRatio, buy, sell, tt.wantBuy, tt.wantSell)
			}
		})
	}
}

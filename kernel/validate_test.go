package kernel

import (
	"testing"
)

// TestLeverageFallback tests automatic correction when leverage exceeds limit
func TestLeverageFallback(t *testing.T) {
	tests := []struct {
		name            string
		decision        Decision
		accountEquity   float64
		btcEthLeverage  int
		altcoinLeverage int
		wantLeverage    int // Expected leverage after correction
		wantError       bool
	}{
		{
			name: "Altcoin leverage exceeded - auto-correct to limit",
			decision: Decision{
				Symbol:                  "SOLUSDT",
				Action:                  "open_long",
				Leverage:                20, // Exceeds limit
				PositionSizeUSD:         100,
				TrailingStopCallbackPct: 1.0,
			},
			accountEquity:   100,
			btcEthLeverage:  10,
			altcoinLeverage: 5, // Limit 5x
			wantLeverage:    5, // Should be corrected to 5
			wantError:       false,
		},
		{
			name: "BTC leverage exceeded - auto-correct to limit",
			decision: Decision{
				Symbol:                  "BTCUSDT",
				Action:                  "open_long",
				Leverage:                20, // Exceeds limit
				PositionSizeUSD:         1000,
				TrailingStopCallbackPct: 1.0,
			},
			accountEquity:   100,
			btcEthLeverage:  10, // Limit 10x
			altcoinLeverage: 5,
			wantLeverage:    10, // Should be corrected to 10
			wantError:       false,
		},
		{
			name: "Leverage within limit - no correction",
			decision: Decision{
				Symbol:                  "ETHUSDT",
				Action:                  "open_short",
				Leverage:                5, // Not exceeded
				PositionSizeUSD:         500,
				TrailingStopCallbackPct: 1.0,
			},
			accountEquity:   100,
			btcEthLeverage:  10,
			altcoinLeverage: 5,
			wantLeverage:    5, // Stays unchanged
			wantError:       false,
		},
		{
			name: "Leverage is 0 - should error",
			decision: Decision{
				Symbol:                  "SOLUSDT",
				Action:                  "open_long",
				Leverage:                0, // Invalid
				PositionSizeUSD:         100,
				TrailingStopCallbackPct: 1.0,
			},
			accountEquity:   100,
			btcEthLeverage:  10,
			altcoinLeverage: 5,
			wantLeverage:    0,
			wantError:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use default position value ratios for testing (10x for BTC/ETH, 1.5x for altcoins)
			err := validateDecision(&tt.decision, tt.accountEquity, tt.btcEthLeverage, tt.altcoinLeverage, 10.0, 1.5)

			// Check error status
			if (err != nil) != tt.wantError {
				t.Errorf("validateDecision() error = %v, wantError %v", err, tt.wantError)
				return
			}

			// If shouldn't error, check if leverage was correctly corrected
			if !tt.wantError && tt.decision.Leverage != tt.wantLeverage {
				t.Errorf("Leverage not corrected: got %d, want %d", tt.decision.Leverage, tt.wantLeverage)
			}
		})
	}
}

func TestTrailingStopEntryDoesNotRequireRiskRewardRatio(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "trailing-only position opens without fixed prices"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := Decision{
				Symbol:                    "ETHUSDT",
				Action:                    "open_long",
				Leverage:                  3,
				PositionSizeUSD:           100,
				TrailingStopActivationPct: 0,
				TrailingStopCallbackPct:   1.5,
			}
			err := validateDecision(&decision, 1000, 10, 5, 10, 1.5)
			if err != nil {
				t.Fatalf("validateDecision() error = %v, want no error", err)
			}
			if decision.StopLoss != 0 || decision.TakeProfit != 0 {
				t.Fatalf("fixed prices should be ignored, got stop loss %.2f and take profit %.2f", decision.StopLoss, decision.TakeProfit)
			}
		})
	}
}

// contains checks if string contains substring (helper function)
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && stringContains(s, substr)))
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

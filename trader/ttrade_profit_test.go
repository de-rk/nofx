package trader

import "testing"

func TestCalculateTTradeRealizedPL(t *testing.T) {
	tests := []struct {
		name             string
		prepSide         string
		entry, exit, qty float64
		want             float64
	}{
		{name: "long profit", prepSide: "buy", entry: 100, exit: 101.5, qty: 2, want: 3},
		{name: "short profit", prepSide: "sell", entry: 100, exit: 98.5, qty: 2, want: 3},
		{name: "long loss", prepSide: "long", entry: 100, exit: 99, qty: 2, want: -2},
		{name: "invalid values", prepSide: "buy", entry: 0, exit: 100, qty: 2, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := calculateTTradeRealizedPL(tt.prepSide, tt.entry, tt.exit, tt.qty); got != tt.want {
				t.Fatalf("calculateTTradeRealizedPL() = %v, want %v", got, tt.want)
			}
		})
	}
}

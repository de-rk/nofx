package trader

import "testing"

func TestShouldCloseProfitDrawdown(t *testing.T) {
	tests := []struct {
		name                      string
		current, peak, activation float64
		threshold                 float64
		want                      bool
	}{
		{name: "peak and current retain activation profit", current: 2, peak: 4, activation: 2, threshold: 50, want: true},
		{name: "current profit below activation is retained", current: 1.9, peak: 4, activation: 2, threshold: 50, want: false},
		{name: "drawdown below threshold", current: 3, peak: 4, activation: 2, threshold: 50, want: false},
		{name: "peak never reached activation", current: 1.5, peak: 1.8, activation: 2, threshold: 20, want: false},
		{name: "disabled threshold", current: 2, peak: 4, activation: 2, threshold: 0, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldCloseProfitDrawdown(tt.current, tt.peak, tt.activation, tt.threshold); got != tt.want {
				t.Fatalf("shouldCloseProfitDrawdown() = %v, want %v", got, tt.want)
			}
		})
	}
}

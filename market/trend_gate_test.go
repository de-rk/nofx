package market

import "testing"

func TestEvaluateTrendGateAllowsLongWithPriceAndVolume(t *testing.T) {
	closes := []float64{100, 101, 102, 103, 104}
	volumes := []float64{10, 10, 10, 10, 20}
	result := EvaluateTrendGate(closes, volumes, "open_long", true, 3, 2, 1.2)
	if !result.Allowed {
		t.Fatalf("gate rejected valid long: %s", result.Reason)
	}
}

func TestEvaluateTrendGateRejectsShortAgainstTrend(t *testing.T) {
	closes := []float64{100, 101, 102, 103, 104}
	volumes := []float64{10, 10, 10, 10, 20}
	result := EvaluateTrendGate(closes, volumes, "open_short", true, 3, 2, 1.2)
	if result.Allowed {
		t.Fatal("gate allowed short against upward trend")
	}
}

func TestEvaluateTrendGateNeverBlocksClosing(t *testing.T) {
	result := EvaluateTrendGate([]float64{100}, []float64{0}, "close_long", true, 20, 2, 1.2)
	if !result.Allowed {
		t.Fatalf("gate blocked close: %s", result.Reason)
	}
}

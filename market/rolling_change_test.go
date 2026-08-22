package market

import (
	"math"
	"testing"
	"time"
)

func TestRollingChangeUsesThreeMinuteReference(t *testing.T) {
	start := time.Unix(0, 0)
	r := NewRollingChange(3 * time.Minute)

	if _, ready := r.Add(PricePoint{Timestamp: start, Price: 100}); ready {
		t.Fatal("window should not be ready before three minutes")
	}
	if _, ready := r.Add(PricePoint{Timestamp: start.Add(2 * time.Minute), Price: 101}); ready {
		t.Fatal("window should not be ready before three minutes")
	}
	change, ready := r.Add(PricePoint{Timestamp: start.Add(3 * time.Minute), Price: 103})
	if !ready || math.Abs(change-3) > 1e-9 {
		t.Fatalf("change = %.6f, ready = %v, want 3 and true", change, ready)
	}
}

func TestRollingChangeTriggersBothDirectionsAtThreshold(t *testing.T) {
	start := time.Unix(0, 0)
	for _, tc := range []struct {
		name  string
		price float64
		want  float64
	}{
		{name: "up", price: 103, want: 3},
		{name: "down", price: 97, want: -3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRollingChange(3 * time.Minute)
			r.Add(PricePoint{Timestamp: start, Price: 100})
			got, ready := r.Add(PricePoint{Timestamp: start.Add(3 * time.Minute), Price: tc.price})
			if !ready || math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("change = %.6f, ready = %v, want %.6f and true", got, ready, tc.want)
			}
		})
	}
}

func TestRollingChangeIgnoresDuplicateTimestamp(t *testing.T) {
	start := time.Unix(0, 0)
	r := NewRollingChange(3 * time.Minute)
	r.Add(PricePoint{Timestamp: start, Price: 100})
	r.Add(PricePoint{Timestamp: start.Add(1 * time.Minute), Price: 101})
	r.Add(PricePoint{Timestamp: start.Add(1 * time.Minute), Price: 102})
	got, ready := r.Add(PricePoint{Timestamp: start.Add(3 * time.Minute), Price: 103})
	if !ready || math.Abs(got-3) > 1e-9 {
		t.Fatalf("change = %.6f, ready = %v, want updated reference 3 and true", got, ready)
	}
}

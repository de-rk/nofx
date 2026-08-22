package market

import (
	"sort"
	"time"
)

// PricePoint is a timestamped market price used for rolling movement checks.
type PricePoint struct {
	Timestamp time.Time
	Price     float64
}

// RollingChange tracks prices over a fixed time window.
type RollingChange struct {
	window        time.Duration
	points        []PricePoint
	lastReference float64
}

// NewRollingChange creates a rolling price-change calculator.
func NewRollingChange(window time.Duration) *RollingChange {
	return &RollingChange{window: window}
}

// Add records a price and returns the change from the latest point at least
// window old. The boolean is false until a complete window is available.
func (r *RollingChange) Add(point PricePoint) (changePct float64, ready bool) {
	if r.window <= 0 || point.Price <= 0 || point.Timestamp.IsZero() {
		return 0, false
	}

	i := sort.Search(len(r.points), func(i int) bool {
		return !r.points[i].Timestamp.Before(point.Timestamp)
	})
	if i < len(r.points) && r.points[i].Timestamp.Equal(point.Timestamp) {
		r.points[i] = point
	} else {
		r.points = append(r.points, PricePoint{})
		copy(r.points[i+1:], r.points[i:])
		r.points[i] = point
	}

	cutoff := point.Timestamp.Add(-r.window)
	baseIdx := sort.Search(len(r.points), func(i int) bool {
		return r.points[i].Timestamp.After(cutoff)
	}) - 1
	if baseIdx < 0 {
		return 0, false
	}

	base := r.points[baseIdx].Price
	r.lastReference = base
	if base <= 0 {
		return 0, false
	}
	changePct = (point.Price - base) / base * 100

	keepFrom := baseIdx - 1
	if keepFrom > 0 {
		r.points = append([]PricePoint(nil), r.points[keepFrom:]...)
	}
	return changePct, true
}

// Points returns a copy of the currently retained price points.
func (r *RollingChange) Points() []PricePoint {
	return append([]PricePoint(nil), r.points...)
}

// LastReference returns the price used by the latest completed calculation.
func (r *RollingChange) LastReference() float64 {
	return r.lastReference
}

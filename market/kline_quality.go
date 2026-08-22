package market

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// cleanKlines keeps only ordered, completed and numerically valid candles.
// The latest completed candle must have volume so callers never treat a
// forming or broken candle as confirmed market data.
func cleanKlines(klines []Kline, timeframe string, now time.Time) ([]Kline, error) {
	if len(klines) == 0 {
		return nil, fmt.Errorf("no kline data")
	}
	duration, err := TFDuration(timeframe)
	if err != nil {
		return nil, err
	}
	ordered := append([]Kline(nil), klines...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].OpenTime < ordered[j].OpenTime })

	completed := make([]Kline, 0, len(ordered))
	var lastOpenTime int64 = -1
	for _, kline := range ordered {
		if kline.OpenTime <= 0 || kline.OpenTime == lastOpenTime {
			continue
		}
		lastOpenTime = kline.OpenTime
		closeTime := kline.CloseTime
		if closeTime <= 0 {
			closeTime = kline.OpenTime + duration.Milliseconds()
		}
		if closeTime > now.UnixMilli() {
			continue
		}
		if !validKline(kline) {
			return nil, fmt.Errorf("invalid OHLCV data at %d", kline.OpenTime)
		}
		kline.CloseTime = closeTime
		completed = append(completed, kline)
	}
	if len(completed) == 0 {
		return nil, fmt.Errorf("no completed candles for %s", timeframe)
	}
	latest := completed[len(completed)-1]
	if latest.Volume <= 0 {
		return nil, fmt.Errorf("latest completed candle has zero volume at %d", latest.CloseTime)
	}
	return completed, nil
}

func validKline(kline Kline) bool {
	if kline.Open <= 0 || kline.High <= 0 || kline.Low <= 0 || kline.Close <= 0 || kline.Volume < 0 {
		return false
	}
	if kline.High < kline.Open || kline.High < kline.Close || kline.Low > kline.Open || kline.Low > kline.Close {
		return false
	}
	for _, value := range []float64{kline.Open, kline.High, kline.Low, kline.Close, kline.Volume} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

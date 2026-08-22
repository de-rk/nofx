package market

import "fmt"

// TrendGateResult describes the deterministic entry checks for one direction.
type TrendGateResult struct {
	Allowed        bool
	Reason         string
	PriceChangePct float64
	VolumeRatio    float64
}

// EvaluateTrendGate evaluates a price-and-volume entry gate using completed
// closes and volumes from one timeframe. It never decides whether to close.
func EvaluateTrendGate(closes, volumes []float64, action string, enabled bool, lookback int, minPriceChangePct, minVolumeRatio float64) TrendGateResult {
	if !enabled {
		return TrendGateResult{Allowed: true, Reason: "disabled"}
	}
	if action != "open_long" && action != "open_short" {
		return TrendGateResult{Allowed: true, Reason: "not an entry"}
	}
	if lookback <= 0 {
		lookback = 20
	}
	minChange := minPriceChangePct
	if minChange < 0 {
		minChange = -minChange
	}
	if minVolumeRatio <= 0 {
		minVolumeRatio = 1
	}
	if len(closes) < lookback+1 || len(volumes) < lookback+1 {
		return TrendGateResult{Reason: fmt.Sprintf("need %d completed candles", lookback+1)}
	}
	latestClose := closes[len(closes)-1]
	baseline := closes[len(closes)-1-lookback]
	if latestClose <= 0 || baseline <= 0 {
		return TrendGateResult{Reason: "invalid close price"}
	}
	priceChange := (latestClose - baseline) / baseline * 100
	volumeStart := len(volumes) - lookback
	volumeSum := 0.0
	for _, volume := range volumes[volumeStart:] {
		volumeSum += volume
	}
	averageVolume := volumeSum / float64(lookback)
	if averageVolume <= 0 {
		return TrendGateResult{PriceChangePct: priceChange, Reason: "volume baseline is zero"}
	}
	volumeRatio := volumes[len(volumes)-1] / averageVolume
	result := TrendGateResult{PriceChangePct: priceChange, VolumeRatio: volumeRatio}
	if action == "open_long" && priceChange < minChange {
		result.Reason = fmt.Sprintf("long price change %.2f%% below %.2f%%", priceChange, minChange)
		return result
	}
	if action == "open_short" && priceChange > -minChange {
		result.Reason = fmt.Sprintf("short price change %.2f%% above -%.2f%%", priceChange, minChange)
		return result
	}
	if volumeRatio < minVolumeRatio {
		result.Reason = fmt.Sprintf("volume ratio %.2fx below %.2fx", volumeRatio, minVolumeRatio)
		return result
	}
	result.Allowed = true
	result.Reason = "price and volume confirmed"
	return result
}

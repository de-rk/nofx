package backtest

import (
	"math"
	"nofx/market"
	"testing"
)

func makeATRKlines(count int, tr float64) []market.Kline {
	klines := make([]market.Kline, count)
	for i := range klines {
		openTime := int64(i * 4 * 60 * 60 * 1000)
		klines[i] = market.Kline{
			OpenTime:  openTime,
			High:      100 + tr/2,
			Low:       100 - tr/2,
			Close:     100,
			CloseTime: openTime + 4*60*60*1000 - 1,
		}
	}
	return klines
}

func TestATRTimelineUsesWilderATR14(t *testing.T) {
	klines := makeATRKlines(16, 2)
	timeline := newATRTimeline(klines)

	want := 2.0
	if got := timeline.at(klines[14].CloseTime); math.Abs(got-want) > 1e-9 {
		t.Fatalf("ATR14 = %.12f, want %.12f", got, want)
	}
}

func TestATRTimelineAlignsClosedFourHourBars(t *testing.T) {
	klines := makeATRKlines(29, 2)
	klines[15].High = 110
	klines[15].Low = 90
	timeline := newATRTimeline(klines)

	beforeChange := timeline.at(klines[14].OpenTime)
	afterChange := timeline.at(klines[15].CloseTime)
	if beforeChange != 0 {
		t.Fatalf("ATR before the first complete 14-TR window = %.12f, want 0", beforeChange)
	}
	if afterChange <= beforeChange {
		t.Fatalf("ATR after a changed 4h bar = %.12f, want greater than %.12f", afterChange, beforeChange)
	}
}

func TestATRTimelineIgnoresFutureBars(t *testing.T) {
	klines := makeATRKlines(30, 2)
	baseline := newATRTimeline(klines)
	klines[29].High = 1000
	klines[29].Low = 1
	withFutureChange := newATRTimeline(klines)

	ts := klines[20].CloseTime
	if got, want := withFutureChange.at(ts), baseline.at(ts); got != want {
		t.Fatalf("ATR at past timestamp changed from future bar: got %.12f, want %.12f", got, want)
	}
}

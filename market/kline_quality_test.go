package market

import (
	"strings"
	"testing"
	"time"
)

func TestCleanKlinesDropsFormingCandleAndSorts(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	klines := []Kline{
		{OpenTime: 700_000, CloseTime: 760_000, Open: 100, High: 102, Low: 99, Close: 101, Volume: 10},
		{OpenTime: 400_000, CloseTime: 460_000, Open: 98, High: 100, Low: 97, Close: 99, Volume: 8},
		{OpenTime: 1_000_000, CloseTime: 1_060_000, Open: 101, High: 103, Low: 100, Close: 102, Volume: 12},
	}
	got, err := cleanKlines(klines, "1m", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].OpenTime != 400_000 || got[1].OpenTime != 700_000 {
		t.Fatalf("unexpected cleaned candles: %+v", got)
	}
}

func TestCleanKlinesRejectsLatestZeroVolume(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	_, err := cleanKlines([]Kline{{
		OpenTime: 700_000, CloseTime: 760_000, Open: 100, High: 102, Low: 99, Close: 101, Volume: 0,
	}}, "1m", now)
	if err == nil || !strings.Contains(err.Error(), "zero volume") {
		t.Fatalf("error = %v, want zero-volume error", err)
	}
}

func TestCleanKlinesKeepsHistoricalZeroVolume(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	got, err := cleanKlines([]Kline{
		{OpenTime: 400_000, CloseTime: 460_000, Open: 98, High: 100, Low: 97, Close: 99, Volume: 0},
		{OpenTime: 700_000, CloseTime: 760_000, Open: 100, High: 102, Low: 99, Close: 101, Volume: 10},
	}, "1m", now)
	if err != nil || len(got) != 2 || got[0].Volume != 0 {
		t.Fatalf("got=%+v err=%v, historical zero volume should be preserved", got, err)
	}
}

package hyperliquid

import (
	"context"
	"fmt"
	"nofx/logger"
	"nofx/market"
	"nofx/trader/types"
	"strconv"
	"sync"
	"time"

	"github.com/sonirico/go-hyperliquid"
)

// hlWSBufCap is how many candles are kept per timeframe.
const hlWSBufCap = 500

// Compile-time capability assertions. The AI trader and the grid probe these
// through anonymous interface assertions, so a signature drift would silently
// disable the feature instead of failing the build.
var (
	_ types.TrailingStopTrader = (*HyperliquidTrader)(nil)
	_ interface {
		GetMinOrderSize(symbol string) (float64, error)
	} = (*HyperliquidTrader)(nil)
	_ interface {
		CancelOrdersBatch(symbol string, orderIDs []string) int
	} = (*HyperliquidTrader)(nil)
	_ interface {
		StartWS(symbol string, primaryTf string) error
		StopWS()
		SubscribeSymbol(symbol string)
		SetWSCallbacks(onPosition func([]map[string]interface{}), onOrder, onKlineClose func())
		SetPrimaryKlineTf(tf string)
		GetWSKlines(symbol, tf string) ([]market.Kline, bool)
	} = (*HyperliquidTrader)(nil)
)

// hlCandle is a decoded Hyperliquid websocket candle.
type hlCandle struct {
	openTime  int64
	closeTime int64
	open      float64
	high      float64
	low       float64
	close     float64
	volume    float64
	trades    int
}

// hlWS owns the Hyperliquid websocket subscriptions that make grid trading
// event-driven instead of poll-driven:
//
//   - candles            -> close of the primary timeframe drives the AI cycle
//   - clearinghouseState -> position pushes drive profit-reduce / drawdown checks
//   - orderUpdates       -> order events drive the T-trade scan
//   - allMids            -> caches mark prices so cycles avoid REST round trips
//
// Hyperliquid has no WS wrapper in the trader layer, so everything OKX gets from
// its public/private channels is reconstructed here.
type hlWS struct {
	trader *HyperliquidTrader

	mu        sync.Mutex
	client    *hyperliquid.WebsocketClient
	cancel    context.CancelFunc
	coin      string
	symbol    string
	primaryTf string
	klineTfs  []string
	started   bool

	buffersMu sync.RWMutex
	buffers   map[string][]hlCandle
	// liveSeen records that a websocket bar has already landed for a timeframe,
	// so the first bar after seeding is never mistaken for a candle close.
	liveSeen map[string]bool

	midsMu sync.RWMutex
	mids   map[string]float64

	cbMu         sync.RWMutex
	onPosition   func([]map[string]interface{})
	onOrder      func()
	onKlineClose func()
}

func newHLWS(trader *HyperliquidTrader) *hlWS {
	return &hlWS{
		trader:   trader,
		buffers:  make(map[string][]hlCandle),
		liveSeen: make(map[string]bool),
		mids:     make(map[string]float64),
	}
}

// Start connects and subscribes. It mirrors the OKX StartWS contract so the grid
// can drive the AI cycle off candle closes.
func (w *hlWS) Start(symbol, primaryTf string) error {
	if primaryTf == "" {
		primaryTf = "5m"
	}

	coin := convertSymbolToHyperliquid(symbol)
	baseURL := hyperliquid.MainnetAPIURL
	if w.trader.isTestnet {
		baseURL = hyperliquid.TestnetAPIURL
	}

	ctx, cancel := context.WithCancel(context.Background())
	client := hyperliquid.NewWebsocketClient(baseURL)
	if err := client.Connect(ctx); err != nil {
		cancel()
		return fmt.Errorf("hyperliquid ws connect failed: %w", err)
	}

	tfs := []string{primaryTf}
	if primaryTf != "4h" {
		tfs = append(tfs, "4h")
	}

	w.mu.Lock()
	w.client = client
	w.cancel = cancel
	w.coin = coin
	w.symbol = symbol
	w.primaryTf = primaryTf
	w.klineTfs = tfs
	w.started = true
	w.mu.Unlock()

	for _, tf := range tfs {
		if _, err := client.Candles(
			hyperliquid.CandlesSubscriptionParams{Coin: coin, Interval: tf},
			w.onCandle(tf),
		); err != nil {
			logger.Warnf("[Hyperliquid][WS] failed to subscribe %s candles for %s: %v", tf, coin, err)
		}
	}

	if _, err := client.ClearinghouseState(
		hyperliquid.ClearinghouseStateSubscriptionParams{User: w.trader.walletAddr},
		w.onClearinghouseState,
	); err != nil {
		logger.Warnf("[Hyperliquid][WS] failed to subscribe clearinghouseState: %v", err)
	}

	if _, err := client.OrderUpdates(
		hyperliquid.OrderUpdatesSubscriptionParams{User: w.trader.walletAddr},
		w.onOrderUpdates,
	); err != nil {
		logger.Warnf("[Hyperliquid][WS] failed to subscribe orderUpdates: %v", err)
	}

	if _, err := client.AllMids(
		hyperliquid.AllMidsSubscriptionParams{},
		w.onAllMids,
	); err != nil {
		logger.Warnf("[Hyperliquid][WS] failed to subscribe allMids: %v", err)
	}

	// Seed buffers from REST so the very first cycle already has context.
	for _, tf := range tfs {
		go w.seedKlines(coin, tf)
	}

	return nil
}

// Stop tears the connection down.
func (w *hlWS) Stop() {
	w.mu.Lock()
	client := w.client
	cancel := w.cancel
	w.client = nil
	w.cancel = nil
	w.started = false
	w.mu.Unlock()

	if client != nil {
		if err := client.Close(); err != nil {
			logger.Warnf("[Hyperliquid][WS] close error: %v", err)
		}
	}
	if cancel != nil {
		cancel()
	}
}

// seedKlines backfills a timeframe buffer from the REST candle snapshot.
func (w *hlWS) seedKlines(coin, tf string) {
	now := time.Now().UnixMilli()
	// Ask for slightly more than the buffer so a full window is available.
	start := now - int64(hlWSBufCap)*hlIntervalMillis(tf)

	candles, err := w.trader.exchange.Info().CandlesSnapshot(w.trader.ctx, coin, tf, start, now)
	if err != nil {
		logger.Warnf("[Hyperliquid][WS] seed %s %s failed: %v", coin, tf, err)
		return
	}

	bars := make([]hlCandle, 0, len(candles))
	for _, c := range candles {
		bars = append(bars, decodeHLCandle(c))
	}
	if len(bars) == 0 {
		return
	}

	w.buffersMu.Lock()
	w.buffers[tf] = bars
	w.buffersMu.Unlock()
}

// hlIntervalMillis maps a Hyperliquid interval to milliseconds so seeds can
// cover a full buffer window.
func hlIntervalMillis(tf string) int64 {
	multiplier := int64(60_000)
	switch {
	case tf == "1d":
		return 24 * 60 * 60 * 1000
	case tf == "1w":
		return 7 * 24 * 60 * 60 * 1000
	}

	trimmed := tf
	switch {
	case len(tf) > 1 && tf[len(tf)-1] == 'm':
		trimmed = tf[:len(tf)-1]
	case len(tf) > 1 && tf[len(tf)-1] == 'h':
		trimmed = tf[:len(tf)-1]
		multiplier = 60 * 60 * 1000
	}

	value, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || value <= 0 {
		return 5 * 60 * 1000
	}
	return value * multiplier
}

func decodeHLCandle(c hyperliquid.Candle) hlCandle {
	return hlCandle{
		openTime:  c.TimeOpen,
		closeTime: c.TimeClose,
		open:      parseFloatOrZero(c.Open),
		high:      parseFloatOrZero(c.High),
		low:       parseFloatOrZero(c.Low),
		close:     parseFloatOrZero(c.Close),
		volume:    parseFloatOrZero(c.Volume),
		trades:    c.TradesCount,
	}
}

func parseFloatOrZero(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

// onCandle maintains the buffer for a timeframe and signals a close when the
// primary timeframe rolls over to a new bar.
func (w *hlWS) onCandle(tf string) func(hyperliquid.Candle, error) {
	return func(c hyperliquid.Candle, err error) {
		if err != nil {
			logger.Warnf("[Hyperliquid][WS] %s candle error: %v", tf, err)
			return
		}

		bar := decodeHLCandle(c)
		if bar.openTime <= 0 {
			return
		}

		closed := false

		w.buffersMu.Lock()
		buf := w.buffers[tf]
		if len(buf) == 0 || buf[len(buf)-1].openTime < bar.openTime {
			// A new bar means the previous one just closed — but only count it
			// once we have already observed a live bar, so the first push after
			// seeding does not fire a phantom close.
			closed = w.liveSeen[tf]
			w.liveSeen[tf] = true
			buf = append(buf, bar)
			if len(buf) > hlWSBufCap {
				buf = buf[len(buf)-hlWSBufCap:]
			}
		} else if len(buf) > 0 {
			// Same bar: update in place.
			buf[len(buf)-1] = bar
		}
		w.buffers[tf] = buf
		w.buffersMu.Unlock()

		if !closed {
			return
		}

		w.mu.Lock()
		primary := w.primaryTf
		w.mu.Unlock()

		if tf == primary {
			w.fireKlineClose()
		}
	}
}

// onClearinghouseState converts each position push into the same shape
// GetPositions returns and forwards it.
func (w *hlWS) onClearinghouseState(state hyperliquid.ClearinghouseState, err error) {
	if err != nil {
		logger.Warnf("[Hyperliquid][WS] clearinghouseState error: %v", err)
		return
	}

	positions := positionsFromClearinghouseState(state)
	if len(positions) == 0 {
		return
	}

	w.cbMu.RLock()
	cb := w.onPosition
	w.cbMu.RUnlock()

	if cb != nil {
		cb(positions)
	}
}

func positionsFromClearinghouseState(state hyperliquid.ClearinghouseState) []map[string]interface{} {
	var result []map[string]interface{}

	for _, assetPos := range state.AssetPositions {
		position := assetPos.Position

		posAmt, _ := strconv.ParseFloat(position.Szi, 64)
		if posAmt == 0 {
			continue
		}

		posMap := make(map[string]interface{})
		posMap["symbol"] = position.Coin + "USDT"

		if posAmt > 0 {
			posMap["side"] = "long"
			posMap["positionAmt"] = posAmt
		} else {
			posMap["side"] = "short"
			posMap["positionAmt"] = -posAmt
		}

		var entryPrice, liquidationPx float64
		if position.EntryPx != nil {
			entryPrice, _ = strconv.ParseFloat(*position.EntryPx, 64)
		}
		if position.LiquidationPx != nil {
			liquidationPx, _ = strconv.ParseFloat(*position.LiquidationPx, 64)
		}

		positionValue, _ := strconv.ParseFloat(position.PositionValue, 64)
		unrealizedPnl, _ := strconv.ParseFloat(position.UnrealizedPnl, 64)

		var markPrice float64
		if posAmt != 0 {
			markPrice = positionValue / absFloat(posAmt)
		}

		posMap["entryPrice"] = entryPrice
		posMap["markPrice"] = markPrice
		posMap["unRealizedProfit"] = unrealizedPnl
		posMap["leverage"] = float64(position.Leverage.Value)
		posMap["liquidationPrice"] = liquidationPx

		result = append(result, posMap)
	}

	return result
}

// onOrderUpdates forwards any order lifecycle event.
func (w *hlWS) onOrderUpdates(orders []hyperliquid.WsOrder, err error) {
	if err != nil {
		logger.Warnf("[Hyperliquid][WS] orderUpdates error: %v", err)
		return
	}
	if len(orders) == 0 {
		return
	}

	w.cbMu.RLock()
	cb := w.onOrder
	w.cbMu.RUnlock()

	if cb != nil {
		cb()
	}
}

// onAllMids caches the latest mid for every coin.
func (w *hlWS) onAllMids(mids hyperliquid.AllMids, err error) {
	if err != nil {
		logger.Warnf("[Hyperliquid][WS] allMids error: %v", err)
		return
	}

	parsed := make(map[string]float64, len(mids.Mids))
	for coin, priceStr := range mids.Mids {
		price, err := strconv.ParseFloat(priceStr, 64)
		if err != nil || price <= 0 {
			continue
		}
		parsed[coin] = price
	}
	if len(parsed) == 0 {
		return
	}

	w.midsMu.Lock()
	for coin, price := range parsed {
		w.mids[coin] = price
	}
	w.midsMu.Unlock()
}

// cachedMid returns the WS-cached mid price for a coin, if known.
func (w *hlWS) cachedMid(coin string) (float64, bool) {
	w.midsMu.RLock()
	defer w.midsMu.RUnlock()

	price, ok := w.mids[coin]
	return price, ok
}

func (w *hlWS) fireKlineClose() {
	w.cbMu.RLock()
	cb := w.onKlineClose
	w.cbMu.RUnlock()

	if cb != nil {
		cb()
	}
}

// SetCallbacks wires the grid's event handlers.
func (w *hlWS) SetCallbacks(onPosition func([]map[string]interface{}), onOrder, onKlineClose func()) {
	w.cbMu.Lock()
	defer w.cbMu.Unlock()

	w.onPosition = onPosition
	w.onOrder = onOrder
	w.onKlineClose = onKlineClose
}

// Klines returns the buffered candles for a timeframe as market.Kline values.
func (w *hlWS) Klines(tf string) ([]market.Kline, bool) {
	w.buffersMu.RLock()
	buf := w.buffers[tf]
	w.buffersMu.RUnlock()

	if len(buf) == 0 {
		return nil, false
	}

	klines := make([]market.Kline, 0, len(buf))
	for _, b := range buf {
		klines = append(klines, market.Kline{
			OpenTime:  b.openTime,
			Open:      b.open,
			High:      b.high,
			Low:       b.low,
			Close:     b.close,
			Volume:    b.volume,
			CloseTime: b.closeTime,
			Trades:    b.trades,
		})
	}
	return klines, true
}

// SubscribeCandles adds candle subscriptions for another coin at runtime.
func (w *hlWS) SubscribeCandles(symbol string) {
	w.mu.Lock()
	client := w.client
	coin := convertSymbolToHyperliquid(symbol)
	tfs := append([]string(nil), w.klineTfs...)
	started := w.started
	w.mu.Unlock()

	if !started || client == nil {
		return
	}
	if len(tfs) == 0 {
		tfs = []string{"5m", "4h"}
	}

	for _, tf := range tfs {
		if _, err := client.Candles(
			hyperliquid.CandlesSubscriptionParams{Coin: coin, Interval: tf},
			w.onCandle(tf),
		); err != nil {
			logger.Warnf("[Hyperliquid][WS] failed to subscribe %s candles for %s: %v", tf, coin, err)
		}
	}
}

// StartWS starts the websocket feed for a symbol.
// primaryTf is the timeframe whose candle close fires the kline-close callback.
func (t *HyperliquidTrader) StartWS(symbol string, primaryTf string) error {
	t.wsMu.Lock()
	if t.ws == nil {
		t.ws = newHLWS(t)
	}
	ws := t.ws
	t.wsMu.Unlock()

	if err := ws.Start(symbol, primaryTf); err != nil {
		return err
	}

	logger.Infof("[Hyperliquid][WS] started for %s (trigger %s)", symbol, primaryTf)
	return nil
}

// StopWS stops the websocket feed.
func (t *HyperliquidTrader) StopWS() {
	t.wsMu.Lock()
	ws := t.ws
	t.ws = nil
	t.wsMu.Unlock()

	if ws != nil {
		ws.Stop()
	}
}

// SubscribeSymbol adds another symbol to the live feed at runtime.
func (t *HyperliquidTrader) SubscribeSymbol(symbol string) {
	t.wsMu.Lock()
	ws := t.ws
	t.wsMu.Unlock()

	if ws != nil {
		ws.SubscribeCandles(symbol)
	}
}

// SetWSCallbacks wires event callbacks into the websocket layer.
// onPosition fires on position pushes, onOrder on order events and
// onKlineClose when a primary-timeframe candle closes.
func (t *HyperliquidTrader) SetWSCallbacks(onPosition func([]map[string]interface{}), onOrder, onKlineClose func()) {
	t.wsMu.Lock()
	if t.ws == nil {
		t.ws = newHLWS(t)
	}
	ws := t.ws
	t.wsMu.Unlock()

	ws.SetCallbacks(onPosition, onOrder, onKlineClose)
}

// SetPrimaryKlineTf sets the timeframe whose candle close fires the callback.
func (t *HyperliquidTrader) SetPrimaryKlineTf(tf string) {
	if tf == "" {
		return
	}

	t.wsMu.Lock()
	ws := t.ws
	t.wsMu.Unlock()

	if ws == nil {
		return
	}

	ws.mu.Lock()
	ws.primaryTf = tf
	ws.mu.Unlock()
}

// GetWSKlines returns WS-cached candles converted to market.Kline.
// Returns (nil, false) when the buffer is empty or the feed is not running.
func (t *HyperliquidTrader) GetWSKlines(symbol, tf string) ([]market.Kline, bool) {
	t.wsMu.Lock()
	ws := t.ws
	t.wsMu.Unlock()

	if ws == nil {
		return nil, false
	}
	return ws.Klines(tf)
}

// wsCachedPrice returns the WS-cached mid price for a symbol when available.
func (t *HyperliquidTrader) wsCachedPrice(symbol string) (float64, bool) {
	t.wsMu.Lock()
	ws := t.ws
	t.wsMu.Unlock()

	if ws == nil {
		return 0, false
	}
	return ws.cachedMid(convertSymbolToHyperliquid(symbol))
}

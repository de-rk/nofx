package okx

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"nofx/logger"
	"nofx/trader/types"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	okxWSPublicURL  = "wss://ws.okx.com:8443/ws/v5/public"
	okxWSPrivateURL = "wss://ws.okx.com:8443/ws/v5/private"
)

type okxWSMsg struct {
	Event string          `json:"event"`
	Arg   struct {
		Channel string `json:"channel"`
		InstId  string `json:"instId"`
	} `json:"arg"`
	Data json.RawMessage `json:"data"`
	Code string          `json:"code"`
	Msg  string          `json:"msg"`
}

// OKXWebSocket maintains live-push caches for balance, positions, open orders, and prices.
// REST methods on OKXTrader check these caches first, falling back to REST if not yet populated.
type OKXWebSocket struct {
	apiKey, secretKey, passphrase string

	instIdsMu sync.RWMutex
	instIds   []string

	pubMu    sync.Mutex
	pubConn  *websocket.Conn
	privMu   sync.Mutex
	privConn *websocket.Conn

	ctx    context.Context
	cancel context.CancelFunc

	balanceMu sync.RWMutex
	wsBalance map[string]interface{}
	balanceOk bool

	positionsMu sync.RWMutex
	wsPositions []map[string]interface{}
	positionsOk bool

	ordersMu sync.RWMutex
	wsOrders map[string][]types.OpenOrder // keyed by generic symbol e.g. "HYPEUSDT"
	ordersOk bool

	pricesMu sync.RWMutex
	wsPrices map[string]float64
	pricesOk map[string]bool

	ctValMu sync.RWMutex
	ctVals  map[string]float64 // instId → ctVal for contract→base conversion

	// Per-connection last-received timestamps for heartbeat management.
	// OKX closes connections idle for 30s, so we ping after 20s of silence.
	pubLastRecv  int64 // Unix nanoseconds, updated atomically via sync/atomic
	privLastRecv int64

	// Event callbacks — called after cache is updated. Use non-blocking channel
	// sends in callers to debounce rapid-fire pushes.
	OnPositionUpdate func() // fired on every positions push
	OnOrderEvent     func() // fired on every orders push (fill, cancel, new)
	OnKlineClose     func() // fired when a 5m candle confirms close (confirm=1)

	// Kline (candlestick) buffers — keyed by instId → timeframe → bars.
	// Each buffer holds up to wsKlineMaxBars closed candles plus the current forming one.
	klineMu        sync.RWMutex
	wsKlines        map[string]map[string][]wsKlineBar // instId → tf → bars (oldest first)
	klineTfs        []string                            // timeframes to subscribe (e.g. ["5m", "4h"])
	primaryKlineTf  string                              // triggers OnKlineClose on confirmed close (e.g. "5m")
}

const wsKlineMaxBars = 300 // rolling buffer size per timeframe

// wsKlineBar is a compact OHLCV record from the OKX candle channel.
type wsKlineBar struct {
	Ts        int64   // open time, unix ms
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Vol       float64 // base asset volume
	Confirmed bool    // true once the candle is closed
}

func newOKXWebSocket(apiKey, secretKey, passphrase string, instIds []string) *OKXWebSocket {
	ctx, cancel := context.WithCancel(context.Background())
	return &OKXWebSocket{
		apiKey:    apiKey,
		secretKey: secretKey,
		passphrase: passphrase,
		instIds:   instIds,
		ctx:       ctx,
		cancel:    cancel,
		wsBalance: make(map[string]interface{}),
		wsOrders:  make(map[string][]types.OpenOrder),
		wsPrices:  make(map[string]float64),
		pricesOk:  make(map[string]bool),
		ctVals:    make(map[string]float64),
		wsKlines:  make(map[string]map[string][]wsKlineBar),
	}
}

// SetKlineTfs sets the timeframes to subscribe for candlestick data (e.g. ["5m", "4h"]).
// Must be called before Start().
func (ws *OKXWebSocket) SetKlineTfs(tfs []string) {
	ws.klineTfs = tfs
}

// SeedKlines pre-fills the kline buffer for a given instId and timeframe using
// OKX REST API (public endpoint, no auth required). Called before the WS stream
// has accumulated enough history.
func (ws *OKXWebSocket) SeedKlines(instId, tf string) {
	bar := marketTfToOKXBar(tf)
	url := fmt.Sprintf("https://www.okx.com/api/v5/market/candles?instId=%s&bar=%s&limit=300", instId, bar)

	resp, err := http.Get(url)
	if err != nil {
		logger.Warnf("[OKX WS] kline seed REST failed %s %s: %v", instId, tf, err)
		return
	}
	defer resp.Body.Close()

	var result struct {
		Code string          `json:"code"`
		Data [][]interface{} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || result.Code != "0" {
		logger.Warnf("[OKX WS] kline seed parse failed %s %s: code=%s err=%v", instId, tf, result.Code, err)
		return
	}

	// OKX returns newest first — reverse to oldest-first order
	bars := make([]wsKlineBar, 0, len(result.Data))
	for i := len(result.Data) - 1; i >= 0; i-- {
		row := result.Data[i]
		if len(row) < 6 {
			continue
		}
		b := parseOKXCandleRow(row)
		b.Confirmed = true
		bars = append(bars, b)
	}
	if len(bars) == 0 {
		return
	}

	ws.klineMu.Lock()
	if ws.wsKlines[instId] == nil {
		ws.wsKlines[instId] = make(map[string][]wsKlineBar)
	}
	ws.wsKlines[instId][tf] = bars
	ws.klineMu.Unlock()
	logger.Infof("[OKX WS] kline seed: %s %s %d bars", instId, tf, len(bars))
}

// GetWSKlineBars returns the current kline buffer for a given instId and timeframe.
// Returns nil if no data is available.
func (ws *OKXWebSocket) GetWSKlineBars(instId, tf string) []wsKlineBar {
	ws.klineMu.RLock()
	defer ws.klineMu.RUnlock()
	if ws.wsKlines[instId] == nil {
		return nil
	}
	bars := ws.wsKlines[instId][tf]
	if len(bars) == 0 {
		return nil
	}
	result := make([]wsKlineBar, len(bars))
	copy(result, bars)
	return result
}

func (ws *OKXWebSocket) setCtVal(instId string, ctVal float64) {
	ws.ctValMu.Lock()
	ws.ctVals[instId] = ctVal
	ws.ctValMu.Unlock()
}

func (ws *OKXWebSocket) getCtVal(instId string) float64 {
	ws.ctValMu.RLock()
	v := ws.ctVals[instId]
	ws.ctValMu.RUnlock()
	if v <= 0 {
		return 1.0
	}
	return v
}

// Start dials both WS connections and begins the run loop. Returns quickly; work runs in goroutines.
func (ws *OKXWebSocket) Start() error {
	if err := ws.connect(); err != nil {
		return err
	}
	go ws.runLoop()
	return nil
}

// Stop cancels the context and closes both connections.
func (ws *OKXWebSocket) Stop() {
	ws.cancel()
	ws.pubMu.Lock()
	if ws.pubConn != nil {
		ws.pubConn.Close()
	}
	ws.pubMu.Unlock()
	ws.privMu.Lock()
	if ws.privConn != nil {
		ws.privConn.Close()
	}
	ws.privMu.Unlock()
}

// addSymbol dynamically subscribes a new ticker instId (e.g. "HYPE-USDT-SWAP").
func (ws *OKXWebSocket) addSymbol(instId string) {
	ws.instIdsMu.Lock()
	// Avoid duplicates
	for _, id := range ws.instIds {
		if id == instId {
			ws.instIdsMu.Unlock()
			return
		}
	}
	ws.instIds = append(ws.instIds, instId)
	ws.instIdsMu.Unlock()

	ws.pubMu.Lock()
	if ws.pubConn != nil {
		ws.pubConn.WriteJSON(map[string]interface{}{
			"op":   "subscribe",
			"args": []map[string]string{{"channel": "tickers", "instId": instId}},
		})
	}
	ws.pubMu.Unlock()
}

func (ws *OKXWebSocket) connect() error {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}

	pub, _, err := dialer.DialContext(ws.ctx, okxWSPublicURL, nil)
	if err != nil {
		return fmt.Errorf("OKX public WS dial: %w", err)
	}

	priv, _, err := dialer.DialContext(ws.ctx, okxWSPrivateURL, nil)
	if err != nil {
		pub.Close()
		return fmt.Errorf("OKX private WS dial: %w", err)
	}

	ws.pubMu.Lock()
	ws.pubConn = pub
	ws.pubMu.Unlock()

	ws.privMu.Lock()
	ws.privConn = priv
	ws.privMu.Unlock()

	// Reset activity timestamps so heartbeat doesn't ping immediately on a fresh connection
	now := time.Now().UnixNano()
	atomic.StoreInt64(&ws.pubLastRecv, now)
	atomic.StoreInt64(&ws.privLastRecv, now)

	return ws.authenticate()
}

func (ws *OKXWebSocket) authenticate() error {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	preHash := ts + "GET" + "/users/self/verify"
	h := hmac.New(sha256.New, []byte(ws.secretKey))
	h.Write([]byte(preHash))
	sign := base64.StdEncoding.EncodeToString(h.Sum(nil))

	loginMsg := map[string]interface{}{
		"op": "login",
		"args": []map[string]string{{
			"apiKey":     ws.apiKey,
			"passphrase": ws.passphrase,
			"timestamp":  ts,
			"sign":       sign,
		}},
	}

	ws.privMu.Lock()
	err := ws.privConn.WriteJSON(loginMsg)
	conn := ws.privConn
	ws.privMu.Unlock()
	if err != nil {
		return fmt.Errorf("WS login send: %w", err)
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, raw, err := conn.ReadMessage()
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		return fmt.Errorf("WS login ack: %w", err)
	}

	var ack okxWSMsg
	json.Unmarshal(raw, &ack)
	if ack.Event == "error" {
		return fmt.Errorf("WS login rejected: code=%s msg=%s", ack.Code, ack.Msg)
	}
	logger.Infof("[OKX WS] authenticated")
	return nil
}

func (ws *OKXWebSocket) subscribe() {
	// Private: account + positions + orders
	ws.privMu.Lock()
	if ws.privConn != nil {
		ws.privConn.WriteJSON(map[string]interface{}{
			"op": "subscribe",
			"args": []map[string]interface{}{
				{"channel": "account", "ccy": "USDT"},
				{"channel": "positions", "instType": "SWAP"},
				{"channel": "orders", "instType": "SWAP"},
			},
		})
	}
	ws.privMu.Unlock()

	// Public: tickers + candles per instId
	ws.instIdsMu.RLock()
	ids := make([]string, len(ws.instIds))
	copy(ids, ws.instIds)
	ws.instIdsMu.RUnlock()

	if len(ids) == 0 {
		return
	}

	var pubArgs []map[string]string
	for _, id := range ids {
		pubArgs = append(pubArgs, map[string]string{"channel": "tickers", "instId": id})
		for _, tf := range ws.klineTfs {
			pubArgs = append(pubArgs, map[string]string{
				"channel": marketTfToOKXChannel(tf),
				"instId":  id,
			})
		}
	}
	ws.pubMu.Lock()
	if ws.pubConn != nil {
		ws.pubConn.WriteJSON(map[string]interface{}{"op": "subscribe", "args": pubArgs})
	}
	ws.pubMu.Unlock()
}

func (ws *OKXWebSocket) runLoop() {
	backoff := time.Second
	for {
		select {
		case <-ws.ctx.Done():
			return
		default:
		}

		ws.subscribe()

		pubDone := make(chan struct{})
		privDone := make(chan struct{})
		go ws.readPublic(pubDone)
		go ws.readPrivate(privDone)
		go ws.heartbeat()

		select {
		case <-ws.ctx.Done():
			return
		case <-pubDone:
			logger.Warnf("[OKX WS] public connection dropped — reconnecting in %v", backoff)
		case <-privDone:
			logger.Warnf("[OKX WS] private connection dropped — reconnecting in %v", backoff)
		}

		ws.pubMu.Lock()
		if ws.pubConn != nil {
			ws.pubConn.Close()
		}
		ws.pubMu.Unlock()
		ws.privMu.Lock()
		if ws.privConn != nil {
			ws.privConn.Close()
		}
		ws.privMu.Unlock()

		// Drain both readers
		<-pubDone
		<-privDone

		select {
		case <-ws.ctx.Done():
			return
		case <-time.After(backoff):
		}

		if err := ws.connect(); err != nil {
			logger.Errorf("[OKX WS] reconnect failed: %v", err)
			if backoff < 60*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		logger.Infof("[OKX WS] reconnected")
	}
}

// heartbeat implements OKX's recommended keep-alive strategy:
// - Check every 5s whether either connection has been silent for 20s
// - If silent, send "ping" and expect a "pong" within 10s
// - If no pong arrives within 10s, close the connection to trigger reconnect
func (ws *OKXWebSocket) heartbeat() {
	const (
		silenceThreshold = 20 * time.Second // send ping after this much silence
		pongTimeout      = 10 * time.Second // close connection if no response
		checkInterval    = 5 * time.Second
	)
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ws.ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()

			// Public connection check
			pubLast := time.Unix(0, atomic.LoadInt64(&ws.pubLastRecv))
			if now.Sub(pubLast) >= silenceThreshold {
				ws.pubMu.Lock()
				conn := ws.pubConn
				ws.pubMu.Unlock()
				if conn != nil {
					conn.WriteMessage(websocket.TextMessage, []byte("ping"))
					// Set a read deadline on the connection — if no pong arrives in time,
					// the next ReadMessage in readPublic will error and trigger reconnect.
					conn.SetReadDeadline(time.Now().Add(pongTimeout))
					logger.Infof("[OKX WS] sent ping to public connection (silent for %.0fs)", now.Sub(pubLast).Seconds())
				}
			}

			// Private connection check
			privLast := time.Unix(0, atomic.LoadInt64(&ws.privLastRecv))
			if now.Sub(privLast) >= silenceThreshold {
				ws.privMu.Lock()
				conn := ws.privConn
				ws.privMu.Unlock()
				if conn != nil {
					conn.WriteMessage(websocket.TextMessage, []byte("ping"))
					conn.SetReadDeadline(time.Now().Add(pongTimeout))
					logger.Infof("[OKX WS] sent ping to private connection (silent for %.0fs)", now.Sub(privLast).Seconds())
				}
			}
		}
	}
}

func (ws *OKXWebSocket) readPublic(done chan struct{}) {
	defer close(done)
	ws.pubMu.Lock()
	conn := ws.pubConn
	ws.pubMu.Unlock()
	if conn == nil {
		return
	}
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		atomic.StoreInt64(&ws.pubLastRecv, time.Now().UnixNano())
		if string(raw) == "pong" {
			// Clear the read deadline that was set when we sent "ping"
			conn.SetReadDeadline(time.Time{})
			continue
		}
		var msg okxWSMsg
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		if msg.Arg.Channel == "tickers" && msg.Data != nil {
			ws.handleTickers(msg.Arg.InstId, msg.Data)
		} else if strings.HasPrefix(msg.Arg.Channel, "candle") && msg.Data != nil {
			ws.handleCandle(msg.Arg.Channel, msg.Arg.InstId, msg.Data)
		}
	}
}

func (ws *OKXWebSocket) readPrivate(done chan struct{}) {
	defer close(done)
	ws.privMu.Lock()
	conn := ws.privConn
	ws.privMu.Unlock()
	if conn == nil {
		return
	}
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		atomic.StoreInt64(&ws.privLastRecv, time.Now().UnixNano())
		if string(raw) == "pong" {
			conn.SetReadDeadline(time.Time{})
			continue
		}
		var msg okxWSMsg
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		switch msg.Arg.Channel {
		case "account":
			if msg.Data != nil {
				ws.handleAccount(msg.Data)
			}
		case "positions":
			if msg.Data != nil {
				ws.handlePositions(msg.Data)
			}
		case "orders":
			if msg.Data != nil {
				ws.handleOrders(msg.Data)
			}
		}
	}
}

// ── Cache handlers ────────────────────────────────────────────────────────────

// marketTfToOKXBar converts market package timeframe strings to OKX bar param.
// e.g. "5m" → "5m", "4h" → "4H", "1h" → "1H"
func marketTfToOKXBar(tf string) string {
	switch tf {
	case "1m":
		return "1m"
	case "3m":
		return "3m"
	case "5m":
		return "5m"
	case "15m":
		return "15m"
	case "30m":
		return "30m"
	case "1h":
		return "1H"
	case "2h":
		return "2H"
	case "4h":
		return "4H"
	case "6h":
		return "6H"
	case "12h":
		return "12H"
	case "1d":
		return "1D"
	default:
		return tf
	}
}

// marketTfToOKXChannel converts timeframe to OKX WS channel name.
// e.g. "5m" → "candle5m", "4h" → "candle4H"
func marketTfToOKXChannel(tf string) string {
	return "candle" + marketTfToOKXBar(tf)
}

func parseOKXCandleRow(row []interface{}) wsKlineBar {
	parseStr := func(v interface{}) float64 {
		if s, ok := v.(string); ok {
			f, _ := strconv.ParseFloat(s, 64)
			return f
		}
		return 0
	}
	ts, _ := strconv.ParseInt(fmt.Sprintf("%v", row[0]), 10, 64)
	return wsKlineBar{
		Ts:    ts,
		Open:  parseStr(row[1]),
		High:  parseStr(row[2]),
		Low:   parseStr(row[3]),
		Close: parseStr(row[4]),
		Vol:   parseStr(row[5]),
	}
}

func (ws *OKXWebSocket) handleCandle(channel, instId string, raw json.RawMessage) {
	// OKX candle data: array of arrays, each: [ts, o, h, l, c, vol, volCcy, volCcyQuote, confirm]
	var rows [][]interface{}
	if err := json.Unmarshal(raw, &rows); err != nil || len(rows) == 0 {
		return
	}

	// Extract timeframe from channel name: "candle5m" → "5m", "candle4H" → "4h"
	tf := strings.ToLower(strings.TrimPrefix(channel, "candle"))

	ws.klineMu.Lock()
	defer ws.klineMu.Unlock()

	if ws.wsKlines[instId] == nil {
		ws.wsKlines[instId] = make(map[string][]wsKlineBar)
	}
	buf := ws.wsKlines[instId][tf]

	for _, row := range rows {
		if len(row) < 6 {
			continue
		}
		bar := parseOKXCandleRow(row)
		confirm := false
		if len(row) >= 9 {
			confirm = fmt.Sprintf("%v", row[8]) == "1"
		}
		bar.Confirmed = confirm

		if len(buf) > 0 && buf[len(buf)-1].Ts == bar.Ts {
			buf[len(buf)-1] = bar
		} else {
			buf = append(buf, bar)
			if len(buf) > wsKlineMaxBars {
				buf = buf[len(buf)-wsKlineMaxBars:]
			}
		}
		// Fire OnKlineClose on confirmed close — OKX only sends confirm=1 once per candle
		if confirm && tf == ws.primaryKlineTf && ws.OnKlineClose != nil {
			ws.OnKlineClose()
		}
	}
	ws.wsKlines[instId][tf] = buf
}

func (ws *OKXWebSocket) handleTickers(instId string, raw json.RawMessage) {
	var tickers []struct {
		Last string `json:"last"`
	}
	if err := json.Unmarshal(raw, &tickers); err != nil || len(tickers) == 0 {
		return
	}
	price, err := strconv.ParseFloat(tickers[0].Last, 64)
	if err != nil || price <= 0 {
		return
	}
	symbol := instIdToSymbol(instId)
	ws.pricesMu.Lock()
	ws.wsPrices[symbol] = price
	ws.pricesOk[symbol] = true
	ws.pricesMu.Unlock()
}

func (ws *OKXWebSocket) handleAccount(raw json.RawMessage) {
	var accounts []struct {
		TotalEq string `json:"totalEq"`
		Details []struct {
			Ccy     string `json:"ccy"`
			CashBal string `json:"cashBal"`
			AvailEq string `json:"availEq"` // WS uses availEq; REST uses availBal
			UPL     string `json:"upl"`
		} `json:"details"`
	}
	if err := json.Unmarshal(raw, &accounts); err != nil || len(accounts) == 0 {
		return
	}
	acc := accounts[0]
	var cashBal, availEq, upl float64
	for _, d := range acc.Details {
		if d.Ccy == "USDT" {
			cashBal, _ = strconv.ParseFloat(d.CashBal, 64)
			availEq, _ = strconv.ParseFloat(d.AvailEq, 64)
			upl, _ = strconv.ParseFloat(d.UPL, 64)
			break
		}
	}
	totalEq, _ := strconv.ParseFloat(acc.TotalEq, 64)

	ws.balanceMu.Lock()
	ws.wsBalance = map[string]interface{}{
		"totalEquity":           totalEq,
		"totalWalletBalance":    cashBal,
		"availableBalance":      availEq,
		"totalUnrealizedProfit": upl,
	}
	ws.balanceOk = true
	ws.balanceMu.Unlock()
}

func (ws *OKXWebSocket) handlePositions(raw json.RawMessage) {
	var positions []struct {
		InstId  string `json:"instId"`
		PosSide string `json:"posSide"`
		Pos     string `json:"pos"`
		AvgPx   string `json:"avgPx"`
		MarkPx  string `json:"markPx"`
		Upl     string `json:"upl"`
		Lever   string `json:"lever"`
		LiqPx   string `json:"liqPx"`
		MgnMode string `json:"mgnMode"`
		CTime   string `json:"cTime"`
		UTime   string `json:"uTime"`
	}
	if err := json.Unmarshal(raw, &positions); err != nil {
		return
	}

	var result []map[string]interface{}
	for _, pos := range positions {
		contracts, _ := strconv.ParseFloat(pos.Pos, 64)
		if contracts == 0 {
			continue
		}
		if contracts < 0 {
			contracts = -contracts
		}
		ctVal := ws.getCtVal(pos.InstId)
		posAmt := contracts * ctVal

		entry, _ := strconv.ParseFloat(pos.AvgPx, 64)
		mark, _ := strconv.ParseFloat(pos.MarkPx, 64)
		upl, _ := strconv.ParseFloat(pos.Upl, 64)
		lever, _ := strconv.ParseFloat(pos.Lever, 64)
		liqPx, _ := strconv.ParseFloat(pos.LiqPx, 64)
		cTime, _ := strconv.ParseInt(pos.CTime, 10, 64)
		uTime, _ := strconv.ParseInt(pos.UTime, 10, 64)

		side := "long"
		if pos.PosSide == "short" {
			side = "short"
		}
		mgnMode := pos.MgnMode
		if mgnMode == "" {
			mgnMode = "cross"
		}

		result = append(result, map[string]interface{}{
			"symbol":           instIdToSymbol(pos.InstId),
			"positionAmt":      posAmt,
			"entryPrice":       entry,
			"markPrice":        mark,
			"unRealizedProfit": upl,
			"leverage":         lever,
			"liquidationPrice": liqPx,
			"side":             side,
			"mgnMode":          mgnMode,
			"createdTime":      cTime,
			"updatedTime":      uTime,
		})
	}

	ws.positionsMu.Lock()
	ws.wsPositions = result
	ws.positionsOk = true
	ws.positionsMu.Unlock()
	if ws.OnPositionUpdate != nil {
		ws.OnPositionUpdate()
	}
}

// handleOrders processes individual order events (not a full snapshot).
// Algo orders (stop/TP) are NOT included — they come via the algo-orders channel.
// GetOpenOrders falls back to REST when ordersOk is false for this reason.
func (ws *OKXWebSocket) handleOrders(raw json.RawMessage) {
	var orders []struct {
		OrdId   string `json:"ordId"`
		InstId  string `json:"instId"`
		Side    string `json:"side"`
		PosSide string `json:"posSide"`
		OrdType string `json:"ordType"`
		Px      string `json:"px"`
		Sz      string `json:"sz"`
		State   string `json:"state"` // live, partially_filled, filled, canceled
	}
	if err := json.Unmarshal(raw, &orders); err != nil {
		return
	}

	ws.ordersMu.Lock()
	defer ws.ordersMu.Unlock()

	for _, o := range orders {
		symbol := instIdToSymbol(o.InstId)
		ctVal := ws.getCtVal(o.InstId)
		sz, _ := strconv.ParseFloat(o.Sz, 64)
		px, _ := strconv.ParseFloat(o.Px, 64)
		qty := sz * ctVal

		side := strings.ToUpper(o.Side)
		posSide := strings.ToUpper(o.PosSide)
		if posSide == "NET" || posSide == "" {
			posSide = "BOTH"
		}

		if o.State == "filled" || o.State == "canceled" {
			existing := ws.wsOrders[symbol]
			filtered := existing[:0]
			for _, ord := range existing {
				if ord.OrderID != o.OrdId {
					filtered = append(filtered, ord)
				}
			}
			ws.wsOrders[symbol] = filtered
			continue
		}

		newOrder := types.OpenOrder{
			OrderID:      o.OrdId,
			Symbol:       symbol,
			Side:         side,
			PositionSide: posSide,
			Type:         strings.ToUpper(o.OrdType),
			Price:        px,
			Quantity:     qty,
			Status:       "NEW",
		}
		existing := ws.wsOrders[symbol]
		found := false
		for i, ord := range existing {
			if ord.OrderID == o.OrdId {
				existing[i] = newOrder
				found = true
				break
			}
		}
		if !found {
			ws.wsOrders[symbol] = append(existing, newOrder)
		}
	}
	ws.ordersOk = true
	if ws.OnOrderEvent != nil {
		ws.OnOrderEvent()
	}
}

// instIdToSymbol converts "HYPE-USDT-SWAP" → "HYPEUSDT"
func instIdToSymbol(instId string) string {
	parts := strings.Split(instId, "-")
	if len(parts) >= 2 {
		return parts[0] + parts[1]
	}
	return instId
}

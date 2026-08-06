package backtest

import (
	"nofx/market"
)

// sideState tracks one side's (long or short) accumulated grid position.
type sideState struct {
	Qty                float64 // base-asset size, always >= 0
	EntryPrice         float64 // volume-weighted average fill price
	AlreadyReduced     float64 // highest profit-reduce step already applied, resets to 0 when profit <= 0
	PeakProfitPct      float64 // highest profit% observed since the position was last flat (drives drawdown-close)
	ReduceCount        int
	TTradeReduceCount  int
	DrawdownCloseCount int
	SmallCloseCount    int
}

func (s *sideState) addFill(qty, price float64) {
	if s.Qty+qty <= 0 {
		return
	}
	s.EntryPrice = (s.EntryPrice*s.Qty + price*qty) / (s.Qty + qty)
	s.Qty += qty
}

// unrealizedPnL and margin follow the same formulas as
// trader.checkProfitReduce: margin = notional / leverage,
// pnl = (mark-entry)*qty for long, (entry-mark)*qty for short.
func (s *sideState) unrealizedPnL(mark float64, isLong bool) float64 {
	if s.Qty == 0 {
		return 0
	}
	if isLong {
		return (mark - s.EntryPrice) * s.Qty
	}
	return (s.EntryPrice - mark) * s.Qty
}

func (s *sideState) margin(leverage int) float64 {
	if s.Qty == 0 || leverage <= 0 {
		return 0
	}
	return s.Qty * s.EntryPrice / float64(leverage)
}

// applyProfitReduce ports trader.checkProfitReduce's per-side step logic.
// Returns the realized PnL freed by the reduce (added to cash, removed from
// the floating position, net of the simulated trading fee), the fee itself,
// and whether a reduce fired this bar.
func applyProfitReduce(s *sideState, mark float64, isLong bool, leverage int, stepPct, multiplier, feePct float64) (realizedPnL, feePaid float64, fired bool) {
	if s.Qty == 0 || stepPct <= 0 {
		return 0, 0, false
	}
	margin := s.margin(leverage)
	if margin <= 0 {
		return 0, 0, false
	}
	pnl := s.unrealizedPnL(mark, isLong)
	pct := profitPct(s, mark, isLong, leverage)

	if pct <= 0 {
		s.AlreadyReduced = 0
		return 0, 0, false
	}

	targetStep := stepFloor(pct, stepPct)
	if targetStep <= s.AlreadyReduced {
		return 0, 0, false
	}

	mult := multiplier
	if mult <= 0 {
		mult = 1.0
	}
	reduceQty := s.Qty * (targetStep / 100) * mult
	if reduceQty > s.Qty {
		reduceQty = s.Qty
	}
	if reduceQty <= 0 {
		return 0, 0, false
	}

	// Realize PnL proportional to the reduced fraction, then shrink the
	// position. EntryPrice is unchanged (matches real reduce-only orders,
	// which close part of the position at the current mark price).
	fraction := reduceQty / s.Qty
	realized := pnl * fraction
	fee := reduceQty * mark * feePct / 100
	s.Qty -= reduceQty
	s.AlreadyReduced = targetStep
	s.ReduceCount++
	return realized - fee, fee, true
}

func stepFloor(profitPct, step float64) float64 {
	steps := int(profitPct / step)
	return float64(steps) * step
}

// profitPct computes the same profit percentage formula used by both
// trader.checkProfitReduce (pnl/margin*100) and trader.checkPositionDrawdown
// ((mark-entry)/entry*leverage*100) — they're algebraically identical:
// pnl/margin = (mark-entry)*qty / (qty*entry/leverage) = (mark-entry)/entry*leverage.
func profitPct(s *sideState, mark float64, isLong bool, leverage int) float64 {
	margin := s.margin(leverage)
	if margin <= 0 {
		return 0
	}
	return s.unrealizedPnL(mark, isLong) / margin * 100
}

// closeSide fully closes a side's position at mark price, returning the
// realized PnL (net of the simulated trading fee) and the fee itself, and
// resets all per-side tracking (stepped-reduce progress, peak profit) so a
// fresh position starts from a clean slate — matching live behavior where a
// full close (profit_drawdown_close, profit_reduce_close) clears the
// corresponding tracker/cache entry.
func closeSide(s *sideState, mark float64, isLong bool, feePct float64) (realizedPnL, feePaid float64) {
	if s.Qty == 0 {
		return 0, 0
	}
	pnl := s.unrealizedPnL(mark, isLong)
	fee := s.Qty * mark * feePct / 100
	s.Qty = 0
	s.EntryPrice = 0
	s.AlreadyReduced = 0
	s.PeakProfitPct = 0
	return pnl - fee, fee
}

// applyRiskChecks runs, in priority order, the three profit-taking/closing
// mechanisms that can fire on a side each bar: peak-drawdown full close
// (trader.checkPositionDrawdown), small-notional full close (the early-exit
// branch in trader.checkProfitReduce), and — only if neither of those
// fired — the stepped partial reduce (applyProfitReduce). At most one of
// them acts per side per bar, mirroring how a full close makes the
// finer-grained mechanisms moot for that side this cycle. Returns the
// realized PnL (net of fees) and the fee paid, if any of the three fired.
func applyRiskChecks(s *sideState, mark float64, isLong bool, leverage int, stepPct, multiplier, drawdownThresholdPct float64, enableSmallClose bool, feePct float64) (realizedPnL, feePaid float64) {
	if s.Qty == 0 {
		return 0, 0
	}
	pct := profitPct(s, mark, isLong, leverage)
	if pct > s.PeakProfitPct {
		s.PeakProfitPct = pct
	}

	// 1. Peak-drawdown full close: only once profit exceeded 5% and has
	// since pulled back drawdownThresholdPct percentage points off its own
	// peak. drawdownThresholdPct<=0 disables the check (matches the live
	// GridConfig.ProfitDrawdownThreshold==0 "disabled" convention).
	if drawdownThresholdPct > 0 && pct > 5.0 && s.PeakProfitPct > 0 && pct < s.PeakProfitPct {
		ddPct := (s.PeakProfitPct - pct) / s.PeakProfitPct * 100
		if ddPct >= drawdownThresholdPct {
			s.DrawdownCloseCount++
			return closeSide(s, mark, isLong, feePct)
		}
	}

	// 2. Small-notional full close: profit exceeds 2x the reduce step and
	// the remaining position's notional value has shrunk under $100 — not
	// worth trickling down further, just close it.
	if enableSmallClose {
		step := stepPct
		if step <= 0 {
			step = 10.0
		}
		notional := s.Qty * mark
		if pct > step*2 && notional < 100 {
			s.SmallCloseCount++
			return closeSide(s, mark, isLong, feePct)
		}
	}

	// 3. Stepped partial reduce (unchanged from before this feature).
	realized, fee, _ := applyProfitReduce(s, mark, isLong, leverage, stepPct, multiplier, feePct)
	return realized, fee
}

// ttradeActivationThreshold applies the live default (30%) when the
// configured threshold is <= 0, matching trader.buildTTradeContext.
func ttradeActivationThreshold(configured float64) float64 {
	if configured <= 0 {
		return 30.0
	}
	return configured
}

// ttradeSpread applies the live floor (0.2%) on the configured spread,
// matching trader.placeTTradeReduceOrder.
func ttradeSpread(configured float64) float64 {
	if configured < 0.2 {
		return 0.2
	}
	return configured
}

// updateTTradeTags ports trader.ttradeTagOrders' tag/untag decision (not its
// timeout handling, which doesn't apply to instantaneous bar-by-bar
// simulation): every still-pending (unfilled) level on an active side gets
// tagged if it qualifies (buy below current price, sell above); every
// tagged-but-still-pending level on a side that's no longer active gets
// untagged, so a later fill is treated as an ordinary grid fill again.
func updateTTradeTags(levels []simLevel, currentPrice float64, longActive, shortActive bool) {
	for i := range levels {
		lv := &levels[i]
		if lv.Filled {
			continue
		}
		if lv.Side == "buy" {
			if longActive && lv.Price <= currentPrice {
				lv.TTradeTagged = true
			} else if !longActive {
				lv.TTradeTagged = false
			}
		} else {
			if shortActive && lv.Price >= currentPrice {
				lv.TTradeTagged = true
			} else if !shortActive {
				lv.TTradeTagged = false
			}
		}
	}
}

// crossMarginMaintenanceRate approximates a cross-margin account's blended
// maintenance margin rate as a flat percentage of total notional (long +
// short), rather than modeling an exchange's tiered maintenance-margin
// schedule (which varies by notional bracket and gets stricter at higher
// leverage). This is a simplification: real cross-margin liquidation is
// tiered and leverage-dependent, but a flat rate is enough to catch grid
// parameter combinations (especially high leverage) that would blow up an
// account well before its equity reaches zero. 0.5% matches OKX's typical
// maintenance margin rate for major pairs at low-to-mid leverage.
const crossMarginMaintenanceRate = 0.005

// pendingTTradeReduce is a T-trade reduce order awaiting fill, tied back to
// the grid level whose tagged fill spawned it so that level can be freed
// (Filled=false) once the reduce itself fills — approximating
// trader.ttradeRepairOrders freeing the level and ttradeSupplementOrder
// immediately re-tagging a fresh prep, by letting the same price level
// become fillable again rather than opening a new level elsewhere.
type pendingTTradeReduce struct {
	ttradeReduceOrder
	LevelIndex int
}

// Simulate runs one backtest pass over klines (ascending time order) for the
// given parameter set, starting at klines[startIdx]. Bars before startIdx are
// used only to seed the ATR14 lookback window — they are never used to size
// or fill the grid, avoiding lookahead.
//
// Fill model: a level's limit order is considered filled the first bar whose
// [Low, High] range covers the level's price. This is the standard
// simplification for grid backtesting in the absence of exchange order-book
// data; it does not model partial fills or maker queue position. Trading
// fees (GridParams.FeePct) ARE modeled — a flat rate charged on every fill's
// notional. Equity/drawdown are evaluated on each bar's Close, so intrabar
// spikes through a level (not settled by Close) are not reflected in the
// drawdown figure — a known limitation, not a bug.
//
// Liquidation model: cross-margin, checked once per bar against the
// account's combined long+short notional (see crossMarginMaintenanceRate).
// This replaces a plain equity<=0 check, which was far more permissive than
// any real exchange's liquidation threshold and would let a backtest report
// a "surviving" account that would have actually been liquidated.
//
// Per-bar order of operations (see inline steps below): T-trade
// activation/tagging is evaluated first using the position as of the end of
// the previous bar (matching live buildTTradeContext/ttradeTagOrders running
// on the position snapshot before that cycle's fills); then normal level
// fills (which also spawn a T-trade reduce order if the filled level was
// tagged); then T-trade reduce-order fills (which realize PnL against the
// side's current VWAP entry price — an approximation, since this simulator
// doesn't do per-lot FIFO accounting — and free the originating level); then
// the peak-drawdown / small-notional / stepped-reduce checks, in that
// priority order (applyRiskChecks); then the liquidation + drawdown check.
func Simulate(klines []market.Kline, startIdx int, totalInvestment float64, p GridParams) SimResult {
	if startIdx >= len(klines) {
		return SimResult{}
	}
	firstBar := klines[startIdx]
	atr14Series := atrSeries(klines) // O(n) once, replaces the old O(n) per-call atrAt(klines, i)
	atr14 := atr14Series[startIdx]
	upper, lower := computeBounds(firstBar.Close, atr14, p)
	levels, _, err := buildLevels(firstBar.Close, upper, lower, totalInvestment, p)
	if err != nil {
		return SimResult{BlewUp: true, Score: -1e9}
	}
	for i := range levels {
		notional := levels[i].AllocatedUSD * float64(p.Leverage)
		if levels[i].Price > 0 {
			levels[i].Qty = notional / levels[i].Price
		}
	}

	var long, short sideState
	var pendingReduces []pendingTTradeReduce
	cashReleased := 0.0 // realized PnL from all profit-taking/close/reduce mechanisms, added back to equity
	peakEquity := totalInvestment
	maxDrawdownPct := 0.0
	filledCount := 0
	totalFees := 0.0
	gridResets := 0
	ttradeThreshold := ttradeActivationThreshold(p.TTradePositionThresholdPct)
	ttradeSpreadPct := ttradeSpread(p.TTradeSpreadPct)

	equityAt := func(mark float64) float64 {
		return totalInvestment + cashReleased + long.unrealizedPnL(mark, true) + short.unrealizedPnL(mark, false)
	}

	for i := startIdx; i < len(klines); i++ {
		bar := klines[i]

		// 1. T-trade activation + tagging, evaluated on the position as it
		// stood at the end of the previous bar (before this bar's fills).
		if p.EnableTTrade {
			longActive := long.margin(p.Leverage)/totalInvestment*100 >= ttradeThreshold
			shortActive := short.margin(p.Leverage)/totalInvestment*100 >= ttradeThreshold
			updateTTradeTags(levels, bar.Close, longActive, shortActive)
		}

		// 2. Normal level fill detection. A tagged level's fill additionally
		// spawns a reduce-only order at fillPrice ± spread.
		for li := range levels {
			lv := &levels[li]
			if lv.Filled {
				continue
			}
			if bar.Low <= lv.Price && lv.Price <= bar.High {
				lv.Filled = true
				filledCount++
				notional := lv.Qty * lv.Price
				fee := notional * p.FeePct / 100
				cashReleased -= fee
				totalFees += fee
				if lv.Side == "buy" {
					long.addFill(lv.Qty, lv.Price)
				} else {
					short.addFill(lv.Qty, lv.Price)
				}
				if p.EnableTTrade && lv.TTradeTagged {
					isLong := lv.Side == "buy"
					reducePrice := lv.Price * (1 + ttradeSpreadPct/100)
					if !isLong {
						reducePrice = lv.Price * (1 - ttradeSpreadPct/100)
					}
					pendingReduces = append(pendingReduces, pendingTTradeReduce{
						ttradeReduceOrder: ttradeReduceOrder{Price: reducePrice, Qty: lv.Qty, IsLong: isLong},
						LevelIndex:        li,
					})
				}
			}
		}

		// 3. T-trade reduce-order fill detection. Realizes PnL against the
		// side's current VWAP entry price (see doc comment above) and frees
		// the originating level so it can be re-tagged/re-filled.
		if len(pendingReduces) > 0 {
			remaining := pendingReduces[:0]
			for _, r := range pendingReduces {
				if bar.Low <= r.Price && r.Price <= bar.High {
					side := &short
					if r.IsLong {
						side = &long
					}
					qty := r.Qty
					if qty > side.Qty {
						qty = side.Qty
					}
					if qty > 0 {
						var realized float64
						if r.IsLong {
							realized = (r.Price - side.EntryPrice) * qty
						} else {
							realized = (side.EntryPrice - r.Price) * qty
						}
						fee := qty * r.Price * p.FeePct / 100
						side.Qty -= qty
						cashReleased += realized - fee
						totalFees += fee
						side.TTradeReduceCount++
					}
					if r.LevelIndex < len(levels) {
						levels[r.LevelIndex].Filled = false
						levels[r.LevelIndex].TTradeTagged = false
					}
					continue
				}
				remaining = append(remaining, r)
			}
			pendingReduces = remaining
		}

		// 4. Peak-drawdown close / small-notional close / stepped profit-reduce,
		// in priority order (see applyRiskChecks).
		longPnL, longFee := applyRiskChecks(&long, bar.Close, true, p.Leverage, p.ProfitReduceStepPct, p.ProfitReduceMultiplier, p.ProfitDrawdownThresholdPct, p.EnableSmallPositionClose, p.FeePct)
		shortPnL, shortFee := applyRiskChecks(&short, bar.Close, false, p.Leverage, p.ProfitReduceStepPct, p.ProfitReduceMultiplier, p.ProfitDrawdownThresholdPct, p.EnableSmallPositionClose, p.FeePct)
		cashReleased += longPnL + shortPnL
		totalFees += longFee + shortFee

		// 4.5. Grid-skew auto-reset (ports trader.autoAdjustGrid/resetGrid),
		// run once per bar after this bar's fills/reduces settle — matching
		// live, where autoAdjustGrid runs at the end of the cycle via
		// syncExchangeState's post-checks. Skipped while a T-trade reduce is
		// in flight: maybeResetGrid rebuilds `levels` from scratch, and a
		// pendingReduces entry's LevelIndex would then point at an unrelated
		// level in the new array — live avoids the equivalent problem by
		// protecting (not touching) in-flight reduce orders across a reset,
		// which this simplified model approximates by just deferring the
		// reset a bar rather than replicating that per-order protection.
		if len(pendingReduces) == 0 {
			atr14Now := atr14Series[i]
			var reset bool
			levels, reset = maybeResetGrid(levels, bar.Close, atr14Now, totalInvestment, p)
			if reset {
				gridResets++
			}
		}

		// 5. Cross-margin liquidation check + equity/drawdown tracking.
		// Notional is marked at the current bar's close (not entry price) —
		// maintenance margin scales with current position value, same as a
		// real exchange.
		equity := equityAt(bar.Close)
		totalNotional := long.Qty*bar.Close + short.Qty*bar.Close
		maintenanceMargin := totalNotional * crossMarginMaintenanceRate
		if equity <= maintenanceMargin {
			return SimResult{
				FinalEquity:         0,
				ReturnPct:           -100,
				MaxDrawdownPct:      100,
				FilledLevels:        filledCount,
				LongReduces:         long.ReduceCount,
				ShortReduces:        short.ReduceCount,
				TTradeReduces:       long.TTradeReduceCount + short.TTradeReduceCount,
				DrawdownCloses:      long.DrawdownCloseCount + short.DrawdownCloseCount,
				SmallPositionCloses: long.SmallCloseCount + short.SmallCloseCount,
				TotalFeesPaid:       totalFees,
				GridResets:          gridResets,
				BlewUp:              true,
				Score:               -1e9,
			}
		}
		if equity > peakEquity {
			peakEquity = equity
		} else if peakEquity > 0 {
			dd := (peakEquity - equity) / peakEquity * 100
			if dd > maxDrawdownPct {
				maxDrawdownPct = dd
			}
		}
	}

	finalEquity := equityAt(klines[len(klines)-1].Close)
	returnPct := (finalEquity - totalInvestment) / totalInvestment * 100

	return SimResult{
		FinalEquity:         finalEquity,
		ReturnPct:           returnPct,
		MaxDrawdownPct:      maxDrawdownPct,
		FilledLevels:        filledCount,
		LongReduces:         long.ReduceCount,
		ShortReduces:        short.ReduceCount,
		TTradeReduces:       long.TTradeReduceCount + short.TTradeReduceCount,
		DrawdownCloses:      long.DrawdownCloseCount + short.DrawdownCloseCount,
		SmallPositionCloses: long.SmallCloseCount + short.SmallCloseCount,
		TotalFeesPaid:       totalFees,
		GridResets:          gridResets,
		BlewUp:              false,
		Score:               Score(returnPct, maxDrawdownPct, p.ScoreMode),
	}
}

// Score is the annealing objective: reward return, penalize drawdown.
// mode == "return_focused" uses a much lighter drawdown penalty so the
// search chases raw return; anything else (including "") is "balanced",
// the original heavier-penalty behavior. Blown-up runs are excluded from
// either mode by a separate, fixed -1e9 penalty applied at the call site
// in Simulate() (not here), so "return_focused" still won't favor a
// combination that wipes the account — it just tolerates more drawdown
// short of that.
func Score(returnPct, maxDrawdownPct float64, mode string) float64 {
	drawdownPenalty := 1.5
	if mode == "return_focused" {
		drawdownPenalty = 0.3
	}
	return returnPct - drawdownPenalty*maxDrawdownPct
}

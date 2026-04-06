package kernel

import (
	"encoding/json"
	"fmt"
	"nofx/logger"
	"nofx/market"
	"nofx/mcp"
	"nofx/store"
	"strings"
	"time"
)

// DecisionSummary is a condensed version of a decision for memory
type DecisionSummary struct {
	Timestamp string  `json:"timestamp"`
	Action    string  `json:"action"`
	Reasoning string  `json:"reasoning"`
	Price     float64 `json:"price"`
}

// GetGridDecisions calls the AI client to get decisions for grid trading
func GetGridDecisions(ctx *GridContext, mcpClient mcp.AIClient, strategyConfig *store.StrategyConfig, lang string) (*FullDecision, error) {
	startTime := time.Now()

	systemPrompt := BuildGridSystemPrompt(strategyConfig, lang)
	userPrompt := BuildGridUserPrompt(ctx, lang)

	logger.Infof("🤖 [Grid] Calling AI for grid decisions...")

	response, err := mcpClient.CallWithMessages(systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("AI call failed: %w", err)
	}

	decisions, err := parseGridDecisions(response, ctx.Symbol)
	if err != nil {
		logger.Warnf("Failed to parse grid decisions: %v", err)
		decisions = []Decision{{
			Symbol:     ctx.Symbol,
			Action:     "hold",
			Confidence: 50,
			Reasoning:  "Failed to parse AI response, holding current state",
		}}
	}

	duration := time.Since(startTime).Milliseconds()
	logger.Infof("⏱️ [Grid] AI call duration: %d ms, decisions: %d", duration, len(decisions))

	cotTrace := extractCoTTrace(response)

	return &FullDecision{
		SystemPrompt:        systemPrompt,
		UserPrompt:          userPrompt,
		CoTTrace:            cotTrace,
		Decisions:           decisions,
		RawResponse:         response,
		AIRequestDurationMs: duration,
		Timestamp:           time.Now(),
	}, nil
}

// parseGridDecisions parses AI response into grid decisions
func parseGridDecisions(response string, symbol string) ([]Decision, error) {
	jsonStr := extractJSONArray(response)
	if jsonStr == "" {
		return nil, fmt.Errorf("no JSON array found in response")
	}

	var decisions []Decision
	if err := json.Unmarshal([]byte(jsonStr), &decisions); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	for i := range decisions {
		if decisions[i].Symbol == "" {
			decisions[i].Symbol = symbol
		}
		if !isValidGridAction(decisions[i].Action) {
			logger.Warnf("Invalid grid action: %s", decisions[i].Action)
		}
	}

	return decisions, nil
}

// isValidGridAction checks if action is a valid grid action
func isValidGridAction(action string) bool {
	validActions := map[string]bool{
		"place_buy_limit":   true,
		"place_sell_limit":  true,
		"cancel_order":      true,
		"cancel_all_orders": true,
		"pause_grid":        true,
		"resume_grid":       true,
		"adjust_grid":       true,
		"hold":              true,
		"reduce_long":       true,
		"reduce_short":      true,
		"reduce_position":   true,
		"open_long":         true,
		"open_short":        true,
		"close_long":        true,
		"close_short":       true,
	}
	return validActions[action]
}

// BuildGridContextFromMarketData initializes a GridContext from raw market data
func BuildGridContextFromMarketData(mktData *market.Data, config *store.GridStrategyConfig) *GridContext {
	ctx := &GridContext{
		Symbol:          config.Symbol,
		CurrentTime:     time.Now().Format("2006-01-02 15:04:05"),
		GridCount:       config.GridCount,
		TotalInvestment: config.TotalInvestment,
		Leverage:        config.Leverage,
		UpperPrice:      config.UpperPrice,
		LowerPrice:      config.LowerPrice,
		GridSpacing: func() float64 {
			if config.GridCount > 0 {
				return (config.UpperPrice - config.LowerPrice) / float64(config.GridCount)
			}
			return 0
		}(),
		Distribution: config.Distribution,
	}

	if mktData != nil {
		ctx.CurrentPrice = mktData.CurrentPrice
		ctx.PriceChange1h = mktData.PriceChange1h
		ctx.PriceChange4h = mktData.PriceChange4h
		ctx.FundingRate = mktData.FundingRate
		ctx.EMA20 = mktData.CurrentEMA20
		ctx.MACD = mktData.CurrentMACD

		if mktData.TimeframeData != nil {
			if tf5m, ok := mktData.TimeframeData["5m"]; ok {
				if len(tf5m.BOLLUpper) > 0 {
					ctx.BollingerUpper = tf5m.BOLLUpper[len(tf5m.BOLLUpper)-1]
					ctx.BollingerMiddle = tf5m.BOLLMiddle[len(tf5m.BOLLMiddle)-1]
					ctx.BollingerLower = tf5m.BOLLLower[len(tf5m.BOLLLower)-1]
					if ctx.BollingerMiddle > 0 {
						ctx.BollingerWidth = (ctx.BollingerUpper - ctx.BollingerLower) / ctx.BollingerMiddle * 100
					}
				}
				ctx.ATR14 = tf5m.ATR14
				if len(tf5m.RSI14Values) > 0 {
					ctx.RSI14 = tf5m.RSI14Values[len(tf5m.RSI14Values)-1]
				}
			}
		}

		if mktData.LongerTermContext != nil {
			if ctx.ATR14 == 0 {
				ctx.ATR14 = mktData.LongerTermContext.ATR14
			}
			ctx.EMA50 = mktData.LongerTermContext.EMA50
		}

		if ctx.EMA50 > 0 {
			ctx.EMADistance = (ctx.EMA20 - ctx.EMA50) / ctx.EMA50 * 100
		}
	}

	return ctx
}

// ============================================================================
// Grid Trading Context and Types
// ============================================================================

// GridLevelInfo represents a single grid level's current state
type GridLevelInfo struct {
	Index          int       `json:"index"`           // Level index (0 = lowest)
	Price          float64   `json:"price"`           // Target price for this level
	State          string    `json:"state"`           // "empty", "pending", "filled"
	Side           string    `json:"side"`            // "buy" or "sell"
	OrderID        string    `json:"order_id"`         // Current order ID (if pending)
	OrderQuantity  float64   `json:"order_quantity"`   // Order quantity
	PositionSize   float64   `json:"position_size"`   // Position size (if filled)
	PositionEntry  float64   `json:"position_entry"`   // Entry price (if filled)
	AllocatedUSD   float64   `json:"allocated_usd"`   // USD allocated to this level
	UnrealizedPnL  float64   `json:"unrealized_pnl"`   // Unrealized P&L (if filled)
	DistancePct    float64   `json:"distance_pct"`    // % distance from current price (+ = above, - = below)
	OrderPlacedAt  time.Time `json:"order_placed_at"` // When the current order was placed (for grace period)
}

// GridContext contains all information needed for AI grid decision making
type GridContext struct {
	// Basic info
	Symbol       string  `json:"symbol"`
	CurrentTime  string  `json:"current_time"`
	CurrentPrice float64 `json:"current_price"`

	// Grid configuration
	GridCount       int     `json:"grid_count"`
	TotalInvestment float64 `json:"total_investment"`
	Leverage        int     `json:"leverage"`
	UpperPrice      float64 `json:"upper_price"`
	LowerPrice      float64 `json:"lower_price"`
	GridSpacing     float64 `json:"grid_spacing"`
	Distribution    string  `json:"distribution"`

	// Grid state
	Levels            []GridLevelInfo `json:"levels"`
	ActiveOrderCount  int             `json:"active_order_count"`
	FilledLevelCount  int             `json:"filled_level_count"`
	IsPaused          bool            `json:"is_paused"`

	// Market data
	ATR14           float64 `json:"atr14"`
	BollingerUpper  float64 `json:"bollinger_upper"`
	BollingerMiddle float64 `json:"bollinger_middle"`
	BollingerLower  float64 `json:"bollinger_lower"`
	BollingerWidth  float64 `json:"bollinger_width"` // Percentage
	EMA20           float64 `json:"ema20"`
	EMA50           float64 `json:"ema50"`
	EMADistance     float64 `json:"ema_distance"` // Percentage
	RSI14           float64 `json:"rsi14"`
	MACD            float64 `json:"macd"`
	MACDSignal      float64 `json:"macd_signal"`
	MACDHistogram   float64 `json:"macd_histogram"`
	FundingRate     float64 `json:"funding_rate"`
	Volume24h       float64 `json:"volume_24h"`
	PriceChange1h   float64 `json:"price_change_1h"`
	PriceChange4h   float64 `json:"price_change_4h"`

	// Account info
	TotalEquity      float64 `json:"total_equity"`
	WalletBalance    float64 `json:"wallet_balance"` // 余额 = 可用余额 + 持仓保证金 - 浮动收益 (totalWalletBalance)
	AvailableBalance float64 `json:"available_balance"`
	CurrentPosition  float64 `json:"current_position"` // Net position size
	LongPosition     float64 `json:"long_position"`    // Long position size
	ShortPosition    float64 `json:"short_position"`    // Short position size
	UnrealizedPnL    float64 `json:"unrealized_pnl"`

	// Performance
	TotalProfit   float64 `json:"total_profit"`
	TotalTrades   int     `json:"total_trades"`
	WinningTrades int     `json:"winning_trades"`
	MaxDrawdown   float64 `json:"max_drawdown"`
	DailyPnL      float64 `json:"daily_pnl"`

	// Box indicators (Donchian Channels)
	BoxData *market.BoxData `json:"box_data,omitempty"`

	// Grid direction (neutral, long, short, long_bias, short_bias)
	CurrentDirection string `json:"current_direction,omitempty"`

	// Trapped position info (被套信息) - populated when positions are in significant loss
	TrappedInfo *TrappedPositionInfo `json:"trapped_info,omitempty"`

	// Decision history for AI context
	DecisionHistory []DecisionSummary `json:"decision_history,omitempty"`
}

// TrappedPositionInfo contains information about trapped (losing) positions
type TrappedPositionInfo struct {
	IsTrapped            bool    `json:"is_trapped"`             // whether currently trapped
	Side                 string  `json:"side"`                  // "buy" (long trapped) or "sell" (short trapped)
	TotalUnrealizedLoss  float64 `json:"total_unrealized_loss"`  // total USD loss
	LossPct              float64 `json:"loss_pct"`               // loss as % of total investment
	TrappedLevelCount    int     `json:"trapped_level_count"`    // number of losing levels
	ThresholdPct         float64 `json:"threshold_pct"`           // configured trigger threshold %
	TrappedPositionSize  float64 `json:"trapped_position_size"`   // total size of trapped position
	AvgEntryPrice        float64 `json:"avg_entry_price"`         // weighted average entry price
	CurrentPrice         float64 `json:"current_price"`           // current market price
	PriceDiffPct         float64 `json:"price_diff_pct"`          // (avgEntry - current) / avgEntry * 100
	SuggestReducePct     float64 `json:"suggest_reduce_pct"`      // suggested reduction percentage
	LastReduceMinutes    int     `json:"last_reduce_minutes"`    // minutes since last reduction (-1 = never)
	// T-trade state (T字状态)
	TTradePhase          string  `json:"t_trade_phase"`           // "idle" | "waiting_buy_fill" | "ready_to_reduce"
	TTradeBuyOrderID     string  `json:"t_trade_buy_order_id"`     // pending T-trade buy order ID (if waiting)
	TTradeBuyPrice       float64 `json:"t_trade_buy_price"`        // price of pending T-trade buy
	TTradePendingReduce  float64 `json:"t_trade_pending_reduce"`   // qty waiting to be reduced after buy fills
}

// ============================================================================
// Grid Prompt Building
// ============================================================================

// BuildGridSystemPrompt builds the system prompt for grid trading AI
func BuildGridSystemPrompt(strategyConfig *store.StrategyConfig, lang string) string {
	config := strategyConfig.GridConfig
	var prompt string
	if lang == "zh" {
		prompt = buildGridSystemPromptZh(config)
	} else {
		prompt = buildGridSystemPromptEn(config)
	}
	// Append custom prompt from strategy config if set
	if strategyConfig.CustomPrompt != "" {
		prompt += "\n\n## 自定义策略补充\n" + strategyConfig.CustomPrompt
	}
	return prompt
}

func buildGridSystemPromptZh(config *store.GridStrategyConfig) string {
	trappedSection := ""
	if config.EnableTrappedReduce {
		trappedSection = fmt.Sprintf(`
## 被套减仓（T字操作）
当 trapped_info.is_trapped = true 且 t_trade_phase = "idle" 时，执行T字操作：
- 多单被套（side=buy）→ place_buy_limit 在当前价下方 0.3~0.5 ATR 挂买单
- 空单被套（side=sell）→ place_sell_limit 在当前价上方 0.3~0.5 ATR 挂卖单
- 数量 = trapped_position_size × suggest_reduce_pct / 100
- 挂单后系统自动监控成交并执行减仓，本周期无需其他操作
- t_trade_phase 不是 "idle" 时，**不要重复下单**
- 触发条件：损失超过 %.1f%%
`, config.TrappedReduceThresholdPct)
	}

	return fmt.Sprintf(`# 网格交易 AI — %s

## 角色
你是一个双向网格交易专家。每个周期你需要：
1. 判断市场状态，决定网格方向偏好
2. 补全空缺的网格层级订单
3. 管理仓位风险

## 网格配置
- 交易对: %s | 层数: %d | 投资额: %.2f USDT | 杠杆: %dx | 分布: %s

## 市场判断
| 状态 | 条件 | 操作 |
|------|------|------|
| 震荡 | 布林带宽 < 3%%, EMA距离 < 1%% | 正常补单，多空均衡 |
| 趋势上行 | 布林带宽 > 4%%, 价格持续突破上轨 | 偏多：优先补买单，减少卖单 |
| 趋势下行 | 布林带宽 > 4%%, 价格持续突破下轨 | 偏空：优先补卖单，减少买单 |
| 高波动 | ATR 异常放大 | pause_grid 暂停 |

## 操作说明

### 网格补单（核心任务）
- place_buy_limit：在买方空层开多仓
- place_sell_limit：在卖方空层开空仓
- **quantity 必须使用层级表中的「建议数量」，不要自行估算**
- **state = "pending" 的层级已有挂单，不要重复下单**

### 仓位管理
- reduce_long：平多仓/减多仓（限价单）
- reduce_short：平空仓/减空仓（限价单）
- ⚠️ 想减少多头敞口 → reduce_long，**不要用 place_sell_limit**
- ⚠️ 想减少空头敞口 → reduce_short，**不要用 place_buy_limit**

### 其他操作
- cancel_order / cancel_all_orders：撤单
- pause_grid / resume_grid：暂停/恢复
- adjust_grid：重新计算网格边界
- hold：本周期不操作
%s
## 输出格式
输出 JSON 数组，示例：
[{"symbol":"...","action":"...","price":0.0,"quantity":0.0,"level_index":0,"confidence":0,"reasoning":"..."}]
`, config.Symbol, config.Symbol, config.GridCount, config.TotalInvestment, config.Leverage, config.Distribution, trappedSection)
}

func buildGridSystemPromptEn(config *store.GridStrategyConfig) string {
	trappedSection := ""
	if config.EnableTrappedReduce {
		trappedSection = fmt.Sprintf(`
## Trapped Position Recovery (T-Trade)
When trapped_info.is_trapped = true and t_trade_phase = "idle":
- Long trapped (side=buy) → place_buy_limit 0.3~0.5 ATR below current price
- Short trapped (side=sell) → place_sell_limit 0.3~0.5 ATR above current price
- Quantity = trapped_position_size × suggest_reduce_pct / 100
- Place the order only — system auto-executes the reduce after fill
- If t_trade_phase ≠ "idle": **do NOT place another order**
- Trigger: loss exceeds %.1f%%
`, config.TrappedReduceThresholdPct)
	}

	return fmt.Sprintf(`# Grid Trading AI — %s

## Role
You are a bidirectional grid trading expert. Each cycle you must:
1. Assess market conditions and determine directional bias
2. Fill empty grid levels with orders
3. Manage position risk

## Grid Configuration
- Symbol: %s | Levels: %d | Investment: %.2f USDT | Leverage: %dx | Distribution: %s

## Market Assessment
| State | Conditions | Action |
|-------|-----------|--------|
| Ranging | BB width < 3%%, EMA distance < 1%% | Normal grid, balanced long/short |
| Uptrend | BB width > 4%%, price breaking upper band | Long bias: prioritize buy orders |
| Downtrend | BB width > 4%%, price breaking lower band | Short bias: prioritize sell orders |
| High volatility | ATR abnormally large | pause_grid |

## Actions

### Grid Orders (core task)
- place_buy_limit: open long at an empty buy-side level
- place_sell_limit: open short at an empty sell-side level
- **quantity must use "Suggested Qty" from the level table — do not estimate**
- **levels with state = "pending" already have an order — do NOT place another**

### Position Management
- reduce_long: close/reduce long position (limit order)
- reduce_short: close/reduce short position (limit order)
- ⚠️ To reduce long exposure → reduce_long, **do NOT use place_sell_limit**
- ⚠️ To reduce short exposure → reduce_short, **do NOT use place_buy_limit**

### Other
- cancel_order / cancel_all_orders: cancel orders
- pause_grid / resume_grid: pause or resume
- adjust_grid: recalculate grid bounds
- hold: no action this cycle
%s
## Output Format
Output a JSON array:
[{"symbol":"...","action":"...","price":0.0,"quantity":0.0,"level_index":0,"confidence":0,"reasoning":"..."}]
`, config.Symbol, config.Symbol, config.GridCount, config.TotalInvestment, config.Leverage, config.Distribution, trappedSection)
}

// BuildGridUserPrompt builds the user prompt for grid trading AI
func BuildGridUserPrompt(ctx *GridContext, lang string) string {
	if lang == "zh" {
		return buildGridUserPromptZh(ctx)
	}
	return buildGridUserPromptEn(ctx)
}

func buildGridUserPromptZh(ctx *GridContext) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("## 当前时间: %s\n\n", ctx.CurrentTime))

	if len(ctx.DecisionHistory) > 0 {
		sb.WriteString("## 历史决策回顾\n")
		sb.WriteString("| 时间 | 操作 | 理由 | 价格 |\n")
		sb.WriteString("|------|------|------|------|\n")
		for _, d := range ctx.DecisionHistory {
			sb.WriteString(fmt.Sprintf("| %s | %s | %s | %.2f |\n", d.Timestamp, d.Action, d.Reasoning, d.Price))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## 市场数据\n")
	sb.WriteString(fmt.Sprintf("- 当前价格: $%.2f\n", ctx.CurrentPrice))
	sb.WriteString(fmt.Sprintf("- 1小时涨跌: %.2f%%\n", ctx.PriceChange1h))
	sb.WriteString(fmt.Sprintf("- 4小时涨跌: %.2f%%\n", ctx.PriceChange4h))
	sb.WriteString(fmt.Sprintf("- ATR14: $%.2f (%.2f%%)\n", ctx.ATR14, ctx.ATR14/ctx.CurrentPrice*100))
	sb.WriteString(fmt.Sprintf("- 布林带: 上轨 $%.2f, 中轨 $%.2f, 下轨 $%.2f\n", ctx.BollingerUpper, ctx.BollingerMiddle, ctx.BollingerLower))
	sb.WriteString(fmt.Sprintf("- 布林带宽度: %.2f%%\n", ctx.BollingerWidth))
	sb.WriteString(fmt.Sprintf("- EMA20: $%.2f, EMA50: $%.2f, 距离: %.2f%%\n", ctx.EMA20, ctx.EMA50, ctx.EMADistance))
	sb.WriteString(fmt.Sprintf("- RSI14: %.1f\n", ctx.RSI14))
	sb.WriteString(fmt.Sprintf("- MACD: %.4f, Signal: %.4f, Histogram: %.4f\n", ctx.MACD, ctx.MACDSignal, ctx.MACDHistogram))
	sb.WriteString(fmt.Sprintf("- 资金费率: %.4f%%\n", ctx.FundingRate*100))
	sb.WriteString("\n")

	if ctx.BoxData != nil {
		sb.WriteString("## 箱体指标 (唐奇安通道)\n\n")
		sb.WriteString("| 箱体级别 | 上轨 | 下轨 | 宽度 |\n")
		sb.WriteString("|----------|------|------|------|\n")
		shortWidth, midWidth, longWidth := 0.0, 0.0, 0.0
		if ctx.BoxData.CurrentPrice > 0 {
			shortWidth = (ctx.BoxData.ShortUpper - ctx.BoxData.ShortLower) / ctx.BoxData.CurrentPrice * 100
			midWidth = (ctx.BoxData.MidUpper - ctx.BoxData.MidLower) / ctx.BoxData.CurrentPrice * 100
			longWidth = (ctx.BoxData.LongUpper - ctx.BoxData.LongLower) / ctx.BoxData.CurrentPrice * 100
		}
		sb.WriteString(fmt.Sprintf("| 短期 (3天) | %.2f | %.2f | %.2f%% |\n", ctx.BoxData.ShortUpper, ctx.BoxData.ShortLower, shortWidth))
		sb.WriteString(fmt.Sprintf("| 中期 (10天) | %.2f | %.2f | %.2f%% |\n", ctx.BoxData.MidUpper, ctx.BoxData.MidLower, midWidth))
		sb.WriteString(fmt.Sprintf("| 长期 (21天) | %.2f | %.2f | %.2f%% |\n", ctx.BoxData.LongUpper, ctx.BoxData.LongLower, longWidth))
		sb.WriteString(fmt.Sprintf("\n当前价格: %.2f\n", ctx.BoxData.CurrentPrice))
		price := ctx.BoxData.CurrentPrice
		if price > ctx.BoxData.LongUpper || price < ctx.BoxData.LongLower {
			sb.WriteString("⚠️ 突破: 价格突破长期箱体!\n")
		} else if price > ctx.BoxData.MidUpper || price < ctx.BoxData.MidLower {
			sb.WriteString("⚠️ 警告: 价格接近长期箱体边界\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## 账户状态\n")
	sb.WriteString(fmt.Sprintf("- 总权益: $%.2f\n", ctx.TotalEquity))
	sb.WriteString(fmt.Sprintf("- 可用余额: $%.2f\n", ctx.AvailableBalance))
	sb.WriteString(fmt.Sprintf("- 余额 (网格总投资=可用余额+持仓保证金-浮动收益): $%.2f\n", ctx.WalletBalance))
	sb.WriteString(fmt.Sprintf("- 当前持仓: %.4f (净头寸)\n", ctx.CurrentPosition))
	if ctx.LongPosition != 0 || ctx.ShortPosition != 0 {
		sb.WriteString(fmt.Sprintf("  - 多头: %.4f 合约\n", ctx.LongPosition))
		sb.WriteString(fmt.Sprintf("  - 空头: %.4f 合约\n", ctx.ShortPosition))
	}
	sb.WriteString(fmt.Sprintf("- 未实现盈亏: $%.2f\n", ctx.UnrealizedPnL))
	sb.WriteString("\n")

	sb.WriteString("## 网格状态\n")
	sb.WriteString(fmt.Sprintf("- 网格范围: $%.2f - $%.2f\n", ctx.LowerPrice, ctx.UpperPrice))
	sb.WriteString(fmt.Sprintf("- 网格间距: $%.2f\n", ctx.GridSpacing))
	sb.WriteString(fmt.Sprintf("- 活跃订单数: %d\n", ctx.ActiveOrderCount))
	sb.WriteString(fmt.Sprintf("- 已成交层数: %d\n", ctx.FilledLevelCount))
	sb.WriteString(fmt.Sprintf("- 网格已暂停: %v\n", ctx.IsPaused))
	if ctx.CurrentDirection != "" {
		dirMap := map[string]string{
			"neutral": "中性 (50%买+50%卖)", "long": "做多 (100%买)",
			"short": "做空 (100%卖)", "long_bias": "偏多 (70%买+30%卖)", "short_bias": "偏空 (30%买+70%卖)",
		}
		desc := dirMap[ctx.CurrentDirection]
		if desc == "" {
			desc = ctx.CurrentDirection
		}
		sb.WriteString(fmt.Sprintf("- 网格方向: %s\n", desc))
	}
	sb.WriteString("\n")

	sb.WriteString("## 网格层级详情\n")
	sb.WriteString("| 层级 | 价格 | 状态 | 方向 | 分配USD | 建议数量 | 订单数量 | 持仓数量 | 未实现盈亏 |\n")
	sb.WriteString("|------|------|------|------|---------|----------|----------|----------|------------|\n")
	for _, level := range ctx.Levels {
		suggestedQty := 0.0
		if level.Price > 0 && level.AllocatedUSD > 0 {
			suggestedQty = level.AllocatedUSD * float64(ctx.Leverage) / level.Price
		}
		sb.WriteString(fmt.Sprintf("| %d | $%.2f | %s | %s | $%.2f | %.4f | %.4f | %.4f | $%.2f |\n",
			level.Index, level.Price, level.State, level.Side, level.AllocatedUSD, suggestedQty,
			level.OrderQuantity, level.PositionSize, level.UnrealizedPnL))
	}
	sb.WriteString("\n")

	sb.WriteString("## 绩效统计\n")
	sb.WriteString(fmt.Sprintf("- 总利润: $%.2f\n", ctx.TotalProfit))
	sb.WriteString(fmt.Sprintf("- 总交易次数: %d\n", ctx.TotalTrades))
	sb.WriteString(fmt.Sprintf("- 胜率: %.1f%%\n", float64(ctx.WinningTrades)/float64(max(ctx.TotalTrades, 1))*100))
	sb.WriteString(fmt.Sprintf("- 最大回撤: %.2f%%\n", ctx.MaxDrawdown))
	sb.WriteString(fmt.Sprintf("- 今日盈亏: $%.2f\n", ctx.DailyPnL))
	sb.WriteString("\n")

	if ctx.TrappedInfo != nil && ctx.TrappedInfo.IsTrapped {
		t := ctx.TrappedInfo
		sideZh := "多单（买入方向）"
		if t.Side == "sell" {
			sideZh = "空单（卖出方向）"
		}
		logger.Infof("[Grid] 🔍 DEBUG: t.Side=%s, sideZh=%s", t.Side, sideZh)
		sb.WriteString("## ⚠️ 被套警告\n")
		sb.WriteString("- 被套状态: 是\n")
		sb.WriteString(fmt.Sprintf("- 被套方向: %s\n", sideZh))
		sb.WriteString(fmt.Sprintf("- 未实现亏损: $%.2f\n", t.TotalUnrealizedLoss))
		sb.WriteString(fmt.Sprintf("- 亏损占比: %.2f%% (阈值: %.1f%%)\n", t.LossPct, t.ThresholdPct))
		sb.WriteString(fmt.Sprintf("- 被套层数: %d\n", t.TrappedLevelCount))
		sb.WriteString(fmt.Sprintf("- 平均开仓价: $%.2f\n", t.AvgEntryPrice))
		sb.WriteString(fmt.Sprintf("- 当前价格: $%.2f\n", t.CurrentPrice))
		sb.WriteString(fmt.Sprintf("- 价差: %.2f%%\n", t.PriceDiffPct))
		sb.WriteString(fmt.Sprintf("- 建议减仓比例: %.0f%%\n", t.SuggestReducePct))
		if t.LastReduceMinutes >= 0 {
			sb.WriteString(fmt.Sprintf("- 上次减仓: %d 分钟前\n", t.LastReduceMinutes))
		} else {
			sb.WriteString("- 上次减仓: 从未执行\n")
		}
		switch t.TTradePhase {
		case "waiting_buy_fill":
			label := "买单"
			if t.Side == "sell" {
				label = "卖单"
			}
			sb.WriteString(fmt.Sprintf("- **T字状态: 等待%s成交** (orderID=%s, 价格=%.2f, 待减仓=%.4f)\n",
				label, t.TTradeBuyOrderID, t.TTradeBuyPrice, t.TTradePendingReduce))
			sb.WriteString("- ⛔ **系统正在等待T字挂单成交后自动执行减仓，本轮请勿重复下单或 reduce_position**\n")
		default:
			sb.WriteString("- T字状态: 空闲 (可执行T字操作)\n")
			if t.Side == "sell" {
				sb.WriteString("**⚡ 空单被套建议：使用 reduce_short 在当前价格附近挂限价单逐步减仓（不建议T字操作）**\n")
			} else {
				sb.WriteString("**⚡ 多单被套T字提示：先用 place_buy_limit 在【低位】挂买单，再执行 reduce_position 减仓（降低平均入场价）**\n")
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## 请分析以上数据，做出网格交易决策\n")
	sb.WriteString("输出JSON数组格式的决策列表。\n")
	return sb.String()
}

func buildGridUserPromptEn(ctx *GridContext) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("## Current Time: %s\n\n", ctx.CurrentTime))

	if len(ctx.DecisionHistory) > 0 {
		sb.WriteString("## Decision History Review\n")
		sb.WriteString("| Time | Action | Reasoning | Price |\n")
		sb.WriteString("|------|--------|-----------|-------|\n")
		for _, d := range ctx.DecisionHistory {
			sb.WriteString(fmt.Sprintf("| %s | %s | %s | %.2f |\n", d.Timestamp, d.Action, d.Reasoning, d.Price))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Market Data\n")
	sb.WriteString(fmt.Sprintf("- Current Price: $%.2f\n", ctx.CurrentPrice))
	sb.WriteString(fmt.Sprintf("- 1h Change: %.2f%%\n", ctx.PriceChange1h))
	sb.WriteString(fmt.Sprintf("- 4h Change: %.2f%%\n", ctx.PriceChange4h))
	sb.WriteString(fmt.Sprintf("- ATR14: $%.2f (%.2f%%)\n", ctx.ATR14, ctx.ATR14/ctx.CurrentPrice*100))
	sb.WriteString(fmt.Sprintf("- Bollinger Bands: Upper $%.2f, Middle $%.2f, Lower $%.2f\n", ctx.BollingerUpper, ctx.BollingerMiddle, ctx.BollingerLower))
	sb.WriteString(fmt.Sprintf("- Bollinger Width: %.2f%%\n", ctx.BollingerWidth))
	sb.WriteString(fmt.Sprintf("- EMA20: $%.2f, EMA50: $%.2f, Distance: %.2f%%\n", ctx.EMA20, ctx.EMA50, ctx.EMADistance))
	sb.WriteString(fmt.Sprintf("- RSI14: %.1f\n", ctx.RSI14))
	sb.WriteString(fmt.Sprintf("- MACD: %.4f, Signal: %.4f, Histogram: %.4f\n", ctx.MACD, ctx.MACDSignal, ctx.MACDHistogram))
	sb.WriteString(fmt.Sprintf("- Funding Rate: %.4f%%\n", ctx.FundingRate*100))
	sb.WriteString("\n")

	if ctx.BoxData != nil {
		sb.WriteString("## Box Indicators (Donchian Channels)\n\n")
		sb.WriteString("| Box Level | Upper | Lower | Width |\n")
		sb.WriteString("|-----------|-------|-------|-------|\n")
		shortWidth, midWidth, longWidth := 0.0, 0.0, 0.0
		if ctx.BoxData.CurrentPrice > 0 {
			shortWidth = (ctx.BoxData.ShortUpper - ctx.BoxData.ShortLower) / ctx.BoxData.CurrentPrice * 100
			midWidth = (ctx.BoxData.MidUpper - ctx.BoxData.MidLower) / ctx.BoxData.CurrentPrice * 100
			longWidth = (ctx.BoxData.LongUpper - ctx.BoxData.LongLower) / ctx.BoxData.CurrentPrice * 100
		}
		sb.WriteString(fmt.Sprintf("| Short (3d) | %.2f | %.2f | %.2f%% |\n", ctx.BoxData.ShortUpper, ctx.BoxData.ShortLower, shortWidth))
		sb.WriteString(fmt.Sprintf("| Mid (10d) | %.2f | %.2f | %.2f%% |\n", ctx.BoxData.MidUpper, ctx.BoxData.MidLower, midWidth))
		sb.WriteString(fmt.Sprintf("| Long (21d) | %.2f | %.2f | %.2f%% |\n", ctx.BoxData.LongUpper, ctx.BoxData.LongLower, longWidth))
		sb.WriteString(fmt.Sprintf("\nCurrent Price: %.2f\n", ctx.BoxData.CurrentPrice))
		price := ctx.BoxData.CurrentPrice
		if price > ctx.BoxData.LongUpper || price < ctx.BoxData.LongLower {
			sb.WriteString("⚠️ BREAKOUT: Price outside long-term box!\n")
		} else if price > ctx.BoxData.MidUpper || price < ctx.BoxData.MidLower {
			sb.WriteString("⚠️ WARNING: Price approaching long-term box boundary\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Account Status\n")
	sb.WriteString(fmt.Sprintf("- Total Equity: $%.2f\n", ctx.TotalEquity))
	sb.WriteString(fmt.Sprintf("- Available Balance: $%.2f\n", ctx.AvailableBalance))
	sb.WriteString(fmt.Sprintf("- Balance (Grid Investment = Available + Margin - Unrealized PnL): $%.2f\n", ctx.WalletBalance))
	sb.WriteString(fmt.Sprintf("- Current Position: %.4f (net)\n", ctx.CurrentPosition))
	sb.WriteString(fmt.Sprintf("- Unrealized PnL: $%.2f\n", ctx.UnrealizedPnL))
	sb.WriteString("\n")

	sb.WriteString("## Grid Status\n")
	sb.WriteString(fmt.Sprintf("- Grid Range: $%.2f - $%.2f\n", ctx.LowerPrice, ctx.UpperPrice))
	sb.WriteString(fmt.Sprintf("- Grid Spacing: $%.2f\n", ctx.GridSpacing))
	sb.WriteString(fmt.Sprintf("- Active Orders: %d\n", ctx.ActiveOrderCount))
	sb.WriteString(fmt.Sprintf("- Filled Levels: %d\n", ctx.FilledLevelCount))
	sb.WriteString(fmt.Sprintf("- Grid Paused: %v\n", ctx.IsPaused))
	if ctx.CurrentDirection != "" {
		dirMap := map[string]string{
			"neutral": "Neutral (50% buy + 50% sell)", "long": "Long (100% buy)",
			"short": "Short (100% sell)", "long_bias": "Long Bias (70% buy + 30% sell)", "short_bias": "Short Bias (30% buy + 70% sell)",
		}
		desc := dirMap[ctx.CurrentDirection]
		if desc == "" {
			desc = ctx.CurrentDirection
		}
		sb.WriteString(fmt.Sprintf("- Grid Direction: %s\n", desc))
	}
	sb.WriteString("\n")

	sb.WriteString("## Grid Levels Detail\n")
	sb.WriteString("| Level | Price | State | Side | Allocated USD | Suggested Qty | Order Qty | Position | Unrealized PnL |\n")
	sb.WriteString("|-------|-------|-------|------|---------------|---------------|-----------|----------|----------------|\n")
	for _, level := range ctx.Levels {
		suggestedQty := 0.0
		if level.Price > 0 && level.AllocatedUSD > 0 {
			suggestedQty = level.AllocatedUSD * float64(ctx.Leverage) / level.Price
		}
		sb.WriteString(fmt.Sprintf("| %d | $%.2f | %s | %s | $%.2f | %.4f | %.4f | %.4f | $%.2f |\n",
			level.Index, level.Price, level.State, level.Side, level.AllocatedUSD, suggestedQty,
			level.OrderQuantity, level.PositionSize, level.UnrealizedPnL))
	}
	sb.WriteString("\n")

	sb.WriteString("## Performance Stats\n")
	sb.WriteString(fmt.Sprintf("- Total Profit: $%.2f\n", ctx.TotalProfit))
	sb.WriteString(fmt.Sprintf("- Total Trades: %d\n", ctx.TotalTrades))
	sb.WriteString(fmt.Sprintf("- Win Rate: %.1f%%\n", float64(ctx.WinningTrades)/float64(max(ctx.TotalTrades, 1))*100))
	sb.WriteString(fmt.Sprintf("- Max Drawdown: %.2f%%\n", ctx.MaxDrawdown))
	sb.WriteString(fmt.Sprintf("- Daily PnL: $%.2f\n", ctx.DailyPnL))
	sb.WriteString("\n")

	if ctx.TrappedInfo != nil && ctx.TrappedInfo.IsTrapped {
		t := ctx.TrappedInfo
		sideEn := "Long (buy direction)"
		if t.Side == "sell" {
			sideEn = "Short (sell direction)"
		}
		sb.WriteString("## ⚠️ TRAPPED POSITION WARNING\n")
		sb.WriteString("- Trapped: YES\n")
		sb.WriteString(fmt.Sprintf("- Trapped Side: %s\n", sideEn))
		sb.WriteString(fmt.Sprintf("- Unrealized Loss: $%.2f\n", t.TotalUnrealizedLoss))
		sb.WriteString(fmt.Sprintf("- Loss Percentage: %.2f%% (threshold: %.1f%%)\n", t.LossPct, t.ThresholdPct))
		sb.WriteString(fmt.Sprintf("- Trapped Levels: %d\n", t.TrappedLevelCount))
		sb.WriteString(fmt.Sprintf("- Avg Entry Price: $%.2f\n", t.AvgEntryPrice))
		sb.WriteString(fmt.Sprintf("- Current Price: $%.2f\n", t.CurrentPrice))
		sb.WriteString(fmt.Sprintf("- Price Diff: %.2f%%\n", t.PriceDiffPct))
		sb.WriteString(fmt.Sprintf("- Suggested Reduce Pct: %.0f%%\n", t.SuggestReducePct))
		if t.LastReduceMinutes >= 0 {
			sb.WriteString(fmt.Sprintf("- Last Reduction: %d minutes ago\n", t.LastReduceMinutes))
		} else {
			sb.WriteString("- Last Reduction: Never executed\n")
		}
		switch t.TTradePhase {
		case "waiting_buy_fill":
			label := "BUY"
			if t.Side == "sell" {
				label = "SELL"
			}
			sb.WriteString(fmt.Sprintf("- **T-Trade State: WAITING FOR %s FILL** (orderID=%s, price=%.2f, pending reduce=%.4f)\n",
				label, t.TTradeBuyOrderID, t.TTradeBuyPrice, t.TTradePendingReduce))
			sb.WriteString("- ⛔ **System is waiting for T-trade order to fill. DO NOT issue additional orders or reduce_position this cycle.**\n")
		default:
			sb.WriteString("- T-Trade State: IDLE (ready for T-trade)\n")
			if t.Side == "sell" {
				sb.WriteString("**⚡ SHORT trapped: Use place_sell_limit at HIGH price, system auto-executes reduce_short after fill**\n")
			} else {
				sb.WriteString("**⚡ LONG trapped: Use place_buy_limit at LOW price, system auto-executes reduce_long after fill**\n")
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Please analyze the data above and make grid trading decisions\n")
	sb.WriteString("Output a JSON array of decisions.\n")
	return sb.String()
}

// extractJSONArray extracts a JSON array from an AI response string
func extractJSONArray(response string) string {
	if m := reJSONFence.FindStringSubmatch(response); len(m) > 1 {
		return m[1]
	}
	if m := reJSONArray.FindString(response); m != "" {
		return m
	}
	return ""
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

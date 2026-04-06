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
	Index          int     `json:"index"`           // Level index (0 = lowest)
	Price          float64 `json:"price"`           // Target price for this level
	State          string  `json:"state"`           // "empty", "pending", "filled"
	Side           string  `json:"side"`            // "buy" or "sell"
	OrderID        string  `json:"order_id"`         // Current order ID (if pending)
	OrderQuantity  float64 `json:"order_quantity"`   // Order quantity
	PositionSize   float64 `json:"position_size"`   // Position size (if filled)
	PositionEntry  float64 `json:"position_entry"`   // Entry price (if filled)
	AllocatedUSD   float64 `json:"allocated_usd"`   // USD allocated to this level
	UnrealizedPnL  float64 `json:"unrealized_pnl"`   // Unrealized P&L (if filled)
	DistancePct    float64 `json:"distance_pct"`    // % distance from current price (+ = above, - = below)
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
### 被套时的T字操作规则（分批减仓）
当 trapped_info.is_trapped = true 时，系统支持双向T字操作：

**多单被套（side=buy，价格下跌亏损）**：
- T字顺序：用 place_buy_limit 在**更低价**挂买单 → 系统自动执行 reduce_long
- 挂单价格：当前价下方 0.3~0.5 个ATR
- 示例：原均价$100，价格跌到$95，在$93挂买单，系统成交后自动减仓50%%

**空单被套（side=sell，价格上涨亏损）**：
- T字顺序：用 place_sell_limit 在**更高价**挂卖单 → 系统自动执行 reduce_short
- 挂单价格：当前价上方 0.3~0.5 个ATR
- 示例：原均价$100，价格涨到$105，在$107挂卖单，系统成交后自动减仓50%%

**关键规则**：
- trapped_info.side = "buy" → 多单被套 → place_buy_limit 在低位（quantity = 建议数量）
- trapped_info.side = "sell" → 空单被套 → place_sell_limit 在高位（quantity = 建议数量）
- 挂单数量 = trapped_position_size × suggest_reduce_pct / 100
- 本周期只挂单，系统自动监控成交并执行reduce，无需手动操作
- 如果 t_trade_phase = "waiting_buy_fill" 或 "waiting_sell_fill"，本轮**不要重复下单**

**何时执行T字**：
- 损失超过 %.1f%% 且价格仍在不利方向运动

**何时不执行**：
- 损失 < %.1f%%
- RSI极端值（<30或>70）且有反转信号
- t_trade_phase 不是 "idle"

示例（多单被套，trapped_info.side="buy"）:
[
{"action": "place_buy_limit", "price": 93000, "quantity": 0.005, "reasoning": "trapped_info.side=buy，多单被套，低位93000挂买单，系统自动reduce_long"}
]

示例（空单被套，trapped_info.side="sell"）:
[
{"action": "place_sell_limit", "price": 107000, "quantity": 0.005, "reasoning": "trapped_info.side=sell，空单被套，高位107000挂卖单，系统自动reduce_short"}
]
`, config.TrappedReduceThresholdPct, config.TrappedReduceThresholdPct)
	}

	return fmt.Sprintf(`# 你是一个专业的网格交易AI

## 角色定义
你是一个经验丰富的网格交易专家，负责管理 %s 的网格交易策略。你的任务是：
1. 判断当前市场状态（震荡/趋势/高波动）
2. 决定是否需要调整网格或暂停交易
3. 管理每个网格层级的订单

## 网格配置
- 交易对: %s
- 网格层数: %d
- 总投资 (余额=可用余额+持仓保证金-浮动收益): %.2f USDT
- 杠杆: %dx
- 价格分布: %s

## 决策规则

### 重要：多任务并行处理
**即使存在被套头寸，也必须同时处理正常的网格交易！**
- 被套处理和网格交易是独立的任务，可以在同一周期内同时执行
- 例如：可以同时输出T字操作订单 + 空网格层级的补单
- 不要因为有被套就忽略网格维护，两者互不冲突

### 市场状态判断
- **震荡市场** (适合网格): 布林带宽度 <<<  3%%, EMA20/50 距离 <<<  1%%, 价格在布林带中轨附近
- **趋势市场** (暂停网格): 布林带宽度 > 4%%, EMA20/50 距离 > 2%%, 价格持续突破布林带
- **高波动市场** (谨慎): ATR异常放大, 价格剧烈波动
%s
### 可执行的操作
**重要：下单时 quantity 必须使用网格层级详情表中的「建议数量」字段，不要自行估算。**
- place_buy_limit: 在指定价格下**开多仓**（补买方网格层级）
- place_sell_limit: 在指定价格下**开空仓**（补卖方网格层级）
- reduce_long: **平多仓/减多仓**（限价单），用于止盈或减少多头敞口
- reduce_short: **平空仓/减空仓**（限价单），用于止盈或减少空头敞口
- cancel_order: 取消指定订单
- cancel_all_orders: 取消所有订单
- pause_grid: 暂停网格交易（趋势市场时）
- resume_grid: 恢复网格交易（震荡市场时）
- adjust_grid: 调整网格边界
- hold: 保持当前状态

### 操作选择规则（重要）
- **补网格空层** → place_buy_limit（买方层级）或 place_sell_limit（卖方层级）
- **想减少多头敞口/平多止盈** → reduce_long，**不要用 place_sell_limit**
- **想减少空头敞口/平空止盈** → reduce_short，**不要用 place_buy_limit**
- place_buy_limit 和 place_sell_limit **只用于补空的网格层级**，不用于主动平仓%s

## 输出格式
输出JSON数组，每个决策包含:
- symbol: 交易对
- action: 操作类型
- price: 价格（限价单用）
- quantity: 数量
- level_index: 网格层级索引
- order_id: 订单ID（取消用）
- confidence: 置信度 0-100
- reasoning: 决策理由
`, config.Symbol, config.Symbol, config.GridCount, config.TotalInvestment, config.Leverage, config.Distribution, trappedSection)
}

func buildGridSystemPromptEn(config *store.GridStrategyConfig) string {
	trappedSection := ""
	if config.EnableTrappedReduce {
		trappedSection = fmt.Sprintf(`
### T-Trade Operation (Trapped Position Recovery)
When trapped_info.is_trapped = true, the system supports bidirectional T-trade:

**Long trapped (side=buy, price falling, losing):**
- T-trade order: place_buy_limit at a LOWER price (0.3~0.5 ATR below current)
- System auto-executes reduce_long after fill
- Example: avg entry $100, price at $95 → place buy at $93, system reduces long 50%% after fill

**Short trapped (side=sell, price rising, losing):**
- T-trade order: place_sell_limit at a HIGHER price (0.3~0.5 ATR above current)
- System auto-executes reduce_short after fill
- Example: avg entry $100, price at $105 → place sell at $107, system reduces short 50%% after fill

**Key rules:**
- trapped_info.side = "buy" → long trapped → place_buy_limit at low price (quantity = suggested qty)
- trapped_info.side = "sell" → short trapped → place_sell_limit at high price (quantity = suggested qty)
- Order quantity = trapped_position_size × suggest_reduce_pct / 100
- This cycle: place the order only. System monitors fill and executes reduce automatically.
- If t_trade_phase = "waiting_buy_fill" or "waiting_sell_fill": **do NOT place another order this cycle**

**When to execute T-trade:**
- Loss exceeds %.1f%% and price is still moving against the position

**When NOT to execute:**
- t_trade_phase is already "waiting_buy_fill" or "waiting_sell_fill"
- Loss is below threshold

Example (long trapped, trapped_info.side="buy"):
[
{"action": "place_buy_limit", "price": 93000, "quantity": 0.005, "reasoning": "trapped_info.side=buy, long trapped, place buy at 93000, system auto reduce_long after fill"}
]

Example (short trapped, trapped_info.side="sell"):
[
{"action": "place_sell_limit", "price": 107000, "quantity": 0.005, "reasoning": "trapped_info.side=sell, short trapped, place sell at 107000, system auto reduce_short after fill"}
]
`, config.TrappedReduceThresholdPct, config.TrappedReduceThresholdPct)
	}

	return fmt.Sprintf(`# You are a professional grid trading AI

## Role
You are an experienced grid trading expert managing the %s grid trading strategy. Your tasks:
1. Assess current market conditions (ranging / trending / high volatility)
2. Decide whether to adjust the grid or pause trading
3. Manage orders at each grid level

## Grid Configuration
- Symbol: %s
- Grid Levels: %d
- Total Investment (balance = available + margin - unrealized PnL): %.2f USDT
- Leverage: %dx
- Price Distribution: %s

## Decision Rules

### Important: Handle multiple tasks in parallel
**Even when a trapped position exists, continue normal grid maintenance!**
- Trapped position handling and grid order placement are independent tasks
- You can output a T-trade order AND fill empty grid levels in the same cycle
- Do not neglect grid maintenance just because there is a trapped position

### Market condition assessment
- **Ranging market** (good for grid): Bollinger width < 3%%, EMA20/50 distance < 1%%, price near BB middle
- **Trending market** (pause grid): Bollinger width > 4%%, EMA20/50 distance > 2%%, price breaking BB
- **High volatility** (caution): ATR abnormally large, price swinging wildly
%s
### Available actions
**Important: always use the "Suggested Qty" from the grid level table — do not estimate quantities yourself.**
- place_buy_limit: **Open long position** (fill an empty buy-side grid level)
- place_sell_limit: **Open short position** (fill an empty sell-side grid level)
- reduce_long: **Close/reduce long position** (limit order), use to take profit or reduce long exposure
- reduce_short: **Close/reduce short position** (limit order), use to take profit or reduce short exposure
- cancel_order: Cancel a specific order
- cancel_all_orders: Cancel all pending orders
- pause_grid: Pause grid trading (trending market)
- resume_grid: Resume grid trading (ranging market)
- adjust_grid: Recalculate grid bounds
- hold: Keep current state

### Action selection rules (important)
- **Fill an empty grid level** → place_buy_limit (buy-side level) or place_sell_limit (sell-side level)
- **Reduce long exposure / take long profit** → reduce_long, **do NOT use place_sell_limit**
- **Reduce short exposure / take short profit** → reduce_short, **do NOT use place_buy_limit**
- place_buy_limit and place_sell_limit are **only for filling empty grid levels**, never for closing positions

## Output format
Output a JSON array where each decision contains:
- symbol: trading pair
- action: action type
- price: price (for limit orders)
- quantity: quantity
- level_index: grid level index
- order_id: order ID (for cancel actions)
- confidence: confidence 0-100
- reasoning: decision rationale
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

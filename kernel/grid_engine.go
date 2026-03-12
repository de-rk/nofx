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

// ============================================================================
// Grid Trading Context and Types
// ============================================================================

// GridLevelInfo represents a single grid level's current state
type GridLevelInfo struct {
	Index          int     `json:"index"`            // Level index (0 = lowest)
	Price          float64 `json:"price"`            // Target price for this level
	State          string  `json:"state"`            // "empty", "pending", "filled"
	Side           string  `json:"side"`             // "buy" or "sell"
	OrderID        string  `json:"order_id"`         // Current order ID (if pending)
	OrderQuantity  float64 `json:"order_quantity"`   // Order quantity
	PositionSize   float64 `json:"position_size"`    // Position size (if filled)
	PositionEntry  float64 `json:"position_entry"`   // Entry price (if filled)
	AllocatedUSD   float64 `json:"allocated_usd"`    // USD allocated to this level
	UnrealizedPnL  float64 `json:"unrealized_pnl"`   // Unrealized P&L (if filled)
}

// GridContext contains all information needed for AI grid decision making
type GridContext struct {
	// Basic info
	Symbol       string    `json:"symbol"`
	CurrentTime  string    `json:"current_time"`
	CurrentPrice float64   `json:"current_price"`

	// Grid configuration
	GridCount       int     `json:"grid_count"`
	TotalInvestment float64 `json:"total_investment"`
	Leverage        int     `json:"leverage"`
	UpperPrice      float64 `json:"upper_price"`
	LowerPrice      float64 `json:"lower_price"`
	GridSpacing     float64 `json:"grid_spacing"`
	Distribution    string  `json:"distribution"`

	// Grid state
	Levels           []GridLevelInfo `json:"levels"`
	ActiveOrderCount int             `json:"active_order_count"`
	FilledLevelCount int             `json:"filled_level_count"`
	IsPaused         bool            `json:"is_paused"`

	// Market data
	ATR14          float64 `json:"atr14"`
	BollingerUpper float64 `json:"bollinger_upper"`
	BollingerMiddle float64 `json:"bollinger_middle"`
	BollingerLower float64 `json:"bollinger_lower"`
	BollingerWidth float64 `json:"bollinger_width"` // Percentage
	EMA20          float64 `json:"ema20"`
	EMA50          float64 `json:"ema50"`
	EMADistance    float64 `json:"ema_distance"` // Percentage
	RSI14          float64 `json:"rsi14"`
	MACD           float64 `json:"macd"`
	MACDSignal     float64 `json:"macd_signal"`
	MACDHistogram  float64 `json:"macd_histogram"`
	FundingRate    float64 `json:"funding_rate"`
	Volume24h      float64 `json:"volume_24h"`
	PriceChange1h  float64 `json:"price_change_1h"`
	PriceChange4h  float64 `json:"price_change_4h"`

	// Account info
	TotalEquity      float64 `json:"total_equity"`
	AvailableBalance float64 `json:"available_balance"`
	CurrentPosition  float64 `json:"current_position"` // Net position size
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
}

// TrappedPositionInfo contains information about trapped (losing) positions
type TrappedPositionInfo struct {
	IsTrapped           bool    `json:"is_trapped"`             // whether currently trapped
	Side                string  `json:"side"`                   // "buy" (long trapped) or "sell" (short trapped)
	TotalUnrealizedLoss float64 `json:"total_unrealized_loss"`  // total USD loss
	LossPct             float64 `json:"loss_pct"`               // loss as % of total investment
	TrappedLevelCount   int     `json:"trapped_level_count"`    // number of losing levels
	AvgEntryPrice       float64 `json:"avg_entry_price"`        // weighted average entry price
	CurrentPrice        float64 `json:"current_price"`          // current market price
	PriceDiffPct        float64 `json:"price_diff_pct"`         // (avgEntry - current) / avgEntry * 100
	SuggestReducePct    float64 `json:"suggest_reduce_pct"`     // suggested reduction percentage
	LastReduceMinutes   int     `json:"last_reduce_minutes"`    // minutes since last reduction (-1 = never)
	// T-trade state (T字状态)
	TTradePhase         string  `json:"t_trade_phase"`          // "idle" | "waiting_buy_fill" | "ready_to_reduce"
	TTradeBuyOrderID    string  `json:"t_trade_buy_order_id"`   // pending T-trade buy order ID (if waiting)
	TTradeBuyPrice      float64 `json:"t_trade_buy_price"`      // price of pending T-trade buy
	TTradePendingReduce float64 `json:"t_trade_pending_reduce"` // qty waiting to be reduced after buy fills
}

// ============================================================================
// Grid Prompt Building
// ============================================================================

// BuildGridSystemPrompt builds the system prompt for grid trading AI
func BuildGridSystemPrompt(config *store.GridStrategyConfig, lang string) string {
	if lang == "zh" {
		return buildGridSystemPromptZh(config)
	}
	return buildGridSystemPromptEn(config)
}

func buildGridSystemPromptZh(config *store.GridStrategyConfig) string {
	trappedSection := ""
	if config.EnableTrappedReduce {
		trappedSection = fmt.Sprintf(`
### 被套时的T字操作规则（分批减仓）
当 trapped_info.is_trapped = true 时，你需要评估是否执行T字操作：

**多单被套（side=buy，价格下跌亏损）**：
- 目标：让平均入场价越低越好，降低成本
- T字顺序：先用 place_buy_limit 在**更低价**挂买单 → 再执行 reduce_position 减仓
- 示例：原均价$100，价格跌到$95，先在$93挂买单，再减仓50%%，新均价降至$96.5

**空单被套（side=sell，价格上涨亏损）**：
- 目标：让平均入场价越高越好，降低成本
- T字顺序：先用 place_sell_limit 在**更高价**挂卖单 → 再执行 reduce_position 减仓
- 示例：原均价$100，价格涨到$105，先在$107挂卖单，再减仓50%%，新均价升至$103.5

**何时执行T字**（必须满足至少一个条件）：
- 损失超过总投资的 %.1f%% 且价格仍在不利趋势（多单下跌/空单上涨）
- 损失超过总投资的 5%% 无论趋势如何
- 箱体破位确认，价格远离网格边界

**T字操作策略**：
1. 判断被套方向（多单还是空单）
2. 多单：place_buy_limit 在低位挂买单；空单：place_sell_limit 在高位挂卖单
3. 挂单位置：距离当前价格1-2个ATR
4. 再用 reduce_position 减少被套仓位（每次%.1f%%，不要全部卖掉）
5. 等待挂单成交，降低平均持仓成本
6. 重复操作，逐步扭亏为盈
7. 减仓时优先平亏损最大的层级

**何时不需要T字**：
- 损失 < %.1f%% 且市场仍在震荡区间
- 多单：RSI < 30 超卖区域，价格即将反弹
- 空单：RSI > 70 超买区域，价格即将回落
- 价格刚触及布林带边轨，有反转信号

示例（多单被套T字操作，先低买后减仓）:
[
  {"symbol": "BTCUSDT", "action": "place_buy_limit", "price": 93000, "quantity": 0.003, "level_index": 1, "confidence": 75, "reasoning": "多单被套损失5%%，先在低位93000挂买单，为T字操作降低均价做准备"},
  {"symbol": "BTCUSDT", "action": "reduce_position", "quantity": 0.005, "confidence": 75, "reasoning": "低位买单已挂好，再减仓25%%，降低平均成本，逐步扭亏为盈"}
]

示例（空单被套T字操作，先高卖后减仓）:
[
  {"symbol": "BTCUSDT", "action": "place_sell_limit", "price": 107000, "quantity": 0.003, "level_index": 8, "confidence": 75, "reasoning": "空单被套损失5%%，先在高位107000挂卖单，为T字操作提高均价做准备"},
  {"symbol": "BTCUSDT", "action": "reduce_position", "quantity": 0.005, "confidence": 75, "reasoning": "高位卖单已挂好，再减仓25%%，提高平均入场价，降低成本，逐步扭亏为盈"}
]
`, config.TrappedReduceThresholdPct, config.TrappedReduceBatchPct, config.TrappedReduceThresholdPct)
	}

	reduceAction := ""
	if config.EnableTrappedReduce {
		reduceAction = "\n- reduce_position: 分批减仓（被套时使用），quantity字段指定减仓数量"
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
- 总投资: %.2f USDT
- 杠杆: %dx
- 价格分布: %s

## 决策规则

### 市场状态判断
- **震荡市场** (适合网格): 布林带宽度 < 3%%, EMA20/50 距离 < 1%%, 价格在布林带中轨附近
- **趋势市场** (暂停网格): 布林带宽度 > 4%%, EMA20/50 距离 > 2%%, 价格持续突破布林带
- **高波动市场** (谨慎): ATR异常放大, 价格剧烈波动
%s
### 可执行的操作
- place_buy_limit: 在指定价格下买入限价单
- place_sell_limit: 在指定价格下卖出限价单
- cancel_order: 取消指定订单
- cancel_all_orders: 取消所有订单
- pause_grid: 暂停网格交易（趋势市场时）
- resume_grid: 恢复网格交易（震荡市场时）
- adjust_grid: 调整网格边界%s
- hold: 保持当前状态不操作

## 输出格式
输出JSON数组，每个决策包含:
- symbol: 交易对
- action: 操作类型
- price: 价格（限价单用）
- quantity: 数量
- level_index: 网格层级索引
- order_id: 订单ID（取消订单用）
- confidence: 置信度 0-100
- reasoning: 决策理由
`, config.Symbol, config.Symbol, config.GridCount, config.TotalInvestment, config.Leverage, config.Distribution, trappedSection, reduceAction)
}

func buildGridSystemPromptEn(config *store.GridStrategyConfig) string {
	trappedSection := ""
	if config.EnableTrappedReduce {
		trappedSection = fmt.Sprintf(`
### Trapped Position T-Trade Rules (Batch Reduction)
When trapped_info.is_trapped = true, evaluate whether to execute T-trade:

**Long position trapped (side=buy, price dropped, losing)**:
- Goal: Lower the average entry price as much as possible
- T-trade order: First use place_buy_limit at a **lower price** → then execute reduce_position
- Example: Avg entry $100, price drops to $95, place buy at $93 first, then reduce 50%%, new avg = $96.5

**Short position trapped (side=sell, price rose, losing)**:
- Goal: Raise the average entry price as high as possible
- T-trade order: First use place_sell_limit at a **higher price** → then execute reduce_position
- Example: Avg entry $100, price rises to $105, place sell at $107 first, then reduce 50%%, new avg = $103.5

**When to execute T-trade** (at least one condition must be met):
- Loss > %.1f%% of total investment AND price still moving against position
- Loss > 5%% of total investment regardless of trend
- Box breakout confirmed, price far from grid boundary

**T-Trade Strategy**:
1. Identify the trapped direction (long or short)
2. Long: place_buy_limit below current price; Short: place_sell_limit above current price
3. Place order 1-2 ATR away from current price
4. Then use reduce_position to close part of trapped position (~%.1f%% each time, NOT all at once)
5. Wait for the order to fill, lowering/raising the average cost
6. Repeat to gradually turn losses into profits
7. Prioritize closing levels with the largest losses first

**When NOT to T-trade**:
- Loss < %.1f%% and market still within ranging range
- Long: RSI < 30 (oversold), price about to rebound
- Short: RSI > 70 (overbought), price about to decline
- Price just touched Bollinger band with reversal signal

Example (Long trapped - T-Trade: buy lower first, then reduce):
[
  {"symbol": "BTCUSDT", "action": "place_buy_limit", "price": 93000, "quantity": 0.003, "level_index": 1, "confidence": 75, "reasoning": "Long trapped, loss 5%%, place buy at 93000 first to lower average cost"},
  {"symbol": "BTCUSDT", "action": "reduce_position", "quantity": 0.005, "confidence": 75, "reasoning": "Low buy order placed, now reduce 25%% of trapped long position to lower average cost"}
]

Example (Short trapped - T-Trade: sell higher first, then reduce):
[
  {"symbol": "BTCUSDT", "action": "place_sell_limit", "price": 107000, "quantity": 0.003, "level_index": 8, "confidence": 75, "reasoning": "Short trapped, loss 5%%, place sell at 107000 first to raise average entry price"},
  {"symbol": "BTCUSDT", "action": "reduce_position", "quantity": 0.005, "confidence": 75, "reasoning": "High sell order placed, now reduce 25%% of trapped short position to raise average cost"}
]
`, config.TrappedReduceThresholdPct, config.TrappedReduceBatchPct, config.TrappedReduceThresholdPct)
	}

	reduceAction := ""
	if config.EnableTrappedReduce {
		reduceAction = "\n- reduce_position: Batch reduce trapped position, quantity field specifies contracts to close"
	}

	return fmt.Sprintf(`# You are a Professional Grid Trading AI

## Role Definition
You are an experienced grid trading expert managing a grid strategy for %s. Your tasks are:
1. Assess current market regime (ranging/trending/volatile)
2. Decide whether to adjust grid or pause trading
3. Manage orders at each grid level

## Grid Configuration
- Symbol: %s
- Grid Levels: %d
- Total Investment: %.2f USDT
- Leverage: %dx
- Distribution: %s

## Decision Rules

### Market Regime Assessment
- **Ranging Market** (ideal for grid): Bollinger width < 3%%, EMA20/50 distance < 1%%, price near middle band
- **Trending Market** (pause grid): Bollinger width > 4%%, EMA20/50 distance > 2%%, price breaking bands
- **High Volatility** (caution): ATR spike, erratic price movement
%s
### Available Actions
- place_buy_limit: Place buy limit order at specified price
- place_sell_limit: Place sell limit order at specified price
- cancel_order: Cancel specific order
- cancel_all_orders: Cancel all orders
- pause_grid: Pause grid trading (in trending market)
- resume_grid: Resume grid trading (in ranging market)
- adjust_grid: Adjust grid boundaries%s
- hold: Maintain current state

## Output Format
Output JSON array, each decision contains:
- symbol: Trading pair
- action: Action type
- price: Price (for limit orders)
- quantity: Quantity
- level_index: Grid level index
- order_id: Order ID (for cancel)
- confidence: Confidence 0-100
- reasoning: Decision reason
`, config.Symbol, config.Symbol, config.GridCount, config.TotalInvestment, config.Leverage, config.Distribution, trappedSection, reduceAction)
}

// BuildGridUserPrompt builds the user prompt with current grid context
func BuildGridUserPrompt(ctx *GridContext, lang string) string {
	if lang == "zh" {
		return buildGridUserPromptZh(ctx)
	}
	return buildGridUserPromptEn(ctx)
}

func buildGridUserPromptZh(ctx *GridContext) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("## 当前时间: %s\n\n", ctx.CurrentTime))

	// Market data section
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

	// Box Indicator Section
	if ctx.BoxData != nil {
		sb.WriteString("## 箱体指标 (唐奇安通道)\n\n")
		sb.WriteString("| 箱体级别 | 上轨 | 下轨 | 宽度 |\n")
		sb.WriteString("|----------|------|------|------|\n")

		shortWidth := 0.0
		midWidth := 0.0
		longWidth := 0.0

		if ctx.BoxData.CurrentPrice > 0 {
			shortWidth = (ctx.BoxData.ShortUpper - ctx.BoxData.ShortLower) / ctx.BoxData.CurrentPrice * 100
			midWidth = (ctx.BoxData.MidUpper - ctx.BoxData.MidLower) / ctx.BoxData.CurrentPrice * 100
			longWidth = (ctx.BoxData.LongUpper - ctx.BoxData.LongLower) / ctx.BoxData.CurrentPrice * 100
		}

		sb.WriteString(fmt.Sprintf("| 短期 (3天) | %.2f | %.2f | %.2f%% |\n",
			ctx.BoxData.ShortUpper, ctx.BoxData.ShortLower, shortWidth))
		sb.WriteString(fmt.Sprintf("| 中期 (10天) | %.2f | %.2f | %.2f%% |\n",
			ctx.BoxData.MidUpper, ctx.BoxData.MidLower, midWidth))
		sb.WriteString(fmt.Sprintf("| 长期 (21天) | %.2f | %.2f | %.2f%% |\n",
			ctx.BoxData.LongUpper, ctx.BoxData.LongLower, longWidth))

		sb.WriteString(fmt.Sprintf("\n当前价格: %.2f\n", ctx.BoxData.CurrentPrice))

		// Check position relative to boxes
		price := ctx.BoxData.CurrentPrice
		if price > ctx.BoxData.LongUpper || price < ctx.BoxData.LongLower {
			sb.WriteString("⚠️ 突破: 价格突破长期箱体!\n")
		} else if price > ctx.BoxData.MidUpper || price < ctx.BoxData.MidLower {
			sb.WriteString("⚠️ 警告: 价格接近长期箱体边界\n")
		}
		sb.WriteString("\n")
	}

	// Account section
	sb.WriteString("## 账户状态\n")
	sb.WriteString(fmt.Sprintf("- 总权益: $%.2f\n", ctx.TotalEquity))
	sb.WriteString(fmt.Sprintf("- 可用余额: $%.2f\n", ctx.AvailableBalance))
	sb.WriteString(fmt.Sprintf("- 当前持仓: %.4f (净头寸)\n", ctx.CurrentPosition))
	sb.WriteString(fmt.Sprintf("- 未实现盈亏: $%.2f\n", ctx.UnrealizedPnL))
	sb.WriteString("\n")

	// Grid state section
	sb.WriteString("## 网格状态\n")
	sb.WriteString(fmt.Sprintf("- 网格范围: $%.2f - $%.2f\n", ctx.LowerPrice, ctx.UpperPrice))
	sb.WriteString(fmt.Sprintf("- 网格间距: $%.2f\n", ctx.GridSpacing))
	sb.WriteString(fmt.Sprintf("- 活跃订单数: %d\n", ctx.ActiveOrderCount))
	sb.WriteString(fmt.Sprintf("- 已成交层数: %d\n", ctx.FilledLevelCount))
	sb.WriteString(fmt.Sprintf("- 网格已暂停: %v\n", ctx.IsPaused))
	if ctx.CurrentDirection != "" {
		directionDescZh := map[string]string{
			"neutral":    "中性 (50%买+50%卖)",
			"long":       "做多 (100%买)",
			"short":      "做空 (100%卖)",
			"long_bias":  "偏多 (70%买+30%卖)",
			"short_bias": "偏空 (30%买+70%卖)",
		}
		desc := directionDescZh[ctx.CurrentDirection]
		if desc == "" {
			desc = ctx.CurrentDirection
		}
		sb.WriteString(fmt.Sprintf("- 网格方向: %s\n", desc))
	}
	sb.WriteString("\n")

	// Grid levels detail
	sb.WriteString("## 网格层级详情\n")
	sb.WriteString("| 层级 | 价格 | 状态 | 方向 | 订单数量 | 持仓数量 | 未实现盈亏 |\n")
	sb.WriteString("|------|------|------|------|----------|----------|------------|\n")
	for _, level := range ctx.Levels {
		sb.WriteString(fmt.Sprintf("| %d | $%.2f | %s | %s | %.4f | %.4f | $%.2f |\n",
			level.Index, level.Price, level.State, level.Side,
			level.OrderQuantity, level.PositionSize, level.UnrealizedPnL))
	}
	sb.WriteString("\n")

	// Performance section
	sb.WriteString("## 绩效统计\n")
	sb.WriteString(fmt.Sprintf("- 总利润: $%.2f\n", ctx.TotalProfit))
	sb.WriteString(fmt.Sprintf("- 总交易次数: %d\n", ctx.TotalTrades))
	sb.WriteString(fmt.Sprintf("- 胜率: %.1f%%\n", float64(ctx.WinningTrades)/float64(max(ctx.TotalTrades, 1))*100))
	sb.WriteString(fmt.Sprintf("- 最大回撤: %.2f%%\n", ctx.MaxDrawdown))
	sb.WriteString(fmt.Sprintf("- 今日盈亏: $%.2f\n", ctx.DailyPnL))
	sb.WriteString("\n")

	// Trapped position info
	if ctx.TrappedInfo != nil && ctx.TrappedInfo.IsTrapped {
		t := ctx.TrappedInfo
		sideZh := "多单（买入方向）"
		if t.Side == "sell" {
			sideZh = "空单（卖出方向）"
		}
		sb.WriteString("## ⚠️ 被套警告\n")
		sb.WriteString(fmt.Sprintf("- 被套状态: 是\n"))
		sb.WriteString(fmt.Sprintf("- 被套方向: %s\n", sideZh))
		sb.WriteString(fmt.Sprintf("- 未实现亏损: $%.2f\n", t.TotalUnrealizedLoss))
		sb.WriteString(fmt.Sprintf("- 亏损占比: %.2f%%\n", t.LossPct))
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
		// Show T-trade state and correct direction hint
		switch t.TTradePhase {
		case "waiting_buy_fill":
			sb.WriteString(fmt.Sprintf("- **T字状态: 等待挂单成交** (orderID=%s, 价格=%.2f, 待减仓=%.4f)\n",
				t.TTradeBuyOrderID, t.TTradeBuyPrice, t.TTradePendingReduce))
			sb.WriteString("- ⛔ **系统正在等待T字挂单成交后自动执行减仓，本轮请勿重复下单或 reduce_position**\n")
		default:
			sb.WriteString("- T字状态: 空闲 (可执行T字操作)\n")
			if t.Side == "sell" {
				sb.WriteString("**⚡ 空单被套T字提示：先用 place_sell_limit 在【高位】挂卖单，再执行 reduce_position 减仓（提高平均入场价）**\n")
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

	// Market data section
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

	// Box Indicator Section
	if ctx.BoxData != nil {
		sb.WriteString("## Box Indicators (Donchian Channels)\n\n")
		sb.WriteString("| Box Level | Upper | Lower | Width |\n")
		sb.WriteString("|-----------|-------|-------|-------|\n")

		shortWidth := 0.0
		midWidth := 0.0
		longWidth := 0.0

		if ctx.BoxData.CurrentPrice > 0 {
			shortWidth = (ctx.BoxData.ShortUpper - ctx.BoxData.ShortLower) / ctx.BoxData.CurrentPrice * 100
			midWidth = (ctx.BoxData.MidUpper - ctx.BoxData.MidLower) / ctx.BoxData.CurrentPrice * 100
			longWidth = (ctx.BoxData.LongUpper - ctx.BoxData.LongLower) / ctx.BoxData.CurrentPrice * 100
		}

		sb.WriteString(fmt.Sprintf("| Short (3d) | %.2f | %.2f | %.2f%% |\n",
			ctx.BoxData.ShortUpper, ctx.BoxData.ShortLower, shortWidth))
		sb.WriteString(fmt.Sprintf("| Mid (10d) | %.2f | %.2f | %.2f%% |\n",
			ctx.BoxData.MidUpper, ctx.BoxData.MidLower, midWidth))
		sb.WriteString(fmt.Sprintf("| Long (21d) | %.2f | %.2f | %.2f%% |\n",
			ctx.BoxData.LongUpper, ctx.BoxData.LongLower, longWidth))

		sb.WriteString(fmt.Sprintf("\nCurrent Price: %.2f\n", ctx.BoxData.CurrentPrice))

		// Check position relative to boxes
		price := ctx.BoxData.CurrentPrice
		if price > ctx.BoxData.LongUpper || price < ctx.BoxData.LongLower {
			sb.WriteString("⚠️ BREAKOUT: Price outside long-term box!\n")
		} else if price > ctx.BoxData.MidUpper || price < ctx.BoxData.MidLower {
			sb.WriteString("⚠️ WARNING: Price approaching long-term box boundary\n")
		}
		sb.WriteString("\n")
	}

	// Account section
	sb.WriteString("## Account Status\n")
	sb.WriteString(fmt.Sprintf("- Total Equity: $%.2f\n", ctx.TotalEquity))
	sb.WriteString(fmt.Sprintf("- Available Balance: $%.2f\n", ctx.AvailableBalance))
	sb.WriteString(fmt.Sprintf("- Current Position: %.4f (net)\n", ctx.CurrentPosition))
	sb.WriteString(fmt.Sprintf("- Unrealized PnL: $%.2f\n", ctx.UnrealizedPnL))
	sb.WriteString("\n")

	// Grid state section
	sb.WriteString("## Grid Status\n")
	sb.WriteString(fmt.Sprintf("- Grid Range: $%.2f - $%.2f\n", ctx.LowerPrice, ctx.UpperPrice))
	sb.WriteString(fmt.Sprintf("- Grid Spacing: $%.2f\n", ctx.GridSpacing))
	sb.WriteString(fmt.Sprintf("- Active Orders: %d\n", ctx.ActiveOrderCount))
	sb.WriteString(fmt.Sprintf("- Filled Levels: %d\n", ctx.FilledLevelCount))
	sb.WriteString(fmt.Sprintf("- Grid Paused: %v\n", ctx.IsPaused))
	if ctx.CurrentDirection != "" {
		directionDescEn := map[string]string{
			"neutral":    "Neutral (50% buy + 50% sell)",
			"long":       "Long (100% buy)",
			"short":      "Short (100% sell)",
			"long_bias":  "Long Bias (70% buy + 30% sell)",
			"short_bias": "Short Bias (30% buy + 70% sell)",
		}
		desc := directionDescEn[ctx.CurrentDirection]
		if desc == "" {
			desc = ctx.CurrentDirection
		}
		sb.WriteString(fmt.Sprintf("- Grid Direction: %s\n", desc))
	}
	sb.WriteString("\n")

	// Grid levels detail
	sb.WriteString("## Grid Levels Detail\n")
	sb.WriteString("| Level | Price | State | Side | Order Qty | Position | Unrealized PnL |\n")
	sb.WriteString("|-------|-------|-------|------|-----------|----------|----------------|\n")
	for _, level := range ctx.Levels {
		sb.WriteString(fmt.Sprintf("| %d | $%.2f | %s | %s | %.4f | %.4f | $%.2f |\n",
			level.Index, level.Price, level.State, level.Side,
			level.OrderQuantity, level.PositionSize, level.UnrealizedPnL))
	}
	sb.WriteString("\n")

	// Performance section
	sb.WriteString("## Performance Stats\n")
	sb.WriteString(fmt.Sprintf("- Total Profit: $%.2f\n", ctx.TotalProfit))
	sb.WriteString(fmt.Sprintf("- Total Trades: %d\n", ctx.TotalTrades))
	sb.WriteString(fmt.Sprintf("- Win Rate: %.1f%%\n", float64(ctx.WinningTrades)/float64(max(ctx.TotalTrades, 1))*100))
	sb.WriteString(fmt.Sprintf("- Max Drawdown: %.2f%%\n", ctx.MaxDrawdown))
	sb.WriteString(fmt.Sprintf("- Daily PnL: $%.2f\n", ctx.DailyPnL))
	sb.WriteString("\n")

	// Trapped position info
	if ctx.TrappedInfo != nil && ctx.TrappedInfo.IsTrapped {
		t := ctx.TrappedInfo
		sb.WriteString("## ⚠️ TRAPPED POSITION WARNING\n")
		sb.WriteString("- Trapped: YES\n")
		trappedSideEn := "Long (buy direction)"
		if t.Side == "sell" {
			trappedSideEn = "Short (sell direction)"
		}
		sb.WriteString(fmt.Sprintf("- Trapped Side: %s\n", trappedSideEn))
		sb.WriteString(fmt.Sprintf("- Unrealized Loss: $%.2f\n", t.TotalUnrealizedLoss))
		sb.WriteString(fmt.Sprintf("- Loss Percentage: %.2f%%\n", t.LossPct))
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
		// Show T-trade state to prevent duplicate orders
		switch t.TTradePhase {
		case "waiting_buy_fill":
			sb.WriteString(fmt.Sprintf("- **T-Trade State: WAITING FOR BUY FILL** (orderID=%s, price=%.2f, pending reduce=%.4f)\n",
				t.TTradeBuyOrderID, t.TTradeBuyPrice, t.TTradePendingReduce))
			sb.WriteString("- ⛔ **System is waiting for T-trade order to fill before auto-executing the reduce. DO NOT issue additional T-trade orders or reduce_position this cycle.**\n")
		default:
			sb.WriteString("- T-Trade State: IDLE (ready for T-trade)\n")
			if t.Side == "sell" {
				sb.WriteString("**⚡ T-Trade tip (SHORT trapped): First use place_sell_limit to place a HIGH sell order, THEN execute reduce_position**\n")
			} else {
				sb.WriteString("**⚡ T-Trade tip (LONG trapped): First use place_buy_limit to place a LOW buy order, THEN execute reduce_position**\n")
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Please analyze the data above and make grid trading decisions\n")
	sb.WriteString("Output a JSON array of decisions.\n")

	return sb.String()
}

// ============================================================================
// Grid Decision Functions
// ============================================================================

// GetGridDecisions gets AI decisions for grid trading
func GetGridDecisions(ctx *GridContext, mcpClient mcp.AIClient, config *store.GridStrategyConfig, lang string) (*FullDecision, error) {
	startTime := time.Now()

	// Build prompts
	systemPrompt := BuildGridSystemPrompt(config, lang)
	userPrompt := BuildGridUserPrompt(ctx, lang)

	logger.Infof("🤖 [Grid] Calling AI for grid decisions...")

	// Call AI
	response, err := mcpClient.CallWithMessages(systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("AI call failed: %w", err)
	}

	// Parse decisions from response
	decisions, err := parseGridDecisions(response, ctx.Symbol)
	if err != nil {
		logger.Warnf("Failed to parse grid decisions: %v", err)
		// Return hold decision as fallback
		decisions = []Decision{{
			Symbol:     ctx.Symbol,
			Action:     "hold",
			Confidence: 50,
			Reasoning:  "Failed to parse AI response, holding current state",
		}}
	}

	duration := time.Since(startTime).Milliseconds()
	logger.Infof("⏱️ [Grid] AI call duration: %d ms, decisions: %d", duration, len(decisions))

	// Extract chain of thought from response
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
	// Try to find JSON array in response
	jsonStr := extractJSONArray(response)
	if jsonStr == "" {
		return nil, fmt.Errorf("no JSON array found in response")
	}

	var decisions []Decision
	if err := json.Unmarshal([]byte(jsonStr), &decisions); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Validate and set default symbol
	for i := range decisions {
		if decisions[i].Symbol == "" {
			decisions[i].Symbol = symbol
		}
		// Validate action
		if !isValidGridAction(decisions[i].Action) {
			logger.Warnf("Invalid grid action: %s", decisions[i].Action)
		}
	}

	return decisions, nil
}

// extractJSONArray extracts JSON array from AI response
func extractJSONArray(response string) string {
	// Try to find ```json code block first
	matches := reJSONFence.FindStringSubmatch(response)
	if len(matches) > 1 {
		return matches[1]
	}

	// Try to find raw JSON array
	matches = reJSONArray.FindStringSubmatch(response)
	if len(matches) > 0 {
		return matches[0]
	}

	return ""
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
		"reduce_position":   true, // batch reduce trapped positions (分批减仓)
		// Also support standard actions for compatibility
		"open_long":  true,
		"open_short": true,
		"close_long": true,
		"close_short": true,
	}
	return validActions[action]
}

// ============================================================================
// Grid Context Builder Helpers
// ============================================================================

// BuildGridContextFromMarketData builds grid context from market data
func BuildGridContextFromMarketData(mktData *market.Data, config *store.GridStrategyConfig) *GridContext {
	ctx := &GridContext{
		Symbol:       config.Symbol,
		CurrentTime:  time.Now().Format("2006-01-02 15:04:05"),
		CurrentPrice: mktData.CurrentPrice,

		// Grid config
		GridCount:       config.GridCount,
		TotalInvestment: config.TotalInvestment,
		Leverage:        config.Leverage,
		Distribution:    config.Distribution,

		// Market data
		PriceChange1h: mktData.PriceChange1h,
		PriceChange4h: mktData.PriceChange4h,
		FundingRate:   mktData.FundingRate,
	}

	// Extract indicators from timeframe data
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

	// Extract longer term context
	if mktData.LongerTermContext != nil {
		if ctx.ATR14 == 0 {
			ctx.ATR14 = mktData.LongerTermContext.ATR14
		}
		ctx.EMA50 = mktData.LongerTermContext.EMA50
	}

	ctx.EMA20 = mktData.CurrentEMA20
	ctx.MACD = mktData.CurrentMACD

	// Calculate EMA distance
	if ctx.EMA50 > 0 {
		ctx.EMADistance = (ctx.EMA20 - ctx.EMA50) / ctx.EMA50 * 100
	}

	return ctx
}

// Helper function for max
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

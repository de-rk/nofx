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
	DecisionHistory []Decision `json:"decision_history,omitempty"`
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
- T字顺序：先用 place_buy_limit 在**更低价**挂买单 → 再执行 reduce_position
- 示例：原均价$100，价格跌到$95，先在$93挂买单，再减仓50%%，新均价降至$96.5
- **直接减仓**：如不使用T字，可用 reduce_long 卖出平仓，价格应在**当前价或略高**争取更好价格（例如当前价$41.36，挂$41.40或$41.45）

**空单被套（side=sell，价格上涨亏损）**：
- ⚠️ **空单被套不适合T字操作**，因为在高价再次做空会增加风险
- 建议：使用 reduce_short 在合理价格平仓减仓
- **价格设置**：reduce_short 是买入平仓，应在**当前价或略低**争取更好价格（例如当前价$41.36，挂$41.30或$41.35）
- 如果亏损严重（>%.1f%%），可以在当前价格附近挂 reduce_short 限价单逐步减仓

**何时执行T字**：
- **仅对多单被套**：损失超过单仓仓位的 %.1f%% 且价格仍在下跌趋势

**T字操作策略**（仅适用于多单被套）：

**关键规则**：
- **仅对多单被套执行T字操作**
- **空单被套直接减仓，不做T字**
- trapped_info.side = "buy" → 多单被套 → place_buy_limit在低位
- trapped_info.side = "sell" → 空单被套 → 直接减仓或等待
- 挂单数量 = trapped_position_size × suggest_reduce_pct / 100
- 本周期只挂单，下周期等待成交，系统自动执行reduce

**执行步骤**：
1. 检查trapped_info.side确定被套方向
2. 如果side="buy"（多单被套）：周期1挂place_buy_limit低位，周期2等待
3. 如果side="sell"（空单被套）：直接输出hold或考虑减仓
4. 成交后系统自动reduce，无需手动操作

**何时不执行**：
- 损失 << %. %.1f%%
- RSI极端值（<<330或>70）且有反转信号

示例（空单被套，trapped_info.side="sell"）:
[
{"action": "reduce_short", "price": 41.30, "quantity": 7.5, "confidence": 75, "reasoning": "trapped_info.side=sell，空单被套亏损严重，使用reduce_short在41.30（略低于当前价41.36）挂限价单减仓10%"}
]

示例（多单被套，trapped_info.side="buy"）:
[
{"action": "place_buy_limit", "price": 93000, "quantity": 0.005, "reasoning": "trapped_info.side=buy，多单被套，低位93000挂买单"}
]
`, config.TrappedReduceThresholdPct, config.TrappedReduceBatchPct, config.TrappedReduceThresholdPct)
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
- **震荡市场** (适合网格): 布林带宽度 <<  3%%, EMA20/50 距离 <<  1%%, 价格在布林带中轨附近
- **趋势市场** (暂停网格): 布林带宽度 > 4%%, EMA20/50 距离 > 2%%, 价格持续突破布林带
- **高波动市场** (谨慎): ATR异常放大, 价格剧烈波动
%s
### 可执行的操作
- place_buy_limit: 在指定价格下买入限价单（开多仓或补网格）
- place_sell_limit: 在指定价格下卖出限价单（开空仓或补网格）
- reduce_long: 平多仓/减多仓，price字段指定限价，quantity字段指定减仓数量
- reduce_short: 平空仓/减空仓，price字段指定限价，quantity字段指定减仓数量
- cancel_order: 取消指定订单
- cancel_all_orders: 取消所有订单
- pause_grid: 暂停网格交易（趋势市场时）
- resume_grid: 恢复网格交易（震荡市场时）
- adjust_grid: 调整网格边界
- hold: 保持当前状态%s

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
	return "English prompt not implemented"
}

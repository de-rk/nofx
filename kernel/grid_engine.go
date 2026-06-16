package kernel

import (
	"encoding/json"
	"fmt"
	"nofx/logger"
	"nofx/market"
	"nofx/mcp"
	"nofx/store"
	"regexp"
	"strings"
	"time"
)

// DecisionSummary is a condensed version of a decision for memory
type DecisionSummary struct {
	Timestamp string  `json:"timestamp"`
	Action    string  `json:"action"`
	Reasoning string  `json:"reasoning"`
	Price     float64 `json:"price"`
	Result    string  `json:"result"` // "ok", "failed: <reason>", or ""
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
		logger.Warnf("Failed to parse grid decisions: %v\nRaw response: %s", err, response)
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
	// Fallback: if no CoT prefix, build trace from each decision's reasoning field
	if cotTrace == "" && len(decisions) > 0 {
		var sb strings.Builder
		for _, d := range decisions {
			if d.Reasoning != "" {
				sb.WriteString(fmt.Sprintf("[%s] %s\n\n", d.Action, d.Reasoning))
			}
		}
		cotTrace = strings.TrimSpace(sb.String())
	}

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

// reTrailingQuoteNum matches a stray quote after a number value: e.g. "price":36.75"
var reTrailingQuoteNum = regexp.MustCompile(`(\d)"(\s*[,}\]])`)

// reTrailingQuoteStr matches a stray extra quote after a string value: e.g. "action":"hold""
var reTrailingQuoteStr = regexp.MustCompile(`([^\\])""\s*([,}\]])`)

// reTrailingComma matches trailing commas before ] or }
var reTrailingComma = regexp.MustCompile(`,\s*([}\]])`)

// reCodeFence matches any fenced code block (with or without language tag)
var reCodeFence = regexp.MustCompile("(?is)```(?:\\w+)?\\s*([\\s\\S]*?)```")

// reJSONObject matches a single JSON object
var reJSONObject = regexp.MustCompile(`(?is)\{.*?\}`)

// sanitizeGridJSON cleans common AI JSON formatting errors
func sanitizeGridJSON(s string) string {
	// Fix curly/smart quotes
	s = strings.ReplaceAll(s, "\u201c", "\"")
	s = strings.ReplaceAll(s, "\u201d", "\"")
	s = strings.ReplaceAll(s, "\u2018", "'")
	s = strings.ReplaceAll(s, "\u2019", "'")
	// Fix stray trailing quote after a number: 36.75" → 36.75
	s = reTrailingQuoteNum.ReplaceAllString(s, `$1$2`)
	// Fix double closing quote after string value: "hold"" → "hold"
	s = reTrailingQuoteStr.ReplaceAllString(s, `$1"$2`)
	// Remove trailing commas before ] or }
	s = reTrailingComma.ReplaceAllString(s, `$1`)
	return s
}

// extractJSONArray extracts a JSON array from an AI response string.
// Handles: ```json [...] ```, bare [...], single object {...} wrapped into array.
func extractJSONArray(response string) string {
	// 1. Fenced code block with json tag
	if m := reJSONFence.FindStringSubmatch(response); len(m) > 1 {
		return m[1]
	}
	// 2. Fenced code block without json tag
	if m := reCodeFence.FindStringSubmatch(response); len(m) > 1 {
		candidate := strings.TrimSpace(m[1])
		if strings.HasPrefix(candidate, "[") || strings.HasPrefix(candidate, "{") {
			if strings.HasPrefix(candidate, "{") {
				candidate = "[" + candidate + "]"
			}
			return candidate
		}
	}
	// 3. Bare JSON array
	if m := reJSONArray.FindString(response); m != "" {
		return m
	}
	// 4. Single JSON object — wrap into array
	if m := reJSONObject.FindString(response); m != "" {
		return "[" + m + "]"
	}
	return ""
}
func parseGridDecisions(response string, symbol string) ([]Decision, error) {
	jsonStr := extractJSONArray(response)
	if jsonStr == "" {
		return nil, fmt.Errorf("no JSON array found in response")
	}

	jsonStr = sanitizeGridJSON(jsonStr)

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
		TTradeSpreadPct: func() float64 {
			if config.TTradeSpreadPct >= 0.2 {
				return config.TTradeSpreadPct
			}
			return 0.2
		}(),
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

	// T-trade spread config
	TTradeSpreadPct float64 `json:"t_trade_spread_pct"`

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
	TTradePhase         string  `json:"t_trade_phase"`           // "idle" | "waiting_buy_fill" | "waiting_reduce_fill"
	TTradeBuyOrderID    string  `json:"t_trade_buy_order_id"`    // first pending T-trade buy order ID (if waiting)
	TTradeBuyPrice      float64 `json:"t_trade_buy_price"`       // price of first pending T-trade buy
	TTradePendingReduce float64 `json:"t_trade_pending_reduce"`  // total qty tagged / pending reduce
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
		threshold := config.TTradePositionThresholdPct
		if threshold <= 0 {
			threshold = 30.0
		}
		trappedSection = fmt.Sprintf(`
## T字操作（仓位控制减仓）
触发条件：任一方向仓位占总资金超过 %.0f%% 时，系统自动标记该方向所有符合条件的网格挂单为触发单（多仓超阈值→标记价格下方买单；空仓超阈值→标记价格上方卖单）。多空两个方向可同时独立运行。

**状态说明：**
| 状态 | 含义 | 你需要做什么 |
|------|------|------------|
| idle | 两个方向仓位均未超阈值 | 正常补单，系统自动监控 |
| waiting_buy_fill | 多仓触发单挂出，等待成交 | 正常补网格单，**禁止执行 reduce_long/reduce_short** |
| waiting_reduce_fill | 减仓单已挂出，等待成交 | 正常补网格单，**禁止执行 reduce_long/reduce_short** |

**注意：** reduce_long/reduce_short 由系统在触发单成交后自动挂出，无需 AI 执行。
`, threshold)
	}

	return fmt.Sprintf(`# 网格交易 AI — %s

## 角色定位
你是双向网格交易的执行引擎。每个周期完成三件事：
1. 判断市场状态，确定方向偏好
2. 补全空缺网格层级
3. 处理特殊状态（T字操作）

## 网格配置
交易对: %s | 层数: %d | 投资额: %.2f USDT | 杠杆: %dx | 分布: %s

## 市场状态判断
| 状态 | 条件 | 操作 |
|------|------|------|
| 震荡 | 布林带宽 < 3%%, EMA距离 < 1%% | 正常补单，多空均衡 |
| 趋势上行 | 布林带宽 > 4%%, 价格持续突破上轨 | 反向布局：买单40%%，卖单60%%，逢高挂卖单吃回调 |
| 趋势下行 | 布林带宽 > 4%%, 价格持续突破下轨 | 反向布局：买单60%%，卖单40%%，逢低挂买单吃反弹 |
| 高波动 | ATR 异常放大 | pause_grid |

## 操作指令

### 网格补单
- place_buy_limit：在买方空层开多仓
- place_sell_limit：在卖方空层开空仓
- quantity 必须使用层级表中的「建议数量」
- state = "pending" 的层级已有挂单，禁止重复下单

### 减仓指令（由系统自动执行，AI 禁止主动使用）
- reduce_long / reduce_short 由系统在 T字操作触发单成交后自动挂出
- ⚠️ AI 在任何状态下都不应主动执行 reduce_long/reduce_short

### 其他
- cancel_order / cancel_all_orders：撤单
- pause_grid / resume_grid：暂停/恢复
- adjust_grid：重新计算网格边界
- hold：本周期不操作

### 余额为零时的处理
当 available_balance ≤ $1 时，系统会忽略所有下单决策。此时你**不应**尝试下单，而应：
1. 输出 `hold`
2. 在 reasoning 中分析当前局面：现有挂单分布、持仓方向与盈亏、被套状态、下一步可能的触发条件（例如某个 T-trade 减仓单成交后余额释放）
3. 该分析会被保存到 Decision History，供后续周期参考
%s
## 输出格式
JSON 数组，每个决策一个对象：
[{"symbol":"...","action":"...","price":0.0,"quantity":0.0,"level_index":0,"confidence":0,"reasoning":"..."}]
**每个周期最多输出 8 个下单决策（place_buy_limit / place_sell_limit 合计），超出部分会被忽略。**
**reasoning 字段保持简洁，不超过 2 句话。**
`, config.Symbol, config.Symbol, config.GridCount, config.TotalInvestment, config.Leverage, config.Distribution, trappedSection)
}

func buildGridSystemPromptEn(config *store.GridStrategyConfig) string {
	trappedSection := ""
	if config.EnableTrappedReduce {
		threshold := config.TTradePositionThresholdPct
		if threshold <= 0 {
			threshold = 30.0
		}
		trappedSection = fmt.Sprintf(`
## T-Trade (Position Size Reduction)
Trigger: system auto-tags qualifying grid orders when either side's position exceeds %.0f%% of total investment (long > threshold → tag buy orders below price; short > threshold → tag sell orders above price). Both sides run independently and simultaneously.

**Three states — your responsibility differs per state:**
| State | Meaning | Your action |
|-------|---------|-------------|
| idle | Neither side exceeds threshold | Normal grid orders, system monitors |
| waiting_buy_fill | Trigger order(s) placed, awaiting fill | Continue normal grid orders — do NOT execute reduce_long/reduce_short |
| waiting_reduce_fill | Reduce order(s) placed, awaiting fill | Continue normal grid orders — do NOT execute reduce_long/reduce_short |

**Note:** reduce_long/reduce_short are placed automatically by the system after trigger orders fill. AI must never use them.
`, threshold)
	}

	return fmt.Sprintf(`# Grid Trading AI — %s

## Role
You are the execution engine for a bidirectional grid strategy. Each cycle:
1. Assess market conditions and set directional bias
2. Fill empty grid levels with orders
3. Handle special states (T-trade)

## Grid Configuration
Symbol: %s | Levels: %d | Investment: %.2f USDT | Leverage: %dx | Distribution: %s

## Market State
| State | Conditions | Action |
|-------|-----------|--------|
| Ranging | BB width < 3%%, EMA distance < 1%% | Normal grid, balanced long/short |
| Uptrend | BB width > 4%%, price breaking upper band | Contrarian: 40%% buy / 60%% sell — place sell orders into strength |
| Downtrend | BB width > 4%%, price breaking lower band | Contrarian: 60%% buy / 40%% sell — place buy orders into weakness |
| High volatility | ATR abnormally large | pause_grid |

## Instructions

### Grid Orders
- place_buy_limit: open long at an empty buy-side level
- place_sell_limit: open short at an empty sell-side level
- quantity must use "Suggested Qty" from the level table
- levels with state = "pending" already have an order — do NOT place another

### Reduce Orders (auto-placed by system — AI must NOT use)
- reduce_long / reduce_short are placed automatically after trigger orders fill
- ⚠️ Do NOT use reduce_long or reduce_short in any state

### Other
- cancel_order / cancel_all_orders: cancel orders
- pause_grid / resume_grid: pause or resume
- adjust_grid: recalculate grid bounds
- hold: no action this cycle

### Zero-Balance Handling
When available_balance ≤ $1, the system discards all order placement decisions. In this state you **must not** attempt to place orders. Instead:
1. Output `hold`
2. In the reasoning, analyze the current situation: existing order distribution, position direction and PnL, trapped state, and what would unlock capital next (e.g. a T-trade reduce fill, a grid order fill releasing margin)
3. This analysis is saved to Decision History for reference in future cycles
%s
## Output Format
JSON array, one object per decision:
[{"symbol":"...","action":"...","price":0.0,"quantity":0.0,"level_index":0,"confidence":0,"reasoning":"..."}]
**Maximum 8 order decisions (place_buy_limit + place_sell_limit combined) per cycle — excess will be ignored.**
**Keep reasoning concise — 2 sentences max.**
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

	// T-trade state first — highest priority
	if ctx.TrappedInfo != nil && ctx.TrappedInfo.IsTrapped {
		t := ctx.TrappedInfo
		sideZh := "多单"
		if t.Side == "sell" {
			sideZh = "空单"
		}
		sb.WriteString("## ⚠️ 被套状态\n")
		sb.WriteString(fmt.Sprintf("- 方向: %s | 亏损: $%.2f (%.2f%%) | 均价: $%.2f | 当前: $%.2f | 价差: %.2f%%\n",
			sideZh, t.TotalUnrealizedLoss, t.LossPct, t.AvgEntryPrice, t.CurrentPrice, t.PriceDiffPct))
		switch t.TTradePhase {
		case "waiting_buy_fill":
			label := "买单"
			if t.Side == "sell" {
				label = "卖单"
			}
			sb.WriteString(fmt.Sprintf("- T字状态: **等待%s成交** (首单=%s @ %.2f, 总待减仓=%.4f)\n",
				label, t.TTradeBuyOrderID, t.TTradeBuyPrice, t.TTradePendingReduce))
			sb.WriteString("- ⛔ 禁止执行 reduce_long/reduce_short，减仓单由系统自动挂出，正常补网格单\n")
		case "waiting_reduce_fill":
			sb.WriteString(fmt.Sprintf("- T字状态: **⏳ 减仓单已挂出，等待成交** (总待减仓=%.4f)\n", t.TTradePendingReduce))
			sb.WriteString("- ⛔ 禁止执行 reduce_long/reduce_short，正常补网格单\n")
		default:
			sb.WriteString("- T字状态: 空闲\n")
		}
		sb.WriteString("\n")
	}

	// Decision history
	if len(ctx.DecisionHistory) > 0 {
		sb.WriteString("## 历史决策\n")
		sb.WriteString("| 时间 | 操作 | 价格 | 结果 | 理由 |\n")
		sb.WriteString("|------|------|------|------|------|\n")
		for _, d := range ctx.DecisionHistory {
			result := d.Result
			if result == "" {
				result = "-"
			}
			sb.WriteString(fmt.Sprintf("| %s | %s | %.2f | %s | %s |\n", d.Timestamp, d.Action, d.Price, result, d.Reasoning))
		}
		sb.WriteString("\n")
	}

	// Market data
	sb.WriteString("## 市场数据\n")
	sb.WriteString(fmt.Sprintf("- 价格: $%.2f | 1h: %.2f%% | 4h: %.2f%%\n", ctx.CurrentPrice, ctx.PriceChange1h, ctx.PriceChange4h))
	sb.WriteString(fmt.Sprintf("- ATR14: $%.2f (%.2f%%) | RSI14: %.1f | 资金费率: %.4f%%\n",
		ctx.ATR14, ctx.ATR14/ctx.CurrentPrice*100, ctx.RSI14, ctx.FundingRate*100))
	sb.WriteString(fmt.Sprintf("- 布林带: 上 $%.2f / 中 $%.2f / 下 $%.2f | 带宽: %.2f%%\n",
		ctx.BollingerUpper, ctx.BollingerMiddle, ctx.BollingerLower, ctx.BollingerWidth))
	sb.WriteString(fmt.Sprintf("- EMA20: $%.2f | EMA50: $%.2f | 距离: %.2f%%\n", ctx.EMA20, ctx.EMA50, ctx.EMADistance))
	sb.WriteString(fmt.Sprintf("- MACD: %.4f | Signal: %.4f | Hist: %.4f\n", ctx.MACD, ctx.MACDSignal, ctx.MACDHistogram))
	sb.WriteString("\n")

	if ctx.BoxData != nil {
		sb.WriteString("## 箱体 (唐奇安)\n")
		sb.WriteString("| 级别 | 上轨 | 下轨 | 宽度 |\n")
		sb.WriteString("|------|------|------|------|\n")
		p := ctx.BoxData.CurrentPrice
		if p > 0 {
			sb.WriteString(fmt.Sprintf("| 短期3天 | %.2f | %.2f | %.2f%% |\n", ctx.BoxData.ShortUpper, ctx.BoxData.ShortLower, (ctx.BoxData.ShortUpper-ctx.BoxData.ShortLower)/p*100))
			sb.WriteString(fmt.Sprintf("| 中期10天 | %.2f | %.2f | %.2f%% |\n", ctx.BoxData.MidUpper, ctx.BoxData.MidLower, (ctx.BoxData.MidUpper-ctx.BoxData.MidLower)/p*100))
			sb.WriteString(fmt.Sprintf("| 长期21天 | %.2f | %.2f | %.2f%% |\n", ctx.BoxData.LongUpper, ctx.BoxData.LongLower, (ctx.BoxData.LongUpper-ctx.BoxData.LongLower)/p*100))
		}
		if p > ctx.BoxData.LongUpper || p < ctx.BoxData.LongLower {
			sb.WriteString("⚠️ 价格突破长期箱体\n")
		} else if p > ctx.BoxData.MidUpper || p < ctx.BoxData.MidLower {
			sb.WriteString("⚠️ 价格接近长期箱体边界\n")
		}
		sb.WriteString("\n")
	}

	// Account
	sb.WriteString("## 账户\n")
	sb.WriteString(fmt.Sprintf("- 可用余额: $%.2f | 总权益: $%.2f | 未实现盈亏: $%.2f\n",
		ctx.AvailableBalance, ctx.TotalEquity, ctx.UnrealizedPnL))
	if ctx.LongPosition != 0 || ctx.ShortPosition != 0 {
		sb.WriteString(fmt.Sprintf("- 多头: %.4f | 空头: %.4f\n", ctx.LongPosition, ctx.ShortPosition))
	}
	sb.WriteString("\n")

	// Grid status
	sb.WriteString("## 网格状态\n")
	sb.WriteString(fmt.Sprintf("- 范围: $%.2f - $%.2f | 间距: $%.2f | 活跃订单: %d | 已成交: %d | 暂停: %v\n",
		ctx.LowerPrice, ctx.UpperPrice, ctx.GridSpacing, ctx.ActiveOrderCount, ctx.FilledLevelCount, ctx.IsPaused))
	sb.WriteString("\n")

	// Grid levels
	sb.WriteString("## 网格层级\n")
	sb.WriteString("| 层级 | 价格 | 状态 | 方向 | 分配USD | 建议数量 | 订单数量 | 持仓 | 浮盈 |\n")
	sb.WriteString("|------|------|------|------|---------|----------|----------|------|------|\n")
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

	// Performance
	sb.WriteString("## 绩效\n")
	sb.WriteString(fmt.Sprintf("- 总利润: $%.2f | 交易次数: %d | 胜率: %.1f%% | 最大回撤: %.2f%% | 今日盈亏: $%.2f\n",
		ctx.TotalProfit, ctx.TotalTrades,
		float64(ctx.WinningTrades)/float64(max(ctx.TotalTrades, 1))*100,
		ctx.MaxDrawdown, ctx.DailyPnL))
	sb.WriteString("\n")

	sb.WriteString("请分析以上数据，输出JSON数组格式的决策。\n")
	return sb.String()
}

func buildGridUserPromptEn(ctx *GridContext) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("## Current Time: %s\n\n", ctx.CurrentTime))

	// T-trade state first — highest priority
	if ctx.TrappedInfo != nil && ctx.TrappedInfo.IsTrapped {
		t := ctx.TrappedInfo
		sideEn := "Long"
		if t.Side == "sell" {
			sideEn = "Short"
		}
		sb.WriteString("## ⚠️ Trapped Position\n")
		sb.WriteString(fmt.Sprintf("- Side: %s | Loss: $%.2f (%.2f%%) | Avg Entry: $%.2f | Current: $%.2f | Diff: %.2f%%\n",
			sideEn, t.TotalUnrealizedLoss, t.LossPct, t.AvgEntryPrice, t.CurrentPrice, t.PriceDiffPct))
		switch t.TTradePhase {
		case "waiting_buy_fill":
			label := "BUY"
			if t.Side == "sell" {
				label = "SELL"
			}
			sb.WriteString(fmt.Sprintf("- T-Trade: **WAITING FOR %s FILL** (first=%s @ %.2f, total pending=%.4f)\n",
				label, t.TTradeBuyOrderID, t.TTradeBuyPrice, t.TTradePendingReduce))
			sb.WriteString("- ⛔ Do NOT execute reduce_long/reduce_short — reduces are auto-placed by system, continue normal grid orders\n")
		case "waiting_reduce_fill":
			sb.WriteString(fmt.Sprintf("- T-Trade: **⏳ REDUCE ORDER(S) PLACED, AWAITING FILL** (total pending=%.4f)\n", t.TTradePendingReduce))
			sb.WriteString("- ⛔ Do NOT execute reduce_long/reduce_short — continue normal grid orders\n")
		default:
			sb.WriteString("- T-Trade: IDLE\n")
		}
		sb.WriteString("\n")
	}

	// Decision history
	if len(ctx.DecisionHistory) > 0 {
		sb.WriteString("## Decision History\n")
		sb.WriteString("| Time | Action | Price | Result | Reasoning |\n")
		sb.WriteString("|------|--------|-------|--------|----------|\n")
		for _, d := range ctx.DecisionHistory {
			result := d.Result
			if result == "" {
				result = "-"
			}
			sb.WriteString(fmt.Sprintf("| %s | %s | %.2f | %s | %s |\n", d.Timestamp, d.Action, d.Price, result, d.Reasoning))
		}
		sb.WriteString("\n")
	}

	// Market data
	sb.WriteString("## Market Data\n")
	sb.WriteString(fmt.Sprintf("- Price: $%.2f | 1h: %.2f%% | 4h: %.2f%%\n", ctx.CurrentPrice, ctx.PriceChange1h, ctx.PriceChange4h))
	sb.WriteString(fmt.Sprintf("- ATR14: $%.2f (%.2f%%) | RSI14: %.1f | Funding: %.4f%%\n",
		ctx.ATR14, ctx.ATR14/ctx.CurrentPrice*100, ctx.RSI14, ctx.FundingRate*100))
	sb.WriteString(fmt.Sprintf("- BB: Upper $%.2f / Mid $%.2f / Lower $%.2f | Width: %.2f%%\n",
		ctx.BollingerUpper, ctx.BollingerMiddle, ctx.BollingerLower, ctx.BollingerWidth))
	sb.WriteString(fmt.Sprintf("- EMA20: $%.2f | EMA50: $%.2f | Distance: %.2f%%\n", ctx.EMA20, ctx.EMA50, ctx.EMADistance))
	sb.WriteString(fmt.Sprintf("- MACD: %.4f | Signal: %.4f | Hist: %.4f\n", ctx.MACD, ctx.MACDSignal, ctx.MACDHistogram))
	sb.WriteString("\n")

	if ctx.BoxData != nil {
		sb.WriteString("## Donchian Channels\n")
		sb.WriteString("| Level | Upper | Lower | Width |\n")
		sb.WriteString("|-------|-------|-------|-------|\n")
		p := ctx.BoxData.CurrentPrice
		if p > 0 {
			sb.WriteString(fmt.Sprintf("| Short 3d | %.2f | %.2f | %.2f%% |\n", ctx.BoxData.ShortUpper, ctx.BoxData.ShortLower, (ctx.BoxData.ShortUpper-ctx.BoxData.ShortLower)/p*100))
			sb.WriteString(fmt.Sprintf("| Mid 10d | %.2f | %.2f | %.2f%% |\n", ctx.BoxData.MidUpper, ctx.BoxData.MidLower, (ctx.BoxData.MidUpper-ctx.BoxData.MidLower)/p*100))
			sb.WriteString(fmt.Sprintf("| Long 21d | %.2f | %.2f | %.2f%% |\n", ctx.BoxData.LongUpper, ctx.BoxData.LongLower, (ctx.BoxData.LongUpper-ctx.BoxData.LongLower)/p*100))
		}
		if p > ctx.BoxData.LongUpper || p < ctx.BoxData.LongLower {
			sb.WriteString("⚠️ Price outside long-term box\n")
		} else if p > ctx.BoxData.MidUpper || p < ctx.BoxData.MidLower {
			sb.WriteString("⚠️ Price approaching long-term box boundary\n")
		}
		sb.WriteString("\n")
	}

	// Account
	sb.WriteString("## Account\n")
	sb.WriteString(fmt.Sprintf("- Available: $%.2f | Equity: $%.2f | Unrealized PnL: $%.2f\n",
		ctx.AvailableBalance, ctx.TotalEquity, ctx.UnrealizedPnL))
	if ctx.LongPosition != 0 || ctx.ShortPosition != 0 {
		sb.WriteString(fmt.Sprintf("- Long: %.4f | Short: %.4f\n", ctx.LongPosition, ctx.ShortPosition))
	}
	sb.WriteString("\n")

	// Grid status
	sb.WriteString("## Grid Status\n")
	sb.WriteString(fmt.Sprintf("- Range: $%.2f - $%.2f | Spacing: $%.2f | Active: %d | Filled: %d | Paused: %v\n",
		ctx.LowerPrice, ctx.UpperPrice, ctx.GridSpacing, ctx.ActiveOrderCount, ctx.FilledLevelCount, ctx.IsPaused))
	sb.WriteString("\n")

	// Grid levels
	sb.WriteString("## Grid Levels\n")
	sb.WriteString("| Level | Price | State | Side | Alloc USD | Suggested Qty | Order Qty | Position | PnL |\n")
	sb.WriteString("|-------|-------|-------|------|-----------|---------------|-----------|----------|-----|\n")
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

	// Performance
	sb.WriteString("## Performance\n")
	sb.WriteString(fmt.Sprintf("- Profit: $%.2f | Trades: %d | Win Rate: %.1f%% | Max DD: %.2f%% | Daily PnL: $%.2f\n",
		ctx.TotalProfit, ctx.TotalTrades,
		float64(ctx.WinningTrades)/float64(max(ctx.TotalTrades, 1))*100,
		ctx.MaxDrawdown, ctx.DailyPnL))
	sb.WriteString("\n")

	sb.WriteString("Analyze the data above and output a JSON array of decisions.\n")
	return sb.String()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

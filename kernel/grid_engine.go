package kernel

import (
	"encoding/json"
	"fmt"
	"math"
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
	parseFailed := err != nil
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
		ParseFailed:         parseFailed,
	}, nil
}

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
		if normalized := normalizeGridAction(decisions[i].Action); normalized != decisions[i].Action {
			logger.Infof("Normalized grid action %q -> %q", decisions[i].Action, normalized)
			decisions[i].Action = normalized
		}
		if !isValidGridAction(decisions[i].Action) {
			logger.Warnf("Invalid grid action: %s", decisions[i].Action)
		}
	}

	return decisions, nil
}

// gridActionSynonyms maps action names some AI providers/models emit instead of
// the exact vocabulary given in the system prompt (BuildGridSystemPrompt) to the
// canonical action executeGridDecision's switch understands. Not every model
// follows the prompt's action list precisely — one observed model returned
// "WAIT_FOR_CONFIRMATION", "MONITOR_STOP_LOSS", "ADD_BUY_ORDERS",
// "REBALANCE_GRID", and uppercase "CANCEL_ORDER" instead of the documented
// lowercase verbs. Without this mapping, such actions fall through the
// execution switch to its default case: no order placed, no error returned,
// nothing visibly wrong — the grid just silently stops being maintained every
// cycle. Keys are matched case-insensitively (normalizeGridAction lowercases
// first); values must be one of the canonical actions in isValidGridAction.
var gridActionSynonyms = map[string]string{
	"wait":                  "hold",
	"wait_for_confirmation": "hold",
	"monitor":               "hold",
	"monitor_position":      "hold",
	"monitor_stop_loss":     "hold",
	"no_action":             "hold",
	"add_buy_order":         "place_buy_limit",
	"add_buy_orders":        "place_buy_limit",
	"open_buy_limit":        "place_buy_limit",
	"add_sell_order":        "place_sell_limit",
	"add_sell_orders":       "place_sell_limit",
	"open_sell_limit":       "place_sell_limit",
	"rebalance":             "adjust_grid",
	"rebalance_grid":        "adjust_grid",
	"reset_grid":            "adjust_grid",
	"cancel_all":            "cancel_all_orders",
}

// normalizeGridAction lowercases action and, if it matches a known synonym
// (see gridActionSynonyms), rewrites it to the canonical action name. Actions
// that are already canonical (just differently-cased, e.g. "CANCEL_ORDER")
// are returned lowercased; unrecognized actions are returned lowercased
// as-is so isValidGridAction's warning still fires for genuinely unknown ones.
func normalizeGridAction(action string) string {
	lower := strings.ToLower(strings.TrimSpace(action))
	if canonical, ok := gridActionSynonyms[lower]; ok {
		return canonical
	}
	return lower
}

// isValidGridAction checks if action is a valid grid action
func isValidGridAction(action string) bool {
	validActions := map[string]bool{
		"place_buy_limit":   true,
		"place_sell_limit":  true,
		"cancel_order":      true,
		"cancel_all_orders": true,
		"adjust_grid":       true,
		"pause_grid":        false,
		"resume_grid":       true,
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
			primaryTf := config.AITriggerTf
			if primaryTf == "" {
				primaryTf = "5m"
			}
			if tf5m, ok := mktData.TimeframeData[primaryTf]; ok {
				if len(tf5m.BOLLUpper) > 0 {
					ctx.BollingerUpper = tf5m.BOLLUpper[len(tf5m.BOLLUpper)-1]
					ctx.BollingerMiddle = tf5m.BOLLMiddle[len(tf5m.BOLLMiddle)-1]
					ctx.BollingerLower = tf5m.BOLLLower[len(tf5m.BOLLLower)-1]
					if ctx.BollingerMiddle > 0 {
						ctx.BollingerWidth = (ctx.BollingerUpper - ctx.BollingerLower) / ctx.BollingerMiddle * 100
					}
				}
				// Grid range calculations use the 4h ATR, matching initialization.
				if fourHour, ok := mktData.TimeframeData["4h"]; ok {
					ctx.ATR14 = fourHour.ATR14
				}
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
	Index         int       `json:"index"`           // Level index (0 = lowest)
	Price         float64   `json:"price"`           // Target price for this level
	State         string    `json:"state"`           // "empty", "pending", "filled"
	Side          string    `json:"side"`            // "buy" or "sell"
	OrderID       string    `json:"order_id"`        // Current order ID (if pending)
	OrderQuantity float64   `json:"order_quantity"`  // Order quantity
	PositionSize  float64   `json:"position_size"`   // Position size (if filled)
	PositionEntry float64   `json:"position_entry"`  // Entry price (if filled)
	AllocatedUSD  float64   `json:"allocated_usd"`   // USD allocated to this level
	UnrealizedPnL float64   `json:"unrealized_pnl"`  // Unrealized P&L (if filled)
	DistancePct   float64   `json:"distance_pct"`    // % distance from current price (+ = above, - = below)
	OrderPlacedAt time.Time `json:"order_placed_at"` // When the current order was placed (for grace period)
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
	Levels           []GridLevelInfo `json:"levels"`
	ActiveOrderCount int             `json:"active_order_count"`
	FilledLevelCount int             `json:"filled_level_count"`
	IsPaused         bool            `json:"is_paused"`

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
	ShortPosition    float64 `json:"short_position"`   // Short position size
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

	// T-trade protected order IDs — AI must never cancel these
	TTradeProtectedOrderIDs []string `json:"t_trade_protected_order_ids,omitempty"`
}

// TrappedPositionInfo contains information about trapped (losing) positions
type TrappedPositionInfo struct {
	IsTrapped           bool    `json:"is_trapped"`            // whether currently trapped
	Side                string  `json:"side"`                  // "buy" (long trapped) or "sell" (short trapped)
	TotalUnrealizedLoss float64 `json:"total_unrealized_loss"` // total USD loss
	LossPct             float64 `json:"loss_pct"`              // loss as % of total investment
	TrappedLevelCount   int     `json:"trapped_level_count"`   // number of losing levels
	ThresholdPct        float64 `json:"threshold_pct"`         // configured trigger threshold %
	TrappedPositionSize float64 `json:"trapped_position_size"` // total size of trapped position
	AvgEntryPrice       float64 `json:"avg_entry_price"`       // weighted average entry price
	CurrentPrice        float64 `json:"current_price"`         // current market price
	PriceDiffPct        float64 `json:"price_diff_pct"`        // (avgEntry - current) / avgEntry * 100
	SuggestReducePct    float64 `json:"suggest_reduce_pct"`    // suggested reduction percentage
	LastReduceMinutes   int     `json:"last_reduce_minutes"`   // minutes since last reduction (-1 = never)
	// T-trade state (T字状态)
	TTradePhase         string  `json:"t_trade_phase"`          // "idle" | "waiting_buy_fill" | "waiting_reduce_fill"
	TTradeBuyOrderID    string  `json:"t_trade_buy_order_id"`   // first pending T-trade buy order ID (if waiting)
	TTradeBuyPrice      float64 `json:"t_trade_buy_price"`      // price of first pending T-trade buy
	TTradePendingReduce float64 `json:"t_trade_pending_reduce"` // total qty tagged / pending reduce
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
	ttradeNote := ""
	if config.EnableTrappedReduce {
		ttradeNote = "\n- **⛔ 如果 User Prompt 中出现「T字保护订单」列表，禁止撤销其中任何订单**"
	}

	return fmt.Sprintf(`# 网格交易 AI — %s

## 角色定位
你是双向网格交易的决策引擎。每个周期根据市场状态决定：
1. 补充哪些空缺网格层（下买单/卖单）
2. 撤销哪些不合理的挂单（单个撤销或全部撤销）
3. 或者观望不动（hold）

系统会自动处理：浮盈减仓、T字保护单的标记与减仓、以及网格越界后的自动重建。自动重建的具体规则见下方「重置网格」。这些无需你输出决策。

## 市场状态判断
| 状态 | 条件 | 操作 |
|------|------|------|
| 震荡 | 布林带宽 < 3%%, EMA距离 < 1%% | 正常补单，多空均衡 |
| 高波动 | ATR 异常放大 | hold，等待波动平息 |

## 可用操作
| 操作 | 用途 | 何时使用 |
|------|------|---------|
| place_buy_limit | 在指定价格挂买单（开多仓） | 网格层为空且当前价格高于该层 |
| place_sell_limit | 在指定价格挂卖单（开空仓） | 网格层为空且当前价格低于该层 |
| cancel_order | 撤销指定订单 | 挂单价格严重偏离市场、长期未成交、或市场状态转变需调整布局 |
| cancel_all_orders | 撤销所有挂单 | 市场剧烈波动需全面重新布局 |
| adjust_grid | 以当前价格为中心重置网格边界和层级 | 当前网格已明显失真、价格持续偏离中心，或需要按最新波动率重新锚定 |
| hold | 本周期不操作 | 市场状态不明朗、余额不足、或现有布局已合理无需调整 |

## 重置网格
- **AI 主动重置**：需要重置时输出 {"action":"adjust_grid","symbol":"...","reasoning":"..."}。price、quantity、level_index、order_id 可填 0 或空字符串，系统会读取最新价格并计算参数。
- **手动重置步骤**：系统先撤销普通网格挂单；读取最新市场价格作为中心；启用 ATR 边界时用 4h ATR14 × ATRMultiplier 作为上下半幅（倍数未设置时使用 2.0），否则使用默认上下 ±3%% ×（网格层数/10）；随后按新的边界、间距和分布重建全部网格层。
- **持仓与保护单**：已有成交持仓会迁移到新网格中距离最近的层级并保留入场价、数量和盈亏状态；T字减仓单和浮盈减仓单属于保护单，不会被撤销。T字标记单是普通网格挂单，重置时可以被撤销。
- **系统自动重置**：每个网格周期、调用 AI 前检查标记价格；当 markPrice > upperPrice × 1.02 或 markPrice < lowerPrice × 0.98 时，系统自动以标记价格为中心重建，优先使用当前行情上下文的 ATR14 × ATRMultiplier，ATR 不可用时使用上述默认范围。此时无需输出 adjust_grid。

## 操作约束与规则

### 下单规则
- quantity 必须使用层级表中的「建议数量」
- state = "pending" 的层级已有挂单，禁止重复下单
- **补单优先级**：优先补靠近当前价格的层级，再铺离价格较远的边缘层级
- **每周期最多 8 个下单决策**（place_buy_limit / place_sell_limit 合计），超出部分会被忽略

### 撤单规则
- cancel_order：填写 order_id（网格层级表中的订单ID），quantity 填 0
- **每周期最多撤 3 个订单**（cancel_order 合计，不含 cancel_all_orders）
- **T字保护订单**：如果 User Prompt 列出「T字保护订单」，这些订单是系统为反向减仓预留的，绝对禁止撤销%s

**reasoning 字段保持简洁，不超过 2 句话。**

## 输出格式
JSON 数组，每个决策一个对象：
[{"symbol":"...","action":"...","price":0.0,"quantity":0.0,"level_index":0,"order_id":"","confidence":0,"reasoning":"..."}]
`, config.Symbol, ttradeNote)
}

func buildGridSystemPromptEn(config *store.GridStrategyConfig) string {
	ttradeNote := ""
	if config.EnableTrappedReduce {
		ttradeNote = "\n  - **⛔ If a \"T-Trade Protected Orders\" list appears in the User Prompt, never cancel any of those order IDs**"
	}

	return fmt.Sprintf(`# Grid Trading AI — %s

## Role
You are the decision engine for a bidirectional grid strategy. Each cycle, based on market conditions, you decide:
1. Which empty grid levels to fill (place buy/sell orders)
2. Which unreasonable pending orders to cancel (individual or all)
3. Or hold and do nothing

The system automatically handles profit-taking reductions, T-trade order tagging and reduction, and automatic grid rebuild. See "Grid Reset" below for the exact rebuild rules; you do not need to output decisions for automatic rebuilds.

## Market State
| State | Conditions | Action |
|-------|-----------|--------|
| Ranging | BB width < 3%%, EMA distance < 1%% | Normal grid, balanced long/short |
| High volatility | ATR abnormally large | hold, wait for volatility to settle |

## Available Actions
| Action | Purpose | When to Use |
|--------|---------|-------------|
| place_buy_limit | Place buy order at specified price (open long) | Grid level is empty and current price is above that level |
| place_sell_limit | Place sell order at specified price (open short) | Grid level is empty and current price is below that level |
| cancel_order | Cancel a specific order | Order price severely off-market, long unfilled, or market regime change requires layout adjustment |
| cancel_all_orders | Cancel all pending orders | Market volatility requires full re-layout |
| adjust_grid | Recenter the grid and rebuild its bounds and levels | The current grid is materially stale, price keeps drifting from its center, or volatility requires a new anchor |
| hold | No action this cycle | Market unclear, insufficient balance, or current layout already reasonable |

## Grid Reset
- **AI-requested reset**: output {"action":"adjust_grid","symbol":"...","reasoning":"..."} when a reset is warranted. price, quantity, level_index, and order_id may be 0 or empty; the system reads the latest price and calculates the parameters.
- **Manual reset procedure**: cancel ordinary grid orders, read the latest market price as the center, then use 4h ATR14 × ATRMultiplier as the half-range when ATR bounds are enabled (default multiplier 2.0). Otherwise use the default ±3%% × (grid count/10) range, and rebuild all levels with the new bounds, spacing, and distribution.
- **Positions and protected orders**: filled positions migrate to the nearest new level with entry price, size, and PnL state preserved. T-trade reduce orders and profit-reduce orders are protected and are not cancelled; T-trade prep orders are ordinary grid orders and may be cancelled during a reset.
- **Automatic reset**: before each AI cycle, the system checks mark price. If markPrice > upperPrice × 1.02 or markPrice < lowerPrice × 0.98, it automatically rebuilds around mark price, preferring the current-context ATR14 × ATRMultiplier and falling back to the default range when ATR is unavailable. Do not output adjust_grid for this automatic reset.

## Constraints and Rules

### Order Placement Rules
- quantity must use "Suggested Qty" from the level table
- levels with state = "pending" already have an order — do NOT place another
- **Order priority**: fill levels closest to the current price first, then spread outward to edge levels
- **Maximum 8 order decisions per cycle** (place_buy_limit + place_sell_limit combined) — excess will be ignored

### Cancellation Rules
- cancel_order: set order_id to the Order ID from the grid level table, quantity to 0
- **Maximum 3 cancel_order decisions per cycle** (excluding cancel_all_orders)
- **T-Trade Protected Orders**: if User Prompt lists "T-Trade Protected Orders", these are reserved by the system for contra-side reductions — never cancel them%s

**Keep reasoning concise — 2 sentences max.**

## Output Format
JSON array, one object per decision:
[{"symbol":"...","action":"...","price":0.0,"quantity":0.0,"level_index":0,"order_id":"","confidence":0,"reasoning":"..."}]
`, config.Symbol, ttradeNote)
}

// BuildGridUserPrompt builds the user prompt for grid trading AI
func BuildGridUserPrompt(ctx *GridContext, lang string) string {
	if lang == "zh" {
		return buildGridUserPromptZh(ctx)
	}
	return buildGridUserPromptEn(ctx)
}

// SuggestedQuantity computes the quantity a level "should" trade at, scaling
// its AllocatedUSD share by current total equity (not just the original
// TotalInvestment, so it reflects actual account size) and leverage. Shared
// by the AI prompt builders (as a suggestion the AI can deviate from) and
// the algorithmic decision maker (as the actual quantity to place).
func SuggestedQuantity(level GridLevelInfo, ctx *GridContext) float64 {
	allocUSD := level.AllocatedUSD
	if ctx.TotalInvestment > 0 && ctx.TotalEquity > 0 {
		allocUSD = level.AllocatedUSD / ctx.TotalInvestment * ctx.TotalEquity
	}
	if level.Price <= 0 || allocUSD <= 0 {
		return 0
	}
	raw := allocUSD * float64(ctx.Leverage) / level.Price
	return math.Round(raw*10000) / 10000
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
		sb.WriteString("\n")
	}

	// Protected T-trade order IDs
	if len(ctx.TTradeProtectedOrderIDs) > 0 {
		sb.WriteString("## ⛔ T字保护订单（禁止撤销）\n")
		for _, id := range ctx.TTradeProtectedOrderIDs {
			sb.WriteString(fmt.Sprintf("- %s\n", id))
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
	sb.WriteString("| 层级 | 价格 | 状态 | 方向 | 订单ID | 权益USD | 建议数量 | 订单数量 | 持仓 |\n")
	sb.WriteString("|------|------|------|------|--------|---------|----------|----------|------|\n")
	for _, level := range ctx.Levels {
		// Scale allocated weight by current total equity so suggested qty reflects actual account size
		allocUSD := level.AllocatedUSD
		if ctx.TotalInvestment > 0 && ctx.TotalEquity > 0 {
			allocUSD = level.AllocatedUSD / ctx.TotalInvestment * ctx.TotalEquity
		}
		suggestedQty := SuggestedQuantity(level, ctx)
		var equityUSD float64
		switch level.State {
		case "filled":
			if level.PositionEntry > 0 {
				equityUSD = level.PositionSize * level.PositionEntry / float64(ctx.Leverage)
			} else {
				equityUSD = level.PositionSize * level.Price / float64(ctx.Leverage)
			}
		case "pending":
			equityUSD = level.OrderQuantity * level.Price / float64(ctx.Leverage)
		default:
			equityUSD = allocUSD
		}
		orderID := level.OrderID
		if orderID == "" {
			orderID = "-"
		}
		sb.WriteString(fmt.Sprintf("| %d | $%.2f | %s | %s | %s | $%.2f | %.4f | %.4f | %.4f |\n",
			level.Index, level.Price, level.State, level.Side, orderID, equityUSD, suggestedQty,
			level.OrderQuantity, level.PositionSize))
	}
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
		sb.WriteString("\n")
	}

	// Protected T-trade order IDs
	if len(ctx.TTradeProtectedOrderIDs) > 0 {
		sb.WriteString("## ⛔ T-Trade Protected Orders (do NOT cancel)\n")
		for _, id := range ctx.TTradeProtectedOrderIDs {
			sb.WriteString(fmt.Sprintf("- %s\n", id))
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
	sb.WriteString("| Level | Price | State | Side | Order ID | Equity USD | Suggested Qty | Order Qty | Position |\n")
	sb.WriteString("|-------|-------|-------|------|----------|------------|---------------|-----------|----------|\n")
	for _, level := range ctx.Levels {
		allocUSD := level.AllocatedUSD
		if ctx.TotalInvestment > 0 && ctx.TotalEquity > 0 {
			allocUSD = level.AllocatedUSD / ctx.TotalInvestment * ctx.TotalEquity
		}
		suggestedQty := SuggestedQuantity(level, ctx)
		var equityUSD float64
		switch level.State {
		case "filled":
			if level.PositionEntry > 0 {
				equityUSD = level.PositionSize * level.PositionEntry / float64(ctx.Leverage)
			} else {
				equityUSD = level.PositionSize * level.Price / float64(ctx.Leverage)
			}
		case "pending":
			equityUSD = level.OrderQuantity * level.Price / float64(ctx.Leverage)
		default:
			equityUSD = allocUSD
		}
		orderID := level.OrderID
		if orderID == "" {
			orderID = "-"
		}
		sb.WriteString(fmt.Sprintf("| %d | $%.2f | %s | %s | %s | $%.2f | %.4f | %.4f | %.4f |\n",
			level.Index, level.Price, level.State, level.Side, orderID, equityUSD, suggestedQty,
			level.OrderQuantity, level.PositionSize))
	}
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

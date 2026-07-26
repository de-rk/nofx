# 项目文件索引

> 新增或修改文件后，同步更新本文件对应条目。

---

## 入口

| 文件 | 说明 |
|------|------|
| `main.go` | 程序入口，初始化 DB/API/Manager，启动 HTTP 服务 |
| `Makefile` | 构建、部署快捷命令 |
| `docker-compose.yml` | 本地开发容器编排 |
| `docker-compose.prod.yml` | 生产环境容器编排 |

---

## api/ — HTTP 接口层

| 文件 | 说明 |
|------|------|
| `server.go` | 路由注册、中间件、服务器启动 |
| `strategy.go` | 策略配置的 CRUD 接口 |
| `crypto_handler.go` | 加密相关接口（密钥导入/导出） |
| `errors.go` | 统一错误响应格式 |
| `utils.go` | 接口层通用工具函数 |

---

## kernel/ — AI 决策引擎

| 文件 | 说明 |
|------|------|
| `engine.go` | 通用 AI 决策引擎（调用 LLM，解析 JSON 输出） |
| `grid_engine.go` | 网格专用引擎：构建 system/user prompt，解析网格决策；含 `BuildGridSystemPrompt`、`BuildGridUserPrompt` |
| `prompt_builder.go` | 非网格策略的 prompt 构建 |
| `formatter.go` | 决策输出格式化 |
| `schema.go` | AI 输出 JSON schema 定义 |

---

## trader/ — 交易执行层

| 文件 | 说明 |
|------|------|
| `auto_trader.go` | 通用自动交易主循环（非网格）：AI 周期、止盈减仓、持仓管理 |
| `auto_trader_grid.go` | 网格交易核心：网格状态机、AI 周期、T-trade（T字操作）、减仓、syncExchangeState、resetGrid、checkProfitReduce（浮盈减仓，排除 T-trade 减仓单与网格层挂单的重复下单检查） |
| `grid_regime.go` | 网格市场状态检测（震荡/趋势/突破） |
| `interface.go` | `GridTrader` 接口定义 |
| `helpers.go` | 通用工具函数（数量计算、价格格式化等） |
| `tp_manager.go` | 止盈管理器：浮盈减仓触发与执行 |
| `tp_helper.go` | 止盈计算辅助函数 |
| `ttrade_enhanced.go` | T-trade 增强：`TTradeState` 结构、阈值上下文构建 |
| `position_rebuild.go` | 重启后持仓状态恢复 |
| `position_snapshot.go` | 持仓快照，用于盈亏计算 |

### trader/okx/

| 文件 | 说明 |
|------|------|
| `trader.go` | OKX 交易所适配器（下单、查询、持仓） |
| `ws.go` | OKX WebSocket 推送（持仓、订单事件） |
| `order_sync.go` | OKX 订单状态同步 |

### trader/binance/

| 文件 | 说明 |
|------|------|
| `futures.go` | Binance 合约适配器 |
| `order_sync.go` | Binance 订单状态同步 |

### trader/hyperliquid/

| 文件 | 说明 |
|------|------|
| `trader.go` | Hyperliquid 适配器 |
| `order_sync.go` | Hyperliquid 订单状态同步 |

---

## mcp/ — LLM 客户端

| 文件 | 说明 |
|------|------|
| `interface.go` | LLM 客户端接口定义 |
| `client.go` | 客户端工厂，按配置选择 provider |
| `request.go` | 请求结构体 |
| `request_builder.go` | 构建 LLM 请求（含 system prompt 注入） |
| `options.go` | 调用选项（temperature、max_tokens 等） |
| `claude_client.go` | Anthropic Claude 客户端 |
| `openai_client.go` | OpenAI 兼容客户端 |
| `deepseek_client.go` | DeepSeek 客户端 |
| `gemini_client.go` | Google Gemini 客户端 |
| `grok_client.go` | xAI Grok 客户端 |
| `kimi_client.go` | Kimi 客户端 |
| `qwen_client.go` | 通义千问客户端 |
| `logger.go` | LLM 请求/响应日志 |
| `config.go` | LLM 配置解析 |

---

## store/ — 数据库层（GORM）

| 文件 | 说明 |
|------|------|
| `store.go` | Store 接口与工厂 |
| `gorm.go` | GORM 连接初始化与迁移 |
| `driver.go` | 数据库驱动选择（SQLite/PostgreSQL） |
| `strategy.go` | 策略配置表（`StrategyConfig`） |
| `grid.go` | 网格配置表（`GridStrategyConfig`），含 T-trade、投资额刷新等字段 |
| `decision.go` | AI 决策记录表 |
| `trader.go` | Trader 实例配置表 |
| `order.go` | 订单记录表 |
| `position.go` | 持仓记录表 |
| `position_builder.go` | 持仓记录构建工具 |
| `equity.go` | 权益快照表 |
| `exchange.go` | 交易所配置表 |
| `ai_model.go` | AI 模型配置表 |
| `user.go` | 用户表 |

---

## market/ — 行情数据

| 文件 | 说明 |
|------|------|
| `data.go` | 行情数据结构与聚合（K线、指标） |
| `api_client.go` | 行情 REST 拉取 |
| `historical.go` | 历史 K 线查询 |
| `timeframe.go` | 时间周期定义与转换 |
| `types.go` | 行情相关类型 |

---

## manager/ — Trader 生命周期管理

| 文件 | 说明 |
|------|------|
| `trader_manager.go` | 创建/启动/停止 Trader 实例，管理多账号并发 |

---

## auth/ / security/ / crypto/

| 文件 | 说明 |
|------|------|
| `auth/auth.go` | JWT 鉴权、登录逻辑 |
| `security/url_validator.go` | 请求 URL 白名单校验 |
| `crypto/crypto.go` | AES 加密/解密（API Key 存储） |

---

## hook/ — 生命周期钩子

| 文件 | 说明 |
|------|------|
| `hooks.go` | 钩子注册与触发 |
| `http_client_hook.go` | HTTP 请求拦截钩子 |
| `ip_hook.go` | IP 过滤钩子 |
| `trader_hook.go` | Trader 事件钩子 |

---

## 其他后端

| 文件 | 说明 |
|------|------|
| `config/config.go` | 全局配置加载（环境变量/文件） |
| `logger/logger.go` | 日志初始化（zerolog） |
| `logger/config.go` | 日志级别/输出配置 |
| `experience/experience.go` | AI 经验积累（历史决策反馈） |
| `llm/qwen_agent.go` | 千问 Agent 独立封装 |

---

## web/src/ — 前端（React + TypeScript）

### 页面

| 文件 | 说明 |
|------|------|
| `pages/TraderDashboardPage.tsx` | Trader 主看板（持仓、决策历史、图表） |
| `pages/StrategyStudioPage.tsx` | 策略配置编辑页 |
| `pages/StrategyMarketPage.tsx` | 策略市场页 |
| `pages/LandingPage.tsx` | 落地页 |
| `pages/PromptTestPage.tsx` | Prompt 调试页 |

### 核心组件

| 文件 | 说明 |
|------|------|
| `components/DecisionCard.tsx` | 单条 AI 决策展示卡片 |
| `components/TraderConfigModal.tsx` | Trader 创建/编辑弹窗 |
| `components/EquityChart.tsx` | 权益曲线图表 |
| `components/PositionHistory.tsx` | 历史持仓列表 |
| `components/Header.tsx` / `HeaderBar.tsx` | 顶部导航栏（策略市场、Traders、Dashboard、Strategy、Prompt测试） |
| `components/LoginPage.tsx` / `RegisterPage.tsx` | 登录/注册页 |
| `components/AdvancedChart.tsx` | K线图表组件（lightweight-charts）：OKX WS 实时推送 + 失败降级轮询、订单标记、挂单价格线、OHLC tooltip |

### 策略组件 (`components/strategy/`)

| 文件 | 说明 |
|------|------|
| `GridConfigEditor.tsx` | 网格策略配置编辑器（层数、投资额、T-trade 阈值等） |

---

## 关键数据流

```
HTTP 请求
  → api/server.go (路由)
  → manager/trader_manager.go (Trader 实例)
  → trader/auto_trader_grid.go (网格主循环 RunGridCycle)
      → kernel/grid_engine.go (构建 prompt)
      → mcp/client.go (调用 LLM)
      → executeGridDecision (执行: place/cancel/hold)
      → syncExchangeState (同步交易所状态)
      → RunTTradeScan (T-trade 标记/减仓)
      → store/* (持久化)
```

---

## 历史修复记录

### 2026-06-24

| Bug | 根因 | 修复位置 |
|-----|------|----------|
| **利润减仓重复下单** — `checkProfitReduce()` 每周期都执行，没有检查是否已有未成交减仓单，导致短时间内多次下单 | 缺少下单前的挂单检查 | `trader/auto_trader_grid.go` `checkProfitReduce()` — 下单前调用 `GetOpenOrders()`，用方向+价格差<1% 识别已存在的减仓单 |
| **WebSocket K线事件不触发 AI 周期** — goroutine 只监听 WS channel，WS 失败时永远阻塞，AI 只执行一次 | 事件监听缺少降级定时器 | `trader/auto_trader.go` — 在监听 goroutine 内加 `fallbackTicker`，WS 静默超时后自动降级触发 |
| **`OpenOrder.ReduceOnly` 编译错误** — `types.OpenOrder` 不含该字段，上述修复引入的编译错误 | 使用了不存在的结构体字段 | 同上，改为启发式判断（方向匹配 + 价格差 < 1%） |

### 2026-07-07

| Bug | 根因 | 修复位置 |
|-----|------|----------|
| **浮盈减仓被网格卖单误判阻塞** — `checkProfitReduce()` 的"已有挂单"检查只排除了 T-trade 减仓单，未排除网格层 SELL 单；AI 在网格层挂的 SELL 单价格恰好在 mark 价 1% 以内时，被误判为已有浮盈减仓单，导致永久跳过 | 缺少网格层订单 ID 的排除逻辑 | `trader/auto_trader_grid.go` `checkProfitReduce()` — 在 `gridState.mu.RLock()` 内同时收集 `gridState.Levels` 的 `OrderID`，与 T-trade 减仓单一并排除 |

### 2026-07-15

| Bug | 根因 | 修复位置 |
|-----|------|----------|
| **API 中断后 investment refresh 误触发网格重置** — 主账号 API Key IP 白名单限制导致长时间 `GetBalance()` 失败，失联期间刷新计时器未更新，API 恢复后立即判定超过刷新间隔，触发 `resetGrid()` 撤销全部挂单 | `GetBalance()` 失败时未重置 `LastInvestmentRefreshAt`，导致失联时长计入刷新间隔 | `trader/auto_trader_grid.go` `checkInvestmentRefresh()` — `GetBalance()` 失败时将 `LastInvestmentRefreshAt` 更新为当前时间，失联期间不计入间隔 |
| **`emergencyExit()` 未使用但存在副作用风险** — 函数从未被调用，但内部直接调用 `CancelAllOrders` 会绕过 T-trade 保护，且不清理内存状态 | 死代码 | `trader/auto_trader_grid.go` — 删除 `emergencyExit()` 函数 |

### 2026-07-18

| Bug | 根因 | 修复位置 |
|-----|------|----------|
| **重启后 `cancelAllGridOrders` 误撤 T-trade 订单** — `cancelAllGridOrders` 的 `protectedIDs` 完全依赖内存 `TTradePrepOrders`/`TTradeReduceOrders`；重启后内存为空，investment refresh / breakout 触发 `resetGrid()` → `cancelAllGridOrders()` 时，所有 T-trade prep/reduce 订单被当作普通网格订单撤销 | 缺少 DB 兜底查询，重启后保护列表为空 | `trader/auto_trader_grid.go` `cancelAllGridOrders()` — 内存为空时从 DB 查询活跃 T-trade 订单（`ttrade_tag` 3h 内无 fill/cancel + `ttrade_reduce_placed` 24h 内无 reduce），合并到 `protectedIDs` |
| **AI 同一周期对同一价格重复下单** — `placeGridLimitOrder` 无价格去重检查，AI 幻觉或逻辑错误时可能在同一周期对同一价格下多个订单，导致仓位超标或资金占用异常 | 缺少下单前价格去重 | `trader/auto_trader_grid.go` `placeGridLimitOrder()` — 遍历所有 pending levels，若价格在 0.1% 容差内则跳过下单，记录 Warn 日志 |
| **T-trade 生命周期无前端可视化** — 之前回退了 `TTradeDrawer` 组件，用户无法在 UI 看到标记→成交→减仓挂单→减仓成交→补单的完整生命周期 | — | `web/src/components/TTradePanel.tsx`（新建）— 按 `order_id` 分组 ttrade 日志，展开显示每个 prep 的完整事件链；`TraderDashboardPage` 新增 `🎯 T-trade` tab |

### 2026-07-20

| 变更 | 说明 | 修改位置 |
|-----|------|----------|
| **日志/交易日志保留期改为 7 天** — `data/` 下的 `.log` 文件与 `grid_trade_logs` 表数据统一保留 7 天 | 原为日志文件 14 天、`grid_trade_logs` 30 天 | `logger/logger.go`（`Init` 内文件清理 cutoff）、`store/grid.go` `LogGridTrade()`（每次写入后清理 cutoff） |
| **重启后 AI 可撤销 T-trade 减仓单（保护失效）** — 07-18 只修复了 `cancelAllGridOrders`（网格重置路径）的重启后保护；但 AI 每周期决策路径（prompt 里的「T字保护订单」列表 + `cancel_order` 执行时的二次校验）仍然只读内存 `TTradeReduceOrders`，重启后内存为空，AI 未被告知需保护、且直接执行也不会被拦截，导致减仓单被误撤 | 同一类问题的另一个未修复入口，`TTradePrepOrders`/`TTradeReduceOrders` 从不落盘 | `trader/auto_trader_grid.go` — 拆分出 `activeTTradePrepOrderIDs()` / `activeTTradeReduceOrderIDs()`（内存 + DB 兜底，各自独立回退）与 `activeTTradeProtectedIDs()`（二者合并，供 `cancelAllGridOrders` 使用）；构建 AI prompt 的 `ctx.TTradeProtectedOrderIDs` 与 `cancel_order` 执行时的保护校验均改为调用 `activeTTradeReduceOrderIDs()` |
| **减仓单 ID 靠 `fmt.Sscanf` 解析 Reason 文本，格式漂移会静默失效** — 上一条修复引入的 DB 兜底依赖 `Sscanf(e.Reason, "reduce order %s placed for prep %s", ...)` 从自由文本里抠 reduce 订单 ID，一旦 Reason 文案改动或含意外空白/标点，`Sscanf` 会静默返回空字符串，整个兜底列表悄悄变空且无报错 | 用文本解析代替结构化字段存 ID，脆弱 | `store/grid.go` `GridTradeLogModel` 新增 `RelatedOrderID` 列；`trader/auto_trader_grid.go` `logGridTrade()` 增加可选 variadic 参数写入该列，`placeTTradeReduceOrder()` 落盘 `ttrade_reduce_placed` 时传入真实 reduce 订单 ID；`InitializeGrid()` 重启恢复与 `activeTTradeReduceOrderIDs()` 两处读取改为只读 `RelatedOrderID`，完全移除 `Sscanf` 解析 Reason 的兜底分支（历史行若无该列则视为无法恢复，不再尝试解析文本） |
| **浮盈减仓每次 WS 持仓推送都打 Info 日志，刷爆日志文件** — `checkProfitReduce()` 由 OKX WS 持仓推送触发（约 1-3 秒一次），其中三条无条件/近乎每次触发的日志（逐次检查详情、逐次目标步进计算、"已减仓跳过"）全部是 `Info` 级别，导致每分钟产生数十条重复日志，与实际下单动作无关 | 诊断用日志误用 `Info` 而非 `Debug`，未区分"例行检查"与"实际执行动作" | `trader/auto_trader_grid.go` `checkProfitReduce()` — 将 4 处逐次检查/无操作跳过的日志降级为 `Debugf`（默认 `info` 级别不输出）；实际下单/状态变更（`profit_reduce`/`profit_reduce_close` 下单、`LongProfitReducedPct`/`ShortProfitReducedPct` 状态更新）保持 `Infof` 不变 |
| **Fallback 定时器"WS 静默 495844h"日志荒谬** — `wsLastKlineClose` 从未收到过 WS 事件时是 `int64` 零值，`time.Unix(0, 0)` 转出来的是 1970-01-01（Unix 纪元），不是 Go 的 `time.Time{}` 零值，导致 `lastKline.IsZero()` 永远为 false，日志误报"WS silent for 495844h..."这类无意义时长 | `time.Unix(0, 0)` ≠ Go 零值 `time.Time{}`，转换前后语义不同 | `trader/auto_trader.go` — fallback ticker 分支改为直接判断 `atomic.LoadInt64(&at.wsLastKlineClose) == 0`（`neverReceived`），只在确实收到过事件时才计算 `time.Since(...)`，不再依赖转换后的 `IsZero()` |

### 已知设计限制（待优化）

| 问题 | 说明 |
|------|------|
| **多持仓冲突** | `sides` map 以 `"long"`/`"short"` 为 key，同方向多持仓时后者覆盖前者，`checkProfitReduce()` 只处理最后一条 |
| **减仓进度状态共享** | `LongProfitReducedPct` / `ShortProfitReducedPct` 全局只有一个值，同方向多持仓无法独立跟踪各自的减仓阶梯 |
| **手动重置后重复触发** | `ResetProfitTracker()` 将 `alreadyReduced` 清零，重置后该方向所有阶梯重新可触发 |

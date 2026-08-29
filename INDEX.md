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
| `docker-compose.stable.yml` | 稳定版环境容器编排 |

---

## api/ — HTTP 接口层

| 文件 | 说明 |
|------|------|
| `server.go` | 路由注册、中间件、服务器启动 |
| `strategy.go` | 策略配置的 CRUD 接口 |
| `backtest.go` | 回测接口（网格策略回测、参数搜索） |
| `crypto_handler.go` | 加密相关接口（密钥导入/导出） |
| `errors.go` | 统一错误响应格式 |
| `utils.go` | 接口层通用工具函数 |

---

## kernel/ — AI 决策引擎

| 文件 | 说明 |
|------|------|
| `engine.go` | 通用 AI 决策引擎（调用 LLM，解析 JSON 输出）；system prompt 明确 AI 可在 OKX 开仓时成对设置原生移动止盈止损的激活/回撤百分比（含多空方向、取值范围及与固定止盈止损共存规则），并要求 `confidence` 使用 JSON 数字；解析器兼容置信度偶发输出英文数字词；AI user prompt 不再注入历史交易统计；风险收益比校验含显示精度容差；`FullDecision.ParseFailed` 标记"AI 调用成功但解析失败、被兜底成 hold"这种情况，供调用方区分"AI 真的决定 hold"与"AI 响应不可用" |
| `engine_prompt_test.go` | `StrategyEngine.BuildSystemPrompt` 的 OKX 原生移动止盈止损 prompt 回归测试，以及英文置信度数字归一化解析测试 |
| `grid_engine.go` | 网格专用引擎：构建 system/user prompt（中英双语），解析网格决策；prompt 支持 AI 主动 `adjust_grid`、`reduce_long` 和 `reduce_short`，并说明与价格越界自动重建并行的 ATR/默认边界、主动部分减仓和保护减仓单规则；含 `BuildGridSystemPrompt`、`BuildGridUserPrompt`、`SuggestedQuantity`（层级建议下单量公式，AI prompt 表格与算法决策模式共用同一份实现）；`parseGridDecisions` 解析出的每条决策先经 `normalizeGridAction`（大小写归一化 + `gridActionSynonyms` 同义词表）再校验，兼容不严格遵循 prompt 动作词表的 AI 模型 |
| `grid_engine_prompt_test.go` | 网格 system prompt 中普通多空部分减仓动作与规则的回归测试 |
| `prompt_builder.go` | 根据市场状态动态调整 prompt 的逻辑（识别趋势/震荡、计算技术指标、生成实时风险提示） |
| `formatter.go` | 决策输出格式化；持仓提示区分交易所标记价与 ticker/K 线价格 |
| `schema.go` | AI 输出 JSON schema 定义 |

---

## trader/ — 交易执行层

| 文件 | 说明 |
|------|------|
| `auto_trader.go` | 通用自动交易主循环（非网格）：AI 周期、止盈减仓、持仓管理；开仓后按 AI 参数调用交易所原生移动止盈止损，移动单成功时只保留单一移动保护单，失败时回退单一合并固定止盈/止损委托；平空与利润回撤全平均按标准化后的持仓方向执行，避免依赖仓位数量正负；支持接替时按方向撤销对侧网格普通挂单并保留减仓单；启动 WS 连接、设置回调（position push、order update、kline close），wired 到 Grid Cycle 和浮盈减仓触发 |
| `auto_trader_grid.go` | 网格交易核心：网格状态机、AI 周期、AI 主动 `adjust_grid` 与价格越界自动重建并行、AI 的 `reduce_long`/`reduce_short` 普通市价部分减仓、定期刷新投资额后复用 `adjust_grid`（保留 T 字/浮盈减仓保护单并迁移持仓）、T-trade（T字操作）、减仓、syncExchangeState、checkProfitReduce（浮盈减仓，排除 T-trade 减仓单与网格层挂单的重复下单检查）；支持接替时暂停网格周期、按方向撤销对侧普通入口单并保护减仓单；`buildAlgoGridDecision` 为非 AI 的确定性决策生成器（补空层+超时撤单），由 `RunGridCycle` 按 `GridConfig.DecisionMode`（"ai"/"ai_with_algo_fallback"/"algo_only"）选择性调用，产出与 AI 完全一致的 `kernel.Decision`，走同一条 `executeGridDecision` 执行链路，交易日志 source 按实际来源标为 `"ai"` 或 `"algo"` |
| `position_rebuild.go` | 持仓重建逻辑：从交易所读取持仓，匹配到网格层级，重建本地状态 |
| `position_snapshot.go` | 持仓快照定时存储（用于绩效分析） |
| `interface.go` | `GridTrader` 与可选 `TrailingStopTrader` 接口定义 |
| `helpers.go` | 通用工具函数（数量计算、价格格式化等） |
| `tp_manager.go` | 止盈管理器：`TPManager` 后台监控循环（每个 trader 实例共用，网格/非网格均会启动），但分批止盈的外部写入入口（`SetTPLevels`）已删除——目前无任何调用方会真正喂数据进去，循环本身是活的、`activeLevels` 永远为空 |
| `position_rebuild.go` | 重启后持仓状态恢复 |
| `position_snapshot.go` | 持仓快照，用于盈亏计算 |

### trader/okx/

| 文件 | 说明 |
|------|------|
| `trader.go` | OKX 交易所适配器（下单、查询、持仓）；AI 下单前强制确认/切换账户双向持仓模式，所有开平仓、限价和保护单显式发送 `posSide=long/short`；固定止盈止损使用单一 OCO algo 订单并兼容 OCO 查询/撤销；支持原生 `move_order_stop` 移动止盈止损 |
| `ws.go` | OKX WebSocket 推送（公共 ticker、business K线、私有持仓/订单事件；`net` 持仓按正负数量识别多空；含独立重连与心跳） |
| `trader_test.go` | OKX 持仓方向解析与双向模式 `posSide` 回归测试 |
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
| `openai_client.go` | OpenAI Responses API 客户端（含 Codex 模型请求/响应格式） |
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
| `strategy.go` | 策略配置表（`StrategyConfig`）；风险控制含 AI 原生移动止盈止损开关 `enable_trailing_stop` |
| `grid.go` | 网格配置表（`GridStrategyConfig`），含 T-trade、投资额刷新等字段 |
| `decision.go` | AI 决策记录表，保存移动止盈止损参数 |
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
| `data.go` | 行情数据结构与聚合（K线、指标）；CoinAnk 交易所数据和其 Binance 回退均不可用时，继续使用 Binance Futures REST 并校验 K 线质量 |
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
| `auth/auth.go` | JWT 鉴权、登录逻辑；用户登录会话有效期为 7 天 |
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
| `market/rolling_change.go` | 带时间戳的滚动价格窗口；按当前价格相对不晚于窗口起点的价格计算涨跌幅，供异常波动接管监控使用 |
| `market/trend_gate.go` | 单币 AI 开仓门控：使用 K 线价格变化和成交量比过滤开多/开空，平仓动作不受限制 |
| `market/kline_quality.go` | 统一 K 线质量清洗：排序去重、过滤未收盘 bar、校验 OHLCV，并拒绝最新已收盘零成交量数据 |
| `web/src/components/strategy/CoinSourceEditor.tsx` | 所有候选源模式都可查看和编辑自定义币种；static 直接使用，AI500 失败时回退，mixed 作为静态来源 |
| `web/src/components/strategy/TrendGateEditor.tsx` | Strategy Studio 的单币 K 线+成交量趋势门控配置编辑器 |
| `manager/handoff_manager.go` | 网格异常波动接替编排：每秒采样源网格标的价格，三分钟绝对涨跌幅达到阈值后暂停网格、按涨跌方向撤销对侧普通挂单（保留减仓单）并启动目标 AI 交易员，不平仓或停止源交易员 |
| `store/handoff.go` | 网格到 AI 的显式接替绑定与执行状态持久化，含原子触发抢占与阶段错误记录 |
| `api/handoff.go` | 接替绑定 CRUD API，校验源为网格、目标为 AI 且使用相同交易所账户 |
| `experience/experience.go` | AI 经验积累（历史决策反馈） |
| `llm/qwen_agent.go` | 千问 Agent 独立封装 |

## backtest/ — 网格策略离线回测 + 模拟退火参数搜索（不接触实盘交易路径）

| 文件 | 说明 |
|------|------|
| `kernel/engine.go` | AI 策略引擎：候选币源（AI500 失败/空结果回退静态币）、按账户交易所获取多周期行情上下文、LLM 决策解析与风险校验；system prompt 根据 `enable_trailing_stop` 开关告知 AI 是否允许原生移动止盈止损，明确 OKX 双向持仓与 `posSide=long/short` 规则，并要求盈亏比在最小阈值外预留执行缓冲 |
| `trader/auto_trader.go` | 普通 AI 交易周期、持仓管理、开仓执行层硬风控（趋势门控按账户交易所获取 K 线、最低置信度、最大保证金占用）和策略热更新；AI 设置 OKX 移动止盈时只创建单一移动保护单，失败才回退单一 OCO 固定止盈/止损，OCO 失败再拆分为两条保护单，且受策略开关控制 |
| `store/strategy.go` | AI/网格策略 JSON 配置，含 `TrendGateConfig` 单币 K 线+成交量开仓门控 |
| `market/data.go` | 多周期 OHLCV 与指标数据构建；支持按账户交易所获取 K 线，保留完整盘中序列供可配置趋势门控使用；严格交易所模式下 OKX 趋势门控的 candles、成交量、OI、资金费率和 ticker 均只使用 OKX 链路，不回退 CoinAnk/Binance |
| `backtest/types.go` | `GridParams`（搜索空间：`grid_count`/`atr_multiplier`/`distribution`/`leverage`/`profit_reduce_step_pct`/`profit_reduce_multiplier`，均带 JSON tag 供 API 序列化；另有固定不参与退火搜索、仅按传入值忠实模拟的风控开关：`EnableTTrade`+`TTradePositionThresholdPct`+`TTradeSpreadPct`、`ProfitDrawdownThresholdPct`、`EnableSmallPositionClose`、`FeePct`（每笔成交名义价值的固定手续费率，0=禁用）、`InvestmentRefreshDays`（每隔N天从权益重算投资额，0=禁用，复刻 trader.checkInvestmentRefresh）、`ScoreMode`（"balanced"（默认，回撤惩罚系数1.5）| "return_focused"（回撤惩罚系数0.3），只影响退火搜索怎么打分选参数，不改变单次回测本身的成交/回撤结果））、`SimResult`（单次回测结果，含 `TTradeReduces`/`DrawdownCloses`/`SmallPositionCloses`/`TotalFeesPaid`/`InvestmentRefreshes` 计数） |
| `backtest/grid.go` | 复刻 `trader/auto_trader_grid.go` 的 `calculateATRBounds`/`initializeGridLevels`：ATR 边界（回测使用原生 4h K 线的 Wilder ATR14 并按交易 bar 时间对齐）、gaussian/pyramid/uniform 权重分配、逐层 `AllocatedUSD`；另复刻网格重置逻辑（`checkGridSkew`/`maybeRebuildGrid`）：`checkGridSkew` 判断价格是否超出网格边界（upper*1.02 或 lower*0.98），`maybeResetGrid` 触发后重新计算边界、重建全部层级，并把仍持仓的层按价格就近迁移到新层（与实盘 trader/auto_trader_grid.go 的 2% 阈值保持一致）|
| `backtest/simulate.go` | `Simulate()`/`SimulateWithATR()` 纯函数：拉历史K线跑网格模拟，后者使用原生 4h ATR14 按时间对齐（成交模型简化——K线 High/Low 区间覆盖某层价格即视为成交，不模拟部分成交/做市排队）+ 手续费模拟（按 `FeePct` 对每笔成交/减仓/平仓的名义价值收取，计入 `cashReleased` 与 `TotalFeesPaid`）+ 三套风控机制精确复刻：①逐层 T 字打标记/挂减仓单/减仓单成交释放该层的状态机（复刻 `ttradeTagOrders`/`ttradeProcessFills`/`placeTTradeReduceOrder`）②利润回撤峰值全平（复刻 `checkPositionDrawdown`：浮盈>5%后从峰值回撤超过阈值即全平该侧）③小仓位自动平仓（复刻 `checkProfitReduce` 的早退分支：浮盈超过止盈步进2倍且名义价值<$100即全平）④止盈阶梯减仓（`applyProfitReduce`，三者互斥，按①②③④优先级触发）+ 全仓强平检测（`crossMarginMaintenanceRate`=0.5% 固定维持保证金率）。输出收益率/最大回撤/成交层数/各类减仓与平仓次数/累计手续费；`Score(returnPct, maxDrawdownPct, mode)` 按「收益 - 回撤惩罚系数×最大回撤」打分（系数由 `GridParams.ScoreMode` 决定），爆仓固定给 `-1e9` 极端惩罚分（不受 ScoreMode 影响） |
| `backtest/anneal.go` | `Anneal()` 通用模拟退火循环，`AnnealConfig.OnProgress` 回调用于流式上报迭代进度（供 SSE handler 使用），不知道传输层细节 |
| `scripts/grid_backtest/main.go` | CLI 入口，薄封装调用 `backtest` 包，含 T字/利润回撤/小仓位平仓/手续费率/评分模式对应 flag。用法：`go run ./scripts/grid_backtest -symbol HYPEUSDT -timeframe 15m -days 60 -investment 1000 -iterations 3000 -enable-ttrade -ttrade-position-threshold-pct 30 -fee-pct 0.02 -score-mode balanced` |
| `api/backtest.go` | `handleGridBacktestRun` — SSE 接口，流式推送 `baseline`/`progress`/`done`/`error` 四种事件，路由 `GET /api/backtest/grid/run`（`api/server.go`，需登录）。基准网格参数（`grid_count`/`atr_multiplier`/`distribution`/`profit_reduce_step_pct`/`profit_reduce_multiplier`/`enable_trapped_reduce`/`t_trade_position_threshold_pct`/`t_trade_spread_pct`/`profit_drawdown_threshold`/`enable_small_position_close`/`fee_pct`/`score_mode`）均可通过 query 覆盖，默认才用硬编码猜测值（`fee_pct` 默认 0.02 对齐 OKX 常规档位 maker 费率，`score_mode` 默认 `balanced`） |
| `web/src/pages/GridBacktestPage.tsx` | 前端页面：策略下拉选择器（`GET /api/strategies`，同 `PromptTestPage.tsx` 的模式，不再依赖"当前激活策略"）+ 参数表单 + SSE 流式读取（`fetch` + `ReadableStream`，同 `App.tsx` 订单事件流的读取方式）+ 基准/最优结果对比卡片。选中策略后用其 `grid_config` 真实值预填基准参数（symbol/leverage/investment + 上述全部网格与风控字段），未选择则保留通用默认值，可手动覆盖任意字段；`fee_pct`/`score_mode` 无实盘配置对应项，始终用表单默认值。T字相关输入仅在勾选启用后展示。周期+天数下方有一行纯展示的"约拉取 N 根K线"提示（`TIMEFRAME_MINUTES` 换算，跟"退火迭代次数"无关，两者是独立参数，不参与任何请求）。搜索结果出来后（`best` 卡片内），新增"应用最优参数到策略"：下拉选目标策略 + 确认弹窗（`confirmToast`，同 `StrategyStudioPage.tsx` 删除策略的模式）+ `PUT /api/strategies/:id`，只覆盖 `grid_config` 里 10 个 backtest 真正搜索/模拟的字段（`grid_count`/`atr_multiplier`/`distribution`/`leverage`/`profit_reduce_step_pct`/`profit_reduce_multiplier`/`enable_trapped_reduce`/`t_trade_position_threshold_pct`/`t_trade_spread_pct`/`profit_drawdown_threshold`/`enable_small_position_close`），其余字段（symbol/total_investment/decision_mode 等）原样保留；`fee_pct`/`score_mode` 无实盘对应项不写回。若目标策略正在运行，后端 `handleUpdateStrategy` 已自带的 `PushStrategyToTraders` 会自动热更新，前端不需要额外处理。导航栏入口：`HeaderBar.tsx` 桌面版 `navTabs`（`/grid-backtest`，与 `prompt-test` 一样未接入移动端菜单） |

只打印/展示建议参数，不写回任何策略配置或数据库。

---

## web/src/ — 前端（React + TypeScript）

### 页面

| 文件 | 说明 |
|------|------|
| `pages/TraderDashboardPage.tsx` | Trader 主看板（持仓、决策历史、图表）；统一使用页面纵向滚动，避免嵌套滚动容器导致返回顶部失效；移动端关闭高开销背景模糊/动画、使用动态视口高度，并按动画帧合并行情刷新，提升触摸滚动流畅度 |
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
| `GridConfigEditor.tsx` | 网格策略配置编辑器（层数、投资额、T-trade 阈值、决策模式等） |
| `RiskControlEditor.tsx` | AI 交易风险控制编辑器，含原生移动止盈止损开关 |

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

### 2026-08-22

| 修改 | 说明 | 提交 |
|------|------|------|
| **异常波动网格接替** | 交易员支持标签与显式网格->AI 接替绑定；每秒按实时价计算三分钟滚动涨跌幅，绝对值达到阈值后暂停源网格，按涨跌方向撤销对侧普通入口单（排除 T-trade/止盈减仓单），保留仓位和源交易员并启动同账户目标 AI | f67358ef |
| **AI K线质量修复** | 统一排序去重并过滤未收盘 K 线，校验 OHLCV 与 CoinAnk 数组字段；最新已收盘 K 线成交量为 0 时拒绝该周期数据，Prompt 仅标记最新已完成 K 线 | 未提交 |

### 2026-08-21

| 修改 | 说明 | 提交 |
|------|------|------|
| **网格回测同步 4h ATR14** | 回测新增 `SimulateWithATR`，使用原生 4h K 线按时间对齐 Wilder ATR14；API/CLI 同时获取交易周期和 4h K 线，初始化与自动重建均使用可见的 4h ATR；live ATR 边界路径优先读取 `TimeframeData["4h"]` | 未提交 |

### 2026-08-18

| 修改 | 说明 | 提交 |
|------|------|------|
| **同步网格重置逻辑** — 将 `trader/auto_trader_grid.go` 的新网格重置逻辑（`maybeRebuildGrid` 边界判定 ±2%，ATR14 自适应边界重建）同步到 `backtest/grid.go`，替换旧的 checkSkew/resetGrid；移除回测中的重复/损坏行 | 实盘在 2026-08-09 重构后采用 `maybeRebuildGrid`（价格超出 [lower×0.98, upper×1.02] 即触发 ATR 边界重建），回测仍用旧逻辑（3%/5% 冲突标记），导致回测结果不代表实盘行为 | 3330a87e |
| **移除 autoAdjustGrid 调用残留** — `trader/auto_trader_grid.go` 中 `syncExchangeState` 的 `runPostChecks` 分支仍调用已删除的 `autoAdjustGrid()`，导致编译错误 | `autoAdjustGrid` 在 3330a87e 被删除（功能合并到 `maybeRebuildGrid`），但 `syncExchangeState` 的调用点未清理 | 749acb7a |

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

### 2026-07-29

| 变更 | 说明 | 修改位置 |
|-----|------|----------|
| **T-trade 减仓单被交易所撤单前若已部分成交，重挂时数量算错导致超额减仓** — `ttradeRepairOrders` 发现减仓单状态变为 `CANCELED`/`EXPIRED` 时，直接用 `entry.Qty`（下单时的原始全额数量）重新挂一笔同等数量的减仓单，完全没检查 OKX 返回的 `executedQty`。但交易所的 `canceled` 状态并不代表零成交——一个单可以先部分成交、剩余部分再被撤，此时 `executedQty > 0`。已成交的那部分已经真实减了仓，重挂时还按全额重挂，等于对同一批仓位多减了一次，是持仓在价格未触及预期点位时也会莫名减少的原因之一 | `GetOrderStatus` 各交易所实现普遍返回 `executedQty`（部分成交量），但 `ttradeRepairOrders` 的 `CANCELED`/`EXPIRED` 分支从未读取它 | `trader/auto_trader_grid.go` `ttradeRepairOrders()` — `CANCELED`/`EXPIRED` 分支改为读取 `statusMap["executedQty"]`：若 >0，先把已成交部分记一条 `ttrade_reduce` 日志（避免这部分减仓事件丢失），再用 `entry.Qty - executedQty`（剩余未成交量）重新挂单，而不是原始全额；若剩余量 ≤0（已通过多次部分成交完全成交）则直接清理该条目、释放对应网格层，不再重挂 |
| **止盈减仓单（profit_reduce）无任何保护，`resetGrid`/`cancelAllGridOrders` 会把它连同普通网格挂单一起撤销，且撤销后没有补偿机制** — T-trade 减仓单一直有 `activeTTradeReduceOrderIDs()` 保护，但 `checkProfitReduce()` 挂出的止盈减仓单从未被追踪，`cancelAllGridOrders` 视其为普通挂单一并撤销；`checkProfitReduce` 只在浮盈达到**新**的百分比阶梯时才会重新计算下单，同一阶梯不会重试，撤销后这笔减仓意图直接丢失，不会自动补挂 | 缺少与 T-trade 平行的追踪+保护机制 | `trader/auto_trader_grid.go` — `GridState` 新增 `ProfitReduceOrderIDs map[string]bool`；`checkProfitReduce()` 下单成功后写入该表；`syncExchangeState()` 每轮用当前交易所挂单列表清理已成交/已撤销的过期条目，避免无限增长；新增 `activeProfitReduceOrderIDs()`（纯内存，无 DB 兜底——止盈减仓触发频繁且 `syncExchangeState` 每轮都清理，不需要像 T-trade 那样跨重启持久化）；`cancelAllGridOrders()` 与 `cancelGridOrder()`（AI 的 `cancel_order` 执行路径）均改为同时读取 T-trade 与止盈减仓两套保护集合 |
| **T-trade 减仓单重挂价格会随 `t_trade_spread_pct` 配置变动而漂移，无法从价格判断是否为"复活"的同一笔单** — `placeTTradeReduceOrder` 原来永远读取*当前* `gridConfig.TTradeSpreadPct` 重新计算价格，若这期间用户改过该配置，重挂出来的价格会和原单不同 | 重新计算价格用了实时配置而非下单时记录的 spread | `trader/auto_trader_grid.go` `placeTTradeReduceOrder()` — 新增可选 variadic 参数 `overrideSpreadPct`（不影响原有 4 处调用点）；`ttradeRepairOrders()` 重挂剩余数量时显式传入 `entry.SpreadPct`（下单时记录的原始 spread），确保重挂价格与原单 `entry.ReducePrice` 完全一致，便于从价格直接识别是否为同一笔单的复活 |

### 2026-07-30

| Bug | 根因 | 修复位置 |
|-----|------|----------|
| **网格挂单被资金/仓位上限裁剪后，层级记录的 `OrderQuantity` 仍是 AI 请求的原始未裁剪数量** — `placeGridLimitOrder` 计算出实际下单的 `quantity`（经保证金/仓位价值上限裁剪）后，却把 `Levels[d.LevelIndex].OrderQuantity` 赋值成 `d.Quantity`（AI 决策里的原始请求量，未裁剪）。只要触发过裁剪，该层级记录的"挂单数量"就和交易所上真实挂着的数量对不上，所有把 `OrderQuantity` 当作成交量真值的下游逻辑都会被污染 | 记录用的变量选错了：用了裁剪前的请求值而非裁剪后的实际下单值 | `trader/auto_trader_grid.go` `placeGridLimitOrder()` — `Levels[d.LevelIndex].OrderQuantity` 改为赋值 `quantity`（裁剪后、真正发给交易所的数量），日志同时打印实际下单量与原始请求量 |
| **T-trade 减仓单数量算错，实际减仓量远超交易所真实成交量**（如某笔挂了 6.7589 的减仓单，交易所真实成交只有 3.7）— `syncExchangeState` 检测到网格层挂单成交时，`level.PositionSize` 与 T-trade "late-detect"（成交太快、未被 `ttradeTagOrders` 提前打标记）兜底路径的减仓数量都直接用 `level.OrderQuantity`，而不是 `GetOrderStatus` 返回的真实 `executedQty`。一是继承了上面 `OrderQuantity` 被裁剪污染的问题，二是即便没有裁剪问题，`OrderQuantity` 本身也只是"下单请求量"而非"真实成交量"，遇到部分成交场景必然算错 | 用请求量代替交易所返回的真实成交量 | `trader/auto_trader_grid.go` `syncExchangeState()` — `orderFillInfo` 新增 `executedQty` 字段，从 `GetOrderStatus` 的 `executedQty` populate；成交检测逻辑改为 `fillQty := info.executedQty`（仅当 `<=0` 即交易所未返回时才回退到 `level.OrderQuantity`），并将 `fillQty` 同时用于 `level.PositionSize`、已打标记 T-trade 减仓路径的 `reduceQty`、late-detect 兜底路径的减仓量三处 |

### 2026-07-30（续）

| 变更 | 说明 | 修改位置 |
|-----|------|----------|
| **T-trade 面板里大量"标记了但没有下一步"的僵尸记录，取消后不会消失** — `syncExchangeState` 检测到 T-trade 标记单被撤销/过期时只打了 `logger.Infof` 控制台日志，从未写入 `ttrade_cancel` 这个 DB 事件（尽管 `activeTTradePrepOrderIDs` 的重启恢复逻辑本就假设这个 action 存在并据此判断是否"已终结"）；前端 `TTradePanel.tsx` 按 `order_id` 分组展示生命周期，一笔只有 `ttrade_tag` 却永远等不到下一条事件的分组会永久停留在"标记"状态、堆在列表里 | 该终结事件从未被写入，是设计遗漏而非前端 bug | `trader/auto_trader_grid.go` `syncExchangeState()` — cancelled/expired 分支收集被取消的 prep（含 side/price/qty），解锁后统一 `logGridTrade("ttrade", "ttrade_cancel", ...)` 落盘（沿用 `pendingReduces` 那种"锁内收集、解锁后处理"的写法）；`web/src/components/TTradePanel.tsx` `groupTTradeEvents()` 中，凡是有 `ttrade_cancel` 且从未 `ttrade_fill` 的分组直接从展示列表中丢弃，不再残留 |

### 2026-08-02 — 清理确认无调用方的死代码

一次实盘 vs 回测的差距分析顺带发现：`trader/` 里有几处"看起来是重要风控机制"的函数，实际从未被生产路径调用——要么从未接线，要么接了但函数体是空的。逐个用全仓库 grep 交叉验证调用方（含 `web/src`、`kernel/`、`api/`、`store/`、测试文件）后确认以下均为死代码并删除：

| 删除内容 | 根因/说明 |
|---------|-----------|
| `trader/auto_trader_grid.go` 的 `checkBreakout`/`checkBoxBreakout`/`handleBreakout`/`executeBreakoutAction`/`closeAllPositions`/`checkFalseBreakoutRecovery` 及其专属类型 `BreakoutType`/`BreakoutNone`/`BreakoutUpper`/`BreakoutLower` | 突破检测从未被 `RunGridCycle` 调用；`RunGridCycle` 里有一段独立的 box 数据刷新代码（供风险面板显示用，已保留），跟这批死函数只是"读同一份数据"，没有调用关系 |
| `trader/auto_trader_grid.go` 的 `checkMaxDrawdown`/`checkDailyLossLimit`/`updateDailyPnL` | 零调用方；对应的 `MaxDrawdownPct`/`DailyLossLimitPct` 配置字段本身也从未被前端编辑器或 AI prompt 读取，当时未动这些字段（这几个 struct 字段后来在下面"排查回测与实盘参数差异"那次一并删除了） |
| `trader/auto_trader_grid.go` 的 `checkAndExecuteStopLoss` | 函数体是空的（注释明写"disabled"），删除空壳调用点 |
| `trader/grid_regime.go`（整个文件删除）：`classifyRegimeLevel`/`getDynamicLeverage`/`getDynamicPositionLimit`/`detectBoxBreakout`/`confirmBreakout`/`getBreakoutAction`/`BreakoutState`/`BreakoutAction` | 前三者仅测试引用或完全零引用；后几个是上面删除 `checkBoxBreakout` 后连带变成孤儿的支撑代码。`GridState.CurrentRegimeLevel` 字段从未被赋值（永远读到默认值 "standard"），保留不动（`GetGridRiskInfo` 仍读取它，改动需要额外碰 API 契约，不在本次范围） |
| `trader/grid_regime_test.go` 里对应的 4 个测试 | `TestClassifyRegimeLevel`/`TestDetectBoxBreakout`/`TestBreakoutConfirmation`/`TestGetBreakoutAction`——随源码一起删除，保留同文件里无关的 `TestGetBuySellRatio` |
| `trader/ttrade_enhanced.go`（整个文件删除）：`TTradeState`、`CalculateDynamicSpread`、`ValidateTTradeSignal`、`CalculatePrepPrice`、`CalculateReducePrice` | 完全自包含、零外部引用、无测试文件——是一套被真正上线的 T-trade 实现（`TTradePrepEntry`/`TTradeReduceEntry`，在 `auto_trader_grid.go` 里）取代之后留下的旧设计 |
| `trader/tp_manager.go` 的 `SetTPLevels`/`ClearTPLevels`，`trader/tp_helper.go`（整个文件删除）的 `LoadTPLevelsFromConfig` | 分批止盈功能的"配置→写入"链路整条都是死的（零调用方），但 `TPManager` 本体（`Start`/`Stop`/`monitorLoop`/`checkAndExecute`/`activeLevels`）是所有 trader（不分网格/非网格）共用的活基础设施，予以保留——只是目前 `activeLevels` 永远拿不到数据 |

删除后逐一在全仓库 grep 验证零残留引用，`gofmt -l` 确认格式正常。

### 2026-08-02（续）— 回测补充手续费与单侧仓位上限模拟

延续同一次实盘 vs 回测差距分析，把其中标记为"相对容易量化建模"的两处差距补进 `backtest/` 离线回测：

| 变更 | 说明 | 修改位置 |
|-----|------|----------|
| **手续费模拟** — 此前 `Simulate()` 完全不扣手续费，收益率会系统性偏高，且退火搜索会偏向"高频/密集网格/频繁 T字减仓"这类实盘手续费吃得最狠的参数组合 | 按 `FeePct`（默认 0.02%，对齐 OKX 常规档位 maker 费率）对每笔成交的名义价值收取，从入场成交、T字减仓成交、止盈阶梯减仓、峰值回撤全平、小仓位全平五处一并扣除，累计计入 `SimResult.TotalFeesPaid` | `backtest/types.go`（`GridParams.FeePct`/`SimResult.TotalFeesPaid`）、`backtest/simulate.go`（`applyProfitReduce`/`closeSide`/`applyRiskChecks` 新增 `feePct` 参数并返回 fee；入场成交与 T字减仓成交处直接计算并扣除） |
| **单侧仓位价值上限** — 此前每层挂单只要价格被K线覆盖就无条件成交，不会像实盘 `checkTotalPositionLimit` 那样在单侧仓位价值超过 `TotalInvestment×Leverage×MaxPositionSizePct/100` 时拒单，偏向单边的趋势行情下回测会持续加仓，实盘早被这个上限拦住了 | 入场成交前检查该侧成交后名义价值是否超过 cap，超过则跳过本次成交（挂单保持 pending，下一根K线重新判断），不影响 T字/风控减仓（只会缩小仓位，不需要校验）；`MaxPositionSizePct<=0` 回退到 100（不额外限制），与实盘 fallback 语义一致 | `backtest/types.go`（`GridParams.MaxPositionSizePct`/`SimResult.CapRejectedFills`）、`backtest/simulate.go`（层成交循环新增 cap 检查） |

两个新字段均为"固定传入值，不参与退火搜索"，与已有的 T字/回撤/小仓位平仓字段同一类别；`FeePct=0` 与 `MaxPositionSizePct<=0` 时行为与改动前完全一致（默认 100% cap 在实际网格分配下几乎不可能触发）。同时把 `web/src/types.ts` 的 `GridStrategyConfig` 补上了此前未暴露给前端的 `max_position_size_pct` 字段，供回测页面预填基准参数用；CLI（`-fee-pct`/`-max-position-size-pct`）、API query 参数（`fee_pct`/`max_position_size_pct`）、前端表单同步更新。

### 2026-08-02（三）— 网格重置不再等待 T-trade 标记单成交

`autoAdjustGrid()`（网格倾斜自动调整）与 `checkInvestmentRefresh()`（定期资金刷新）原先都有一段"只要 `TTradePrepOrders`/`TTradeReduceOrders` 里还有条目就整个跳过本次重置"的粗粒度保护。逐一排查后发现这层保护过度保守：

- **止盈减仓单、T-trade 减仓单**（已成交转入 `TTradeReduceOrders` 的）从未绑定到任何 `Levels` 条目，`resetGrid` 的重建逻辑根本不会碰它们，`cancelAllGridOrders` 的逐单保护已经足够，完全不需要因为它们的存在而挡住整个重置。
- **T-trade 标记单**（`TTradePrepOrders`，还没成交、层状态是 `"pending"`）才是真正有风险的一种——`resetGrid` 原来只会把 `state=="filled"` 的层按最近价迁移到新网格，`"pending"` 的层会被直接推倒重建。即便这笔标记单被 `cancelAllGridOrders` 保护、没有被撤单，重建后 `Levels`/`OrderBook` 里也会彻底找不到它，导致它后续真的成交时，仓位对账（`syncExchangeState` 的 expected vs actual 仓位比较）和按订单ID撤单（AI 的 `cancel_order`）都会失真。

修复思路是直接把这个"账本会丢"的根因堵上，而不是继续靠外层等待硬扛：

| 变更 | 修改位置 |
|-----|----------|
| `resetGrid()` 现在会在重建前，把仍处于 `"pending"` 状态的层（此时必然是被保护的 T-trade 标记单——`cancelAllGridOrders` 已经把所有*非*保护的挂单层清成 `"empty"`）单独收集，重建后按下单价格（不是成交价，因为还没成交）就近迁移到新层，恢复 `State`/`Side`/`OrderID`/`OrderQuantity`/`OrderPlacedAt`，并同步更新 `OrderBook[orderID]` 为新的层下标（此前 `OrderBook` 是 `cancelAllGridOrders` 用旧层下标建的，重建后如果不同步就会指向错误的层） | `trader/auto_trader_grid.go` `resetGrid()` |
| 移除 `autoAdjustGrid()` 与 `checkInvestmentRefresh()` 里"T-trade 有挂单就整个跳过重置"的外层保护——`resetGrid` 自身已经能正确处理，不再需要多等 | `trader/auto_trader_grid.go` `autoAdjustGrid()`、`checkInvestmentRefresh()` |

减仓单（T-trade 与止盈）本身的保护（不被撤单）不受影响，改动的只是"要不要因为有标记单/减仓单挂着就连整个重置都不做"这一层。

### 2026-08-02（四）— 删除"单侧仓位上限 %"（`MaxPositionSizePct`），实盘与回测一并移除

排查"单侧仓位上限 %"为什么在实盘里看起来没生效时发现：这不是执行逻辑的 bug，而是这个功能从一开始就没有可用的配置入口——

- 策略配置是整体序列化成 JSON blob 存的（`StrategyConfig.Config` 字段），不是走 GORM 表列，所以 `store/grid.go` 里那个 `gorm:"default:35"` 根本不生效——那张 `grid_configs` 表（`GridConfigModel`）本身就是从未被实际读写过的历史遗留表（`SaveGridConfig`/`LoadGridConfig`/`ListGridConfigs` 零调用方），跟真正在用的策略配置无关。
- `web/src/components/strategy/GridConfigEditor.tsx` 从来没有"单侧仓位上限 %"这个输入框，连 `defaultGridConfig` 预设里都没有这个字段。

所以任何通过前端创建的策略，`MaxPositionSizePct` 在真正使用的 `GridStrategyConfig`（`store/strategy.go`）里永远是 Go 零值 `0`，被 `checkTotalPositionLimit` 的 `<=0 → 100` 兜底吃掉——100% 上限在正常网格分配下（单侧最多约占用50%名义仓位）几乎不可能触发，等于形同虚设。用户确认不需要修复，直接删除整个功能，并同步移除本次一起加进回测的对应实现：

| 删除内容 | 位置 |
|---------|------|
| `checkTotalPositionLimit()` 函数及其在 `placeGridLimitOrder()` 里的调用点 | `trader/auto_trader_grid.go` |
| `GridStrategyConfig.MaxPositionSizePct` 字段 | `store/strategy.go` |
| `GridParams.MaxPositionSizePct`、`SimResult.CapRejectedFills`，`Simulate()` 里对应的仓位上限检查逻辑 | `backtest/types.go`、`backtest/simulate.go` |
| `max_position_size_pct` query 参数与 `-max-position-size-pct` CLI flag | `api/backtest.go`、`scripts/grid_backtest/main.go` |
| `max_position_size_pct`/`cap_rejected_fills` 相关的 state、表单输入框、翻译文案、`GridStrategyConfig` 类型字段 | `web/src/pages/GridBacktestPage.tsx`、`web/src/types.ts` |

`store/grid.go` 里 `GridConfigModel.MaxPositionSizePct`（那张从未被使用的历史遗留表）本次未动，因为它跟这个功能是否生效完全无关，属于更早就存在、范围更大的另一个死代码问题。

### 2026-08-02（五）— 修复网格重置不再等待后暴露的减仓单保护竞态

上一条改动（移除 `autoAdjustGrid`/`checkInvestmentRefresh` 等待 T-trade 挂单的外层保护）上线后，用户反馈实盘出现两笔"卖出平多"减仓单在网格重置时被误撤（订单号 3792321529529425920、3792378871973339136，2026-08-02 11:07:06）。排查后确认：**不是保护逻辑本身写错了，是一个此前被外层等待意外掩盖的竞态条件**，改动去掉那层等待后这个窗口被放大、更容易撞上。

具体机制：`placeTTradeReduceOrder`（T-trade 减仓单）与 `checkProfitReduce`（止盈减仓单）下单时，都是先调用交易所下单接口，**接口返回成功后才**把订单号写进 `TTradeReduceOrders`/`ProfitReduceOrderIDs`——这中间有一段真实存在的时间窗口（网络往返）：订单已经在交易所上挂着了，但本地保护表还没来得及记上它。此外 T-trade 侧的下单是通过 `go at.placeTTradeReduceOrder(...)` 异步派发的，不等它跑完就继续往下走。如果 `cancelAllGridOrders()`（`resetGrid`/`autoAdjustGrid`/`checkInvestmentRefresh` 都会走到这里）恰好在这个窗口内运行，构建保护集合时用的还是"更新前"的表，就会把这笔已经真实存在、理应被保护的减仓单当成普通挂单撤掉。

以前这条路径能大概率跑通，纯粹是因为外层"只要有 T-trade 挂单就整个跳过重置"的判断顺手把这个窗口盖住了；不是真的解决了竞态，是很少真正触发到。移除外层等待后，重置更频繁地在这个窗口内运行，问题才暴露出来。

修复思路是把这个窗口本身堵上，而不是走回头路恢复外层等待：

| 变更 | 修改位置 |
|-----|----------|
| `GridState` 新增 `PendingReducePlacements int32`（原子计数器）：下单前 `+1`，订单号成功写入保护表后 `-1`（T-trade 侧在 `placeTTradeReduceOrder` 里用 `defer` 保证异步 goroutine 的每条退出路径都会回落；止盈减仓侧因为是同步代码直接首尾各写一次） | `trader/auto_trader_grid.go` `GridState`、`placeTTradeReduceOrder()`、`checkProfitReduce()` |
| `cancelAllGridOrders()` 开头新增等待：计数器归零前不构建保护集合，最多等 5 秒（超时后按改动前的行为继续，不会卡死整个网格维护） | `trader/auto_trader_grid.go` `cancelAllGridOrders()` |

这样无论 `resetGrid` 由网格倾斜、资金刷新还是其他路径触发、也无论触发得多频繁，只要有减仓单正在下单过程中，`cancelAllGridOrders` 都会先等它落地记账、再决定撤哪些单——不再依赖"恰好有别的 T-trade 挂单顺手把整个重置卡住"这种偶然的保护。

### 2026-08-02（六）— 排查上一条竞态时顺带发现：减仓单会被误"收编"成网格层挂单

排查主账号那笔"没重置网格却被撤单"的报告时确认：那也是修复前同一批竞态问题的现场记录，不是新 bug。但排查过程中确认了另一个真实存在、独立的问题——`syncExchangeState` 的"收编未跟踪交易所挂单"逻辑（服务重启后/会话外挂的单，按价格就近认领到空的网格层）只检查订单ID是否在 `OrderBook` 里，没有排除 T-trade 减仓单和止盈减仓单——这两类单从设计上就不会进 `OrderBook`（它们独立存在于 `TTradeReduceOrders`/`ProfitReduceOrderIDs`，保护判断也是按订单ID直查这两张表，跟 `OrderBook`/`Levels` 无关）。结果就是一笔减仓单如果价格恰好落在某个空网格层附近（半个网格间距内），会被误当成普通网格挂单"收编"进 `Levels`，把它的方向/数量按网格层记账——不会导致被撤单（撤单判断本来就跟 Levels 无关），但会污染仓位记账：这笔减仓单以后真成交时会被当成"网格层新开仓"处理，而不是识别成减仓，可能让 `PositionSize`/`TotalTrades` 记错。

| 修复 | 位置 |
|-----|------|
| 收编逻辑遍历交易所挂单时，新增对 `TTradePrepOrders`/`TTradeReduceOrders`/`ProfitReduceOrderIDs` 的排除检查，命中任意一张表就跳过、不收编 | `trader/auto_trader_grid.go` `syncExchangeState()`（"Adopt untracked exchange orders" 段） |

### 2026-08-03 — 新增算法决策模式（不依赖 AI 也能维护网格）

起因：AI 网络不稳定时（超时、返回内容解析失败）网格周期只能兜底 hold，整个周期不维护网格，完全靠 API 稳定性撑着。排查执行链路后发现：`place_buy_limit`/`place_sell_limit`/`cancel_order` 的执行代码（`executeGridDecision`→`placeGridLimitOrder`/`cancelGridOrder`）完全不关心决策是不是 AI 给的，`Reasoning`/`Confidence` 只用于日志展示——这意味着可以写一个确定性算法直接生成同样格式的 `kernel.Decision`，走一模一样的执行链路（下单前的资金校验、撤单前的 T字/止盈减仓单保护，全部原样复用，不用重新实现）。

新增 `GridStrategyConfig.DecisionMode`（`"ai"` | `"ai_with_algo_fallback"` | `"algo_only"`，为空默认 `"ai"`，行为完全不变）：

| 模式 | 行为 |
|-----|------|
| `ai`（默认） | 跟之前完全一样，不受本次改动影响 |
| `ai_with_algo_fallback` | 正常调用 AI；AI 调用报错，或者返回内容解析失败被兜底成 hold（`FullDecision.ParseFailed`），这两种情况都视为"AI 不可用"，切换成算法决策；AI 真的自己决定 hold 则不受影响 |
| `algo_only` | 完全不调用 AI，每个周期都走算法 |

算法本身（`buildAlgoGridDecision`）只做两件事：① 每个空网格层按预设价位、`SuggestedQuantity`（从 `kernel/grid_engine.go` 里给 AI 展示"建议数量"的公式抽出来的共用函数，跟 AI prompt 表格算的是同一个数）算出的数量补单；② 挂单超过 6 小时（`algoStaleOrderTimeout`）未成交就撤销，让该层下一周期在当前价位重新评估。刻意不做"价格偏离网格范围就撤单"——网格边界的重新计算本来就由 `checkGridSkew`/`resetGrid`/`checkInvestmentRefresh` 这些跟决策模式无关的机制在管，不需要再加一套重复的判断。

| 变更 | 位置 |
|-----|------|
| `GridStrategyConfig` 新增 `DecisionMode string` | `store/strategy.go` |
| `FullDecision` 新增 `ParseFailed bool`，在 `GetGridDecisions` 解析失败兜底 hold 时置位，供调用方区分"AI 真 hold"与"AI 响应不可用"，不再需要靠字符串匹配 Reasoning 文案判断 | `kernel/engine.go`、`kernel/grid_engine.go` |
| 抽出共用的 `SuggestedQuantity(level, ctx)` 函数，替换掉中英文两份 prompt 构建函数里原本逐字重复的建议数量公式 | `kernel/grid_engine.go` |
| 新增 `buildAlgoGridDecision()`（补空层+超时撤单，产出 `kernel.FullDecision`）；`RunGridCycle` 按 `DecisionMode` 分支调用 AI/算法/两者结合 | `trader/auto_trader_grid.go` |
| 新增"决策模式"下拉选择器（三个选项），`defaultGridConfig` 默认值 `'ai'` | `web/src/components/strategy/GridConfigEditor.tsx`、`web/src/types.ts` |

### 2026-08-03（续）— 回测新增评分模式："收益优先" / "收益与风险均衡"

`Score()`（退火搜索的目标函数）原来只有一套固定公式「收益 - 1.5×最大回撤」，搜索器只会往"稳"的方向偏。新增 `GridParams.ScoreMode`：`"balanced"`（默认，回撤惩罚系数 1.5，跟改动前完全一样）与 `"return_focused"`（回撤惩罚系数 0.3，让搜索更愿意接受高回撤换更高收益的组合）。注意这个参数**不改变单次回测本身的成交/回撤/手续费结果**——同一组参数在两种模式下 `ReturnPct`/`MaxDrawdownPct` 完全一样，只有 `Score` 字段的值不同，进而影响退火搜索最终选中哪一组参数。爆仓（`BlewUp`）固定给 `-1e9` 极端惩罚分，不受 `ScoreMode` 影响——"收益优先"也不会选出一个会打爆仓的组合。

| 变更 | 位置 |
|-----|------|
| `GridParams` 新增 `ScoreMode string`（`"balanced"`\|`"return_focused"`） | `backtest/types.go` |
| `Score()` 签名新增 `mode string` 参数，按 mode 选择回撤惩罚系数 | `backtest/simulate.go` |
| 新增 `score_mode` query 参数 / `-score-mode` CLI flag，默认 `balanced` | `api/backtest.go`、`scripts/grid_backtest/main.go` |
| 新增"评分策略"下拉选择器（收益与风险均衡/收益优先） | `web/src/pages/GridBacktestPage.tsx` |

### 2026-08-06（续）— 回测补充网格失衡自动重建

排查回测与实盘参数差异时发现的最大一项缺口：实盘遇到单边行情、买卖失衡超3倍（或一侧完全空仓）会自动撤单、按当前价重建网格（`checkGridSkew`/`autoAdjustGrid`/`resetGrid`），回测的网格边界和层级此前建好后永远不变，趋势行情下会持续偏离、结果被系统性拉偏。

新增 `checkGridSkew(levels)`（复刻同名实盘函数，用"已成交/未成交"层数近似实盘的"filled/empty"状态区分）+ `maybeResetGrid(...)`（复刻 `autoAdjustGrid`+`resetGrid`：失衡判定通过后，还要求当前价偏离网格中心超过区间30%才真正重建，跟实盘的双重门槛一致；重建时重新算边界、重建全部层级，已成交层按价格就近迁移到新层，这一步在回测里是精确匹配而非近似，因为模拟器的成交价从不偏离层的目标价）。每根K线在风控检查后、爆仓判断前跑一次；T字减仓单在途时跳过重建（避免 `pendingReduces` 里的层索引指向重建后数组里不相关的层），近似实盘"重建时保护在途减仓单"的效果。新增 `SimResult.GridResets` 计数上报重建次数。

| 变更 | 位置 |
|-----|------|
| 新增 `checkGridSkew`/`maybeResetGrid` | `backtest/grid.go` |
| 主循环每根K线新增第4.5步重建检查；`SimResult` 新增 `GridResets` | `backtest/simulate.go`、`backtest/types.go` |
| `printResult` 新增 `grid_resets` 展示 | `scripts/grid_backtest/main.go` |
| 结果卡片新增"网格重建次数"展示 | `web/src/pages/GridBacktestPage.tsx` |

### 2026-08-06 — 清理网格配置里的三个死字段

排查回测与实盘参数差异时发现：`GridStrategyConfig.MaxDrawdownPct`/`.StopLossPct`/`.DailyLossLimitPct` 定义在 struct 里、能存进数据库，但全仓库零读取——`auto_trader_grid.go` 里约30处 `gridConfig := at.config.StrategyConfig.GridConfig` 取值从没碰过这三个字段，前端 `GridConfigEditor.tsx` 也从没暴露过对应输入框。溯源到 `docs/plans/2026-01-14-grid-trading-fixes.md`，当年是计划做止损/回撤/日损限制，但从未真正落地到代码，一直是纯占位字段。

顺带核实了另一个疑似死代码候选——非网格路径 `RiskControlConfig.ProfitDrawdownPct`/`.ProfitThresholdPct`——确认是**活代码**：`checkProfitDrawdown()`（`auto_trader.go:2546`）读取这两个字段做峰值回撤全平判断，且被 `auto_trader.go:541`/`728` 两处真实调用，未做改动。

| 变更 | 位置 |
|-----|------|
| 删除 `GridStrategyConfig` 的 `MaxDrawdownPct`/`StopLossPct`/`DailyLossLimitPct` 三个字段 | `store/strategy.go` |
| 删除 `GridStrategyConfig` TS 镜像接口里对应的三个字段 | `web/src/types.ts` |

`store/grid.go`（`GridConfigModel`，那张已确认零调用方的历史遗留表）里的同名字段本次未动，维持之前清理 `MaxPositionSizePct` 时的判断——那是更早、范围更大的另一个死代码问题，不在本次范围内。

### 2026-08-07 — 修复网格失衡重建引入的回测性能回归（O(n²) → O(n)）

用户反馈网格回测变慢很多，排查是上一条"网格失衡自动重建"改动引入的性能回归：`maybeResetGrid` 每根K线都要重新算一次 ATR14 判断是否需要重建，而 `atrAt(klines, idx)` 每次调用都从 `klines[:idx]` 重新算全量真实波幅序列再跑一遍 Wilder 平滑——单次 O(idx)，放进主循环逐根K线调用后整体变成 O(n²)，K线数量/回测天数越大越慢。

Wilder 平滑本身就是 O(1) 的滚动递推公式（`atr = (atr*(period-1) + tr)/period`），没有必要每次从头重算。改为一次性预计算：新增 `atrSeries(klines) []float64`（`backtest/grid.go`）在 O(n) 内为每个下标算出对应的 ATR14 值，数值与原来逐次重算完全一致（同样的算式、同样的浮点运算顺序），只是不再重复劳动。`atrAt` 删除，`Simulate()`（`backtest/simulate.go`）里原来两处调用改为对预计算数组做 O(1) 查表。

未加开关——这是纯粹的算法复杂度 bug，直接修掉比加开关"绕开"更合适，修复后行为（何时重建、重建到什么状态）与之前完全一致，只是变快了。

| 变更 | 位置 |
|-----|------|
| 删除 `atrAt`，新增 O(n) 一次性预计算的 `atrSeries` | `backtest/grid.go` |
| 两处调用改为查表 `atr14Series[idx]` | `backtest/simulate.go` |

### 2026-08-07（续）— 算法决策模式的下单日志改用独立 `"algo"` 标签

`RunGridCycle` 走算法决策模式（`algo_only` 或 `ai_with_algo_fallback` 触发的降级）时，`executeGridDecision` 之前无条件把 `logGridTrade` 的 source 写成 `"ai"`，导致算法补单在前端交易日志里显示成 AI 下单标签，无法区分。

`RunGridCycle` 新增 `source` 局部变量（`buildAlgoGridDecision` 路径设为 `"algo"`，否则 `"ai"`），透传给 `executeGridDecision(d, ctx, source)`，写日志时用该值而非硬编码 `"ai"`。`GridTradeLogModel.Source` 取值集合新增 `"algo"`；前端 `SOURCE_COLORS`（`web/src/pages/TraderDashboardPage.tsx`）加对应配色，`types.ts` 注释同步。

| 变更 | 位置 |
|-----|------|
| `executeGridDecision` 新增 `source` 参数，日志写入真实来源而非硬编码 `"ai"` | `trader/auto_trader_grid.go` |
| `Source` 取值集合新增 `"algo"` | `store/grid.go`、`web/src/types.ts` |
| 新增 `algo` 配色 | `web/src/pages/TraderDashboardPage.tsx` |

### 2026-08-08 — 算法决策模式下单前先按可用余额过滤，不再整屏刷决策列表

用户反馈算法模式一个周期刷出一长串 `algo: filling empty grid level` 的 WAIT 卡片，但实际能下的单远少于此——`buildAlgoGridDecision` 之前对每个 `"empty"` 层都无条件生成一条 `place_*_limit` 决策，完全不管余额，指望 `RunGridCycle` 里那个粗粒度的 `gridCtx.AvailableBalance < 1.0` 兜底（或干脆等交易所拒单）来收场，实际效果是决策列表照样很长，只是后面静默失败/跳过。

改为在生成决策的同一个循环里维护一个 `availableMargin` 累计余额（初值 `ctx.AvailableBalance`），每accepted一个空层就按 `qty*price/leverage` 估算所需保证金并扣减；余额不够的层直接跳过（不生成决策），继续扫描后面的层（后面价位更便宜的层可能仍然够用，不整体截断）。全部层都因为余额不足被跳过时，`hold` 的 `Reasoning` 会带上"N 个空层因余额不足未下单"的提示，而不是笼统的"nothing to do"。

| 变更 | 位置 |
|-----|------|
| `buildAlgoGridDecision` 新增按 `AvailableBalance` 累计扣减的可下单性过滤，跳过的空层不再生成决策 | `trader/auto_trader_grid.go` |
| `hold` 的 `Reasoning`、算法模式 `CoTTrace` 补充"因余额不足跳过 N 个空层"的计数 | `trader/auto_trader_grid.go` |

### 2026-08-08（续）— 修复减仓单在下单落地窗口内被误收编成网格层挂单

用户反馈实盘出现一笔"賣出平多"（止盈减仓关多）成交后，系统日志把它当成 T-trade 标记单，自动派了一张"减仓"单去平根本不存在的空头。排查确认是 `2026-08-02（六）` 那次修复的防御不完整：`syncExchangeState()` 的"收编未跟踪交易所挂单"逻辑只按订单ID排除已记录在 `TTradePrepOrders`/`TTradeReduceOrders`/`ProfitReduceOrderIDs` 里的单，但 `checkProfitReduce`/`placeTTradeReduceOrder` 都是**先调用交易所下单接口，返回成功后才**把订单号写进这些表——这段网络往返窗口内，一笔已经真实挂在交易所上的减仓单在本地还没有任何ID记录，如果 `syncExchangeState` 恰好在这个窗口跑收编逻辑，仅凭ID排除认不出它，就会按价格就近把它收编成空网格层的开仓挂单；如果价格又恰好落在某个"看似应该开空"的层附近，后续 `ttradeTagOrders` 就会把它错当成 T-trade 标记单继续处理。

`ttradeTagOrders` 本身早就有"SELL+LONG 或 BUY+SHORT 必然是平仓单，不可能是新开仓"的结构性判断（`side`/`posSide` 组合），但收编逻辑没有复用这条判断，只靠ID一层防护，窗口期内失效。修复：收编逻辑新增同样的 side/positionSide 结构检查，不管ID有没有被记上保护表，SELL+LONG / BUY+SHORT 组合永远不收编。

| 变更 | 位置 |
|-----|------|
| "收编未跟踪交易所挂单"循环新增 side/positionSide 结构检查（与 `ttradeTagOrders` 一致） | `trader/auto_trader_grid.go` `syncExchangeState()` |

### 2026-08-08（三）— 减仓单保护等待超时从 5 秒提高到 30 秒

排查上一条 bug 现场时发现同一次事故里还有第二个独立缺口：一笔止盈减仓单（`checkProfitReduce` 下的"賣出平多"）在下单确认窗口内被 `resetGrid`（`checkInvestmentRefresh` 定期资金刷新触发）批量撤单误撤——跟同一批次的几笔网格开仓单一起被撤，是 `cancelAllGridOrders()` 的典型特征。

根因在 `cancelAllGridOrders()` 开头等待 `PendingReducePlacements` 归零的逻辑（`2026-08-02（五）`引入）：等待时间硬编码 5 秒，超时后直接放弃保护、按改动前的行为继续（注释里也写明这是已知风险，不是新引入的）。交易所下单确认在网络抖动或 API 变慢时超过 5 秒并不罕见，一旦命中就会让这笔已经真实挂在交易所上、本该受保护的减仓单被当成普通网格挂单撤掉——跟上一条 bug 同属"下单确认延迟窗口"这一类问题的另一处表现，但触发路径不同（那个是收编逻辑缺结构判断，这个是等待超时不够长）。

用户确认直接把超时从 5 秒提高到 30 秒，缩小窗口触发概率（不是从根本上消除，正常网络延迟不会超过几秒，只有极端变慢才会撞上）。

| 变更 | 位置 |
|-----|------|
| `cancelAllGridOrders()` 等待 `PendingReducePlacements` 归零的超时从 5 秒改为 30 秒 | `trader/auto_trader_grid.go` |

### 2026-08-08（四）— 网格决策动作名归一化，容忍 AI 不遵循 prompt 词表

用户更换了一个 AI 模型后，网格决策卡片全变成了 `WAIT_FOR_CONFIRMATION`/`MONITOR_STOP_LOSS`/`ADD_BUY_ORDERS`/`REBALANCE_GRID`/大写 `CANCEL_ORDER` 这类词，跟 `BuildGridSystemPrompt` 里规定的小写动作词表（`hold`/`place_buy_limit`/`cancel_order`...）完全不一致。

排查确认这不是解析失败——JSON 本身能正常解析，`parseGridDecisions` 只在动作名不在白名单里时打一条 `Invalid grid action` 的 warning 日志，不会拦截或改写。真正出问题的是 `executeGridDecision` 的 switch 语句：这些自造/大小写不一致的动作名匹配不到任何已知 case，直接落到 `default` 分支——只打一条 `Unknown action` 日志，不下单、不撤单、不返回错误。结果是这个 AI 每个周期的所有决策都被静默丢弃，网格完全没被维护，但前端看不出任何报错，只有一堆"看起来正常"但什么都没发生的决策卡片。

修复不是去纠正这个 AI（模型行为不受控），而是在解析层加一层归一化，把常见的同义/变体动作名映射回规范词表：

| 变更 | 位置 |
|-----|------|
| 新增 `gridActionSynonyms` 映射表（如 `wait_for_confirmation`/`monitor_stop_loss`/`no_action` → `hold`，`add_buy_order(s)`/`open_buy_limit` → `place_buy_limit`，`rebalance`/`rebalance_grid`/`reset_grid` → `adjust_grid`，`cancel_all` → `cancel_all_orders`）+ `normalizeGridAction()`（先转小写再查表，未命中的动作原样小写返回，让 `isValidGridAction` 的告警仍能捕获真正未知的动作） | `kernel/grid_engine.go` |
| `parseGridDecisions` 在校验前先跑归一化，命中时打一条 Info 日志记录改写前后的值 | `kernel/grid_engine.go` |

只覆盖了这次实际观察到的变体，不是穷举所有可能的 AI 输出；后续换模型如果又出现新的自造动作名，需要照这个模式往 `gridActionSynonyms` 里追加。

### 2026-08-08（五）— 修复金字塔资金分配在卖方权重方向反了

用户反馈"金字塔分配"感觉有问题，排查确认权重公式 `weights[i] = GridCount - i` 只是对绝对下标 `i` 做单调线性递减，跟 gaussian 那个"以当前价为中心对称"的公式（`|i - center|`）不一样——完全不知道当前价格在网格里的位置。

网格下标 `i` 从下往上递增（`i=0` 是最低价，`i=GridCount-1` 是最高价），买方层（价格低于当前价）恰好落在低下标区间，"下标越小权重越大"跟"离当前价越远权重越大"方向一致，看起来符合"金字塔"直觉；但卖方层（价格高于当前价）落在高下标区间，同一个公式在这边是"下标越大权重越小"，等于"离当前价越远权重越小"——跟买方方向正好相反，网格最顶部（该重仓做空的位置）反而分到最少资金。三处复制粘贴的实现（`trader/auto_trader_grid.go` 的 `initializeGridLevels`/`initializeGridLevelsLocked`，`backtest/grid.go` 的 `buildLevels`）都有同样的问题。

修复：改成跟 gaussian 一样以当前价格所在下标为中心的对称公式 `weights[i] = 1 + |i - center|`，买卖两侧都变成"离当前价越远权重越大"，形状真正对称。

| 变更 | 位置 |
|-----|------|
| pyramid 权重公式从单侧线性 `GridCount - i` 改为以 center 对称的 `1 + |i - center|` | `trader/auto_trader_grid.go`（`initializeGridLevels`、`initializeGridLevelsLocked`）、`backtest/grid.go`（`buildLevels`） |

### 2026-08-09（六）— 浮盈减仓并发竞态与重启保护修复

用户报告浮盈减仓出现重复下单（同一阶梯触发两次）和重启后减仓单被取消的问题。

**问题1：并发竞态导致重复减仓**

`checkProfitReduce()` 有两个并发调用点：
- Grid Cycle（`auto_trader_grid.go:625`）：每个周期主动调用，传 `nil`
- WS Position Push（`auto_trader.go:539`）：持仓推送时触发，传入 `positions` 数据

如果两者几乎同时触发（如 WS 推送到达时 Grid Cycle 定时器也恰好到期），都会通过 954 行的 `targetReducePct <= alreadyReduced` 判断（此时读到相同的旧值），然后都执行下单，1108-1111 行才更新 `LongProfitReducedPct`。虽然 1008-1059 行有"检查交易所是否已有减仓单"的逻辑，但如果第一次下单还没返回（API 延迟），第二次检查不到，就会重复下单。

**修复**：在下单前（1071行之前）新增原子检查：
- 锁内再次验证 `targetReducePct <= currentReduced`，如果并发调用已更新则跳过
- 预先更新 `LongProfitReducedPct = targetReducePct`（占位）
- 下单成功后保持该值；下单失败则回滚到 `oldPct`

**问题2：重启后减仓单被取消**

`ProfitReduceOrderIDs` 是内存映射（不持久化），重启后为空。`cancelAllGridOrders()` 通过 `activeProfitReduceOrderIDs()` 获取需要保护的减仓单ID，但重启后该映射为空，交易所上还挂着的减仓单会被当成"普通单"撤掉。

原先 `InitializeGrid` 在 210-313 行恢复浮盈减仓进度时，只恢复了 `LongProfitReducedPct` 的值（从日志读取），没有恢复 `ProfitReduceOrderIDs`。

**修复**：在恢复进度时同步恢复 `ProfitReduceOrderIDs`：
- 调用 `GetOpenOrders()` 和 `GetPositions()` 获取交易所当前状态
- 推断哪些是减仓单（方向与持仓相反、价格在 mark price 2% 以内、不在网格层级里）
- 恢复匹配的订单ID到 `ProfitReduceOrderIDs`，使其受 `cancelAllGridOrders` 保护

**问题3：时序竞态导致保护失效**

`InitializeGrid()` 在 205 行就设置 `IsInitialized = true`，然后 210-415 行才执行恢复逻辑（需要网络调用 `GetOpenOrders`/`GetPositions`）。`auto_trader.go:505` 在 `InitializeGrid()` 返回后立即调用 `RunGridCycle()`，如果这次调用抢在恢复逻辑完成之前执行：
- 看到 `IsInitialized = true`（通过检查）
- 但 `ProfitReduceOrderIDs = {}`（还没恢复）
- AI 决策或其他逻辑触发 `cancelAllGridOrders()`
- `activeProfitReduceOrderIDs()` 返回空集合
- 减仓单不在保护列表 → 被撤销

**修复**：延迟设置 `IsInitialized = true` 到恢复逻辑完成之后（487行，`return nil` 之前），阻止 `RunGridCycle` 在保护映射未就绪时进入。

**附加优化：去除冗余 GetPositions 调用**

Grid Cycle 调用 `checkProfitReduce(nil)` 会触发内部调用 `GetPositions()`，但 WS position push 已经每 2 秒推送一次并触发 `checkProfitReduce(positions)`，Grid Cycle 的调用是冗余的。

修改 `checkProfitReduce(nil)` 语义：`nil` 表示"跳过检查"而非"获取持仓后检查"，Grid Cycle 传 `nil` 直接返回，依赖 WS 推送触发检查，减少一次 REST API 调用。

| 变更 | 位置 |
|-----|------|
| `checkProfitReduce` 下单前新增原子检查：锁内再次验证进度、预先更新、失败时回滚 | `trader/auto_trader_grid.go:1059-1093` |
| `InitializeGrid` 恢复 `LongProfitReducedPct` 时，从交易所挂单推断并恢复 `ProfitReduceOrderIDs` | `trader/auto_trader_grid.go:211-310` |
| 延迟设置 `IsInitialized = true` 到所有恢复逻辑完成之后（`return nil` 之前） | `trader/auto_trader_grid.go:487` |
| `checkProfitReduce(nil)` 语义改为"跳过检查"，Grid Cycle 不再调用 `GetPositions()` | `trader/auto_trader_grid.go:901-906` |
| 测试修复：显式传入 mock positions 而非依赖已删除的 `GetPositions()` 回退 | `trader/profit_reduce_duplicate_test.go` |

### 2026-08-09（六）— Claude API Prompt Caching 启用

启用 Anthropic Claude API 的 prompt caching 功能，减少重复 token 开销。网格 AI 的 system prompt 在单个交易周期内几乎完全一致（只依赖 GridConfig，不依赖实时市场数据），缓存命中率高。

| 变更 | 位置 |
|-----|------|
| `ClaudeClient` 的 system prompt 从裸字符串改为带 `cache_control: {type: "ephemeral"}` 的对象数组格式 | `mcp/claude_client.go` |
| 解析响应中的 `usage.cache_read_input_tokens` 和 `usage.cache_creation_input_tokens`，传给 `TokenUsage` 结构体 | `mcp/claude_client.go` |
| `TokenUsage` 新增 `CacheReadTokens` 和 `CacheWriteTokens` 字段 | `mcp/request.go` |

缓存 TTL：新模型（Opus 4/Sonnet 4）1小时，旧模型 5 分钟。网格 AI 调用间隔通常在 TTL 以内，可持续命中缓存。

### 2026-08-09（六）— Grid AI System Prompt 角色描述更新

原 system prompt 的角色描述过于狭窄，只提到"判断市场状态、补充空层级"，但 AI 实际可以执行的操作更多（撤单、重置网格、观望）。

| 变更 | 位置 |
|-----|------|
| 角色描述从"执行引擎"改为"决策引擎"，列出所有可用操作（place_buy_limit、place_sell_limit、cancel_order、cancel_all_orders、adjust_grid、hold） | `kernel/grid_engine.go` |
| 新增"极端单边"市场状态行：网格严重失衡且价格偏离中心 > 30% 时，用 `adjust_grid` 重建 | `kernel/grid_engine.go` |
| 明确说明系统自动处理的部分（浮盈减仓、T-trade 标记/减仓、自动网格重建），AI 无需输出这些决策 | `kernel/grid_engine.go` |
| 重新组织约束条件：补单规则、撤单规则、禁用操作、零余额处理，分节清晰列出 | `kernel/grid_engine.go` |

中英文 prompt 同步更新。无逻辑变更，纯文档优化。

### 2026-08-09（六）— 网格重置逻辑重构：从订单失衡改为价格边界触发

原网格重置逻辑基于订单成交失衡（一侧 filled ≥ 3倍另一侧，且 >5 个），依赖订单状态，对趋势响应滞后。重构为基于价格位置的直接触发。

**问题1：投资额刷新触发不必要的网格重置**

`checkInvestmentRefresh` 每 2 天刷新一次投资额（从余额重新计算），之前会自动调用 `resetGrid(currentPrice)`，导致所有挂单被取消、网格重建。对于持续运行的策略，这会造成不必要的订单扰动和持仓中断。

**修复**：移除 `checkInvestmentRefresh` 中的 `resetGrid` 调用。投资额仍然按周期刷新，但不再触发网格重置。网格会通过 `autoAdjustGrid` 在价格显著偏离时自然调整。

**问题2：订单失衡检测不直观且滞后**

`checkGridSkew` 统计买卖两侧的 filled/empty 订单数，通过比例判断失衡（3x、>5 个）。这个逻辑：
- 依赖订单状态，不直接反映价格位置
- 趋势行情中，价格可能已跑出网格很远，但订单状态变化滞后
- 阈值（3x、>5）较为武断，难以调优

**修复**：重构 `checkGridSkew` 为基于价格边界的触发条件：
- 当 `currentPrice > upper * 1.05` 或 `currentPrice < lower * 0.95` 时触发重置
- 直接反映价格位置，不依赖订单状态
- 阈值清晰：价格超出网格边界 5% 即重置

**问题3：autoAdjustGrid 重复检查价格偏离**

`autoAdjustGrid` 在 `checkGridSkew` 返回 skewed 后，还会再次计算 `math.Abs(currentPrice-midPrice) < gridRange*0.3` 做二次过滤。价格检查逻辑分散在两个函数中，不够清晰。

**修复**：将价格检查完全集成到 `checkGridSkew` 中，`autoAdjustGrid` 直接根据返回值决定是否重置。

| 变更 | 位置 |
|-----|------|
| `checkInvestmentRefresh` 移除 `resetGrid` 调用，只刷新投资额 | `trader/auto_trader_grid.go:1645-1651` |
| `checkGridSkew` 从订单失衡检测改为价格边界检测（超出 upper*1.05 或低于 lower*0.95） | `trader/auto_trader_grid.go:2507-2546` |
| `autoAdjustGrid` 移除重复的价格偏离检查，简化逻辑 | `trader/auto_trader_grid.go:2641-2661` |
| 回测同步：`backtest/grid.go` 的 `checkGridSkew` 和 `maybeResetGrid` 使用相同的 5% 边界触发 | `backtest/grid.go:127-150, 161-185` |

**影响**：
- 网格对趋势行情响应更快（价格一旦超出边界 5% 立即触发，不等订单状态变化）
- 减少误触发（投资额刷新不再重置网格）
- 重置条件更清晰，便于调试和优化
- 回测与实盘逻辑保持一致，确保回测结果能准确预测实盘表现

### 已知设计限制（待优化）

| 问题 | 说明 |
|------|------|
| **多持仓冲突** | `sides` map 以 `"long"`/`"short"` 为 key，同方向多持仓时后者覆盖前者，`checkProfitReduce()` 只处理最后一条 |
| **减仓进度状态共享** | `LongProfitReducedPct` / `ShortProfitReducedPct` 全局只有一个值，同方向多持仓无法独立跟踪各自的减仓阶梯 |
| **手动重置后重复触发** | `ResetProfitTracker()` 将 `alreadyReduced` 清零，重置后该方向所有阶梯重新可触发 |

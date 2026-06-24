# K线收盘事件驱动 AI 周期架构分析

## 📋 概述

NOFX 网格交易采用**事件驱动架构**，通过 WebSocket 实时监听 OKX 交易所的 K线收盘事件，触发 AI 决策周期，显著降低 API 调用频率，提高系统响应速度。

## 🏗️ 架构层次

```
┌─────────────────────────────────────────────────────────┐
│                    事件驱动架构                          │
├─────────────────────────────────────────────────────────┤
│                                                         │
│  OKX 交易所                                             │
│     ↓ WebSocket 推送                                    │
│  OKXWebSocket (trader/okx/ws.go)                       │
│     ↓ handleCandle() 解析                               │
│  OnKlineClose 回调                                      │
│     ↓ notifyGridCycle() 触发                            │
│  wsGridCycleCh 通道 (chan struct{})                     │
│     ↓ goroutine 监听                                    │
│  RunGridCycle() 执行                                    │
│     ├─ RunTTradeScan()        [系统级]                  │
│     ├─ checkProfitReduce()    [系统级]                  │
│     ├─ checkInvestmentRefresh()                         │
│     ├─ buildGridContext()     [AI 级]                   │
│     ├─ GetGridDecisions()     [AI 级]                   │
│     └─ executeDecisions()     [AI 级]                   │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

## 🔌 核心组件详解

### 1. OKXWebSocket - WebSocket 客户端

**文件**: `trader/okx/ws.go`

#### 核心结构

```go
type OKXWebSocket struct {
    // 认证信息
    apiKey, secretKey, passphrase string
    
    // 连接管理
    pubConn  *websocket.Conn  // 公开频道（行情、K线）
    privConn *websocket.Conn  // 私有频道（账户、订单、持仓）
    
    // 实时缓存
    wsBalance   map[string]interface{}        // 账户余额
    wsPositions []map[string]interface{}      // 持仓列表
    wsOrders    map[string][]types.OpenOrder  // 挂单列表
    wsPrices    map[string]float64            // 最新价格
    wsKlines    map[string]map[string][]wsKlineBar // K线数据
    
    // 事件回调
    OnPositionUpdate func() // 持仓变动回调
    OnOrderEvent     func() // 订单事件回调
    OnKlineClose     func() // K线收盘回调
    
    // K线配置
    klineTfs       []string // 订阅的周期 (e.g. ["5m", "4h"])
    primaryKlineTf string   // 主周期 (触发 OnKlineClose)
}
```

#### K线数据处理

```go
func (ws *OKXWebSocket) handleCandle(channel, instId string, raw json.RawMessage) {
    // 1. 解析 OKX K线推送数据
    //    格式: [[ts, open, high, low, close, vol, volCcy, volCcyQuote, confirm]]
    var rows [][]interface{}
    json.Unmarshal(raw, &rows)
    
    // 2. 提取时间周期
    //    "candle5m" → "5m"
    //    "candle4H" → "4h"
    tf := strings.ToLower(strings.TrimPrefix(channel, "candle"))
    
    // 3. 更新 K线缓存
    ws.klineMu.Lock()
    for _, row := range rows {
        bar := parseOKXCandleRow(row)
        confirm := fmt.Sprintf("%v", row[8]) == "1"  // 确认收盘
        bar.Confirmed = confirm
        
        // 更新或追加 K线
        if lastBar.Ts == bar.Ts {
            buf[len(buf)-1] = bar  // 更新未收盘K线
        } else {
            buf = append(buf, bar)  // 新K线
        }
        
        // 🔔 触发收盘事件
        if confirm && tf == ws.primaryKlineTf && ws.OnKlineClose != nil {
            ws.OnKlineClose()
        }
    }
    ws.wsKlines[instId][tf] = buf
    ws.klineMu.Unlock()
}
```

**关键点**：
- OKX 只在K线确认收盘时发送 `confirm=1`，每根K线只触发一次
- `primaryKlineTf` 决定哪个周期触发 AI 决策（默认 "5m"）
- K线缓存保留最近 300 根（约 25 小时历史数据）

### 2. AutoTrader - 事件通道管理

**文件**: `trader/auto_trader.go`

#### 通道定义

```go
type AutoTrader struct {
    // WebSocket 事件通道
    wsScanCh      chan struct{}  // T-trade/利润减仓扫描事件
    wsGridCycleCh chan struct{}  // K线收盘 → AI 决策周期事件
    
    // 事件时间戳（原子操作）
    wsLastKlineClose int64  // 最后一次K线收盘时间（纳秒）
    
    // 回调函数（由 API 层设置，用于 SSE 推送）
    OnOrderUpdate func()  // 订单变动 → 前端实时更新
}
```

#### 通道初始化顺序

```go
func (at *AutoTrader) Run() error {
    isGridStrategy := at.config.StrategyConfig.StrategyType == "grid_trading"
    
    // ⚠️ 关键：必须在 InitializeGrid() 之前创建通道
    // 因为 InitializeGrid() 会启动 WebSocket 并设置回调
    if isGridStrategy {
        at.wsScanCh = make(chan struct{}, 1)       // 缓冲=1，非阻塞
        at.wsGridCycleCh = make(chan struct{}, 1)  // 缓冲=1，非阻塞
    }
    
    // 初始化网格（内部会启动 WebSocket）
    if err := at.InitializeGrid(); err != nil {
        return err
    }
    
    // 启动事件监听 goroutine（后文详述）
    ...
}
```

### 3. WebSocket 回调绑定

**文件**: `trader/auto_trader_grid.go`

```go
func (at *AutoTrader) InitializeGrid() error {
    gridConfig := at.config.StrategyConfig.GridConfig
    triggerTf := gridConfig.AITriggerTf  // 例如 "5m"
    if triggerTf == "" {
        triggerTf = "5m"
    }
    
    // 类型断言：检查 trader 是否支持 WebSocket
    type wsStarter interface {
        StartWS(symbol string, primaryTf string) error
    }
    type wsCallbackSetter interface {
        SetWSCallbacks(onPosition, onOrder, onKlineClose func())
    }
    
    if starter, ok := at.trader.(wsStarter); ok {
        // 1. 启动 WebSocket 连接
        if err := starter.StartWS(gridConfig.Symbol, triggerTf); err != nil {
            logger.Warnf("[Grid] OKX WS start failed: %v", err)
        } else {
            logger.Infof("[Grid] OKX WS started for %s", gridConfig.Symbol)
            
            // 2. 设置回调函数
            if setter, ok := at.trader.(wsCallbackSetter); ok {
                // 持仓/订单变动 → 触发 T-trade 扫描
                notifyScan := func() {
                    if at.wsScanCh != nil {
                        select {
                        case at.wsScanCh <- struct{}{}:
                        default:  // 通道满则丢弃（去抖动）
                        }
                    }
                    // 同时通知前端更新
                    if at.OnOrderUpdate != nil {
                        at.OnOrderUpdate()
                    }
                }
                
                // K线收盘 → 触发 AI 决策
                notifyGridCycle := func() {
                    if at.wsGridCycleCh != nil {
                        atomic.StoreInt64(&at.wsLastKlineClose, time.Now().UnixNano())
                        select {
                        case at.wsGridCycleCh <- struct{}{}:
                        default:
                        }
                    }
                }
                
                setter.SetWSCallbacks(notifyScan, notifyScan, notifyGridCycle)
                logger.Infof("[Grid] event-driven mode: AI cycle on %s kline close", triggerTf)
            }
        }
    }
    
    return nil
}
```

## 🔄 事件流完整分析

### 流程 1: K线收盘事件 → AI 决策周期

```
┌─────────────────────────────────────────────────────────────┐
│  Step 1: OKX 交易所推送 K线收盘                              │
├─────────────────────────────────────────────────────────────┤
│  • 时间: 每 5 分钟整点（例如 12:00, 12:05, 12:10）          │
│  • 数据: {"data":[[ts, o, h, l, c, vol, ..., confirm: "1"]]}│
│  • 频道: wss://ws.okx.com:8443/ws/v5/public                 │
│  • 订阅: {"channel": "candle5m", "instId": "HYPE-USDT-SWAP"}│
└─────────────────────────────────────────────────────────────┘
                    ↓
┌─────────────────────────────────────────────────────────────┐
│  Step 2: OKXWebSocket.handleCandle() 解析                    │
├─────────────────────────────────────────────────────────────┤
│  ws.klineMu.Lock()                                           │
│  for _, row := range rows {                                 │
│      bar := parseOKXCandleRow(row)                           │
│      confirm := row[8] == "1"  // ✅ 收盘确认                │
│      if confirm && tf == "5m" && ws.OnKlineClose != nil {   │
│          ws.OnKlineClose()  // 🔔 触发回调                   │
│      }                                                       │
│  }                                                           │
│  ws.klineMu.Unlock()                                         │
└─────────────────────────────────────────────────────────────┘
                    ↓
┌─────────────────────────────────────────────────────────────┐
│  Step 3: notifyGridCycle() 回调执行                          │
├─────────────────────────────────────────────────────────────┤
│  func notifyGridCycle() {                                    │
│      // 记录时间戳（原子操作）                               │
│      atomic.StoreInt64(&at.wsLastKlineClose,                │
│                        time.Now().UnixNano())                │
│      // 非阻塞发送信号                                       │
│      select {                                                │
│      case at.wsGridCycleCh <- struct{}{}:                    │
│          // 成功发送                                         │
│      default:                                                │
│          // 通道已满，丢弃（去抖动）                         │
│      }                                                       │
│  }                                                           │
└─────────────────────────────────────────────────────────────┘
                    ↓
┌─────────────────────────────────────────────────────────────┐
│  Step 4: goroutine 监听通道                                  │
├─────────────────────────────────────────────────────────────┤
│  go func() {                                                 │
│      for {                                                   │
│          select {                                            │
│          case <-at.wsGridCycleCh:  // 🎯 接收信号            │
│              // 检查运行状态                                 │
│              at.isRunningMutex.RLock()                       │
│              running := at.isRunning                         │
│              at.isRunningMutex.RUnlock()                     │
│              if !running { return }                          │
│              // 执行网格周期                                 │
│              if err := at.RunGridCycle(); err != nil {       │
│                  logger.Infof("Grid execution failed: %v", err)│
│              }                                               │
│          case <-at.stopMonitorCh:                            │
│              return  // 停止信号                             │
│          }                                                   │
│      }                                                       │
│  }()                                                         │
└─────────────────────────────────────────────────────────────┘
                    ↓
┌─────────────────────────────────────────────────────────────┐
│  Step 5: RunGridCycle() 执行                                 │
├─────────────────────────────────────────────────────────────┤
│  func (at *AutoTrader) RunGridCycle() error {               │
│      // 系统级操作（始终执行）                               │
│      openOrders := at.trader.GetOpenOrders(symbol)          │
│      at.RunTTradeScan(openOrders)  // T-trade 扫描           │
│      at.checkProfitReduce()         // 利润减仓检查          │
│      at.checkInvestmentRefresh()    // 资金刷新              │
│      // AI 级操作（可能被暂停）                              │
│      if !at.gridState.IsPaused {                             │
│          ctx := at.buildGridContext()     // 构建上下文      │
│          decision := GetGridDecisions(ctx) // AI 决策        │
│          at.executeDecisions(decision)    // 执行            │
│      }                                                       │
│  }                                                           │
└─────────────────────────────────────────────────────────────┘
```

### 流程 2: 持仓/订单变动 → T-trade/利润减仓扫描

```
┌─────────────────────────────────────────────────────────────┐
│  OKX 推送: 持仓变动 / 订单成交                               │
├─────────────────────────────────────────────────────────────┤
│  • 频道: wss://ws.okx.com:8443/ws/v5/private               │
│  • 推送: positions / orders                                 │
└─────────────────────────────────────────────────────────────┘
                    ↓
┌─────────────────────────────────────────────────────────────┐
│  OKXWebSocket.handlePositions() / handleOrders()             │
├─────────────────────────────────────────────────────────────┤
│  ws.positionsMu.Lock()                                       │
│  ws.wsPositions = parsePositions(data)                       │
│  ws.positionsMu.Unlock()                                     │
│  if ws.OnPositionUpdate != nil {                             │
│      ws.OnPositionUpdate()  // 🔔 触发回调                   │
│  }                                                           │
└─────────────────────────────────────────────────────────────┘
                    ↓
┌─────────────────────────────────────────────────────────────┐
│  notifyScan() 回调执行                                       │
├─────────────────────────────────────────────────────────────┤
│  select {                                                    │
│  case at.wsScanCh <- struct{}{}:  // 非阻塞发送              │
│  default:  // 丢弃（去抖动）                                 │
│  }                                                           │
│  if at.OnOrderUpdate != nil {                                │
│      at.OnOrderUpdate()  // 通知前端 SSE                     │
│  }                                                           │
└─────────────────────────────────────────────────────────────┘
                    ↓
┌─────────────────────────────────────────────────────────────┐
│  goroutine 监听 wsScanCh                                     │
├─────────────────────────────────────────────────────────────┤
│  case <-at.wsScanCh:                                         │
│      openOrders := at.trader.GetOpenOrders(symbol)          │
│      if gridConfig.EnableTrappedReduce {                     │
│          at.RunTTradeScan(openOrders)  // T-trade            │
│      }                                                       │
│      if gridConfig.EnableProfitReduce {                      │
│          at.checkProfitReduce()        // 利润减仓           │
│      }                                                       │
│      at.checkProfitDrawdown()           // 回撤检查          │
└─────────────────────────────────────────────────────────────┘
```

## ⚙️ 系统级 vs AI 级操作

### 系统级操作（始终执行）

这些操作**不依赖 AI**，基于规则自动执行：

```go
func (at *AutoTrader) RunGridCycle() error {
    // 1. T-Trade 扫描（无论网格是否暂停）
    openOrders, _ := at.trader.GetOpenOrders(symbol)
    at.syncOpenOrdersFromExchange(openOrders)
    at.RunTTradeScan(openOrders)
    //   ├─ ttradeTagOrders()      标记符合条件的订单
    //   ├─ ttradeRepairOrders()   修复失效订单
    //   └─ ttradeProcessFills()   处理成交并下减仓单
    
    // 2. 利润减仓检查
    if gridConfig.EnableProfitReduce {
        at.checkProfitReduce()
        //   ├─ 计算每边浮盈百分比
        //   ├─ 判断是否达到下一级阶梯（10% 步进）
        //   ├─ 检查是否已存在减仓单（防重复）
        //   └─ 下单 reduce-only 限价单
    }
    
    // 3. 投资额刷新
    if gridConfig.EnableInvestmentRefresh {
        at.checkInvestmentRefresh()
        //   └─ 每 N 天从钱包余额更新 TotalInvestment
    }
    
    // ... AI 级操作在后面
}
```

### AI 级操作（可被暂停）

这些操作依赖 AI 决策，网格暂停时跳过：

```go
func (at *AutoTrader) RunGridCycle() error {
    // ... 系统级操作 ...
    
    // 检查网格是否暂停
    at.gridState.mu.RLock()
    isPaused := at.gridState.IsPaused
    at.gridState.mu.RUnlock()
    
    if isPaused {
        logger.Infof("[Grid] Grid is paused, skipping AI cycle")
        return nil
    }
    
    // 1. 构建 AI 上下文
    gridCtx, err := at.buildGridContext()
    //   ├─ 获取市场数据（价格、K线、技术指标）
    //   ├─ 读取持仓和余额
    //   ├─ 构建网格层级表
    //   ├─ 添加 T-trade 状态
    //   └─ 添加利润减仓进度
    
    // 2. 调用 AI 获取决策
    decision, err := kernel.GetGridDecisions(gridCtx, at.mcpClient, strategyConfig, lang)
    //   ├─ BuildGridSystemPrompt()  构建系统提示词
    //   ├─ BuildGridUserPrompt()    构建用户提示词
    //   ├─ mcpClient.CallWithMessages()  调用 AI API
    //   └─ parseGridDecisions()     解析 JSON 决策
    
    // 3. 执行 AI 决策
    for _, d := range decision.Decisions {
        switch d.Action {
        case "place_buy_limit", "place_sell_limit":
            at.trader.PlaceLimitOrder(...)
        case "cancel_order":
            at.trader.CancelOrder(d.OrderID)
        case "reduce_long", "reduce_short":
            at.trader.ReducePosition(...)
        case "adjust_grid":
            at.recalculateGridBounds(...)
        case "hold":
            // 保持当前状态
        }
    }
    
    // 4. 记录决策日志
    at.store.Decision().CreateGridDecision(...)
    
    return nil
}
```

## 🚀 性能优化对比

### 传统轮询模式（已废弃）

```
每 5 分钟执行一次 RunGridCycle():
├─ GetBalance()        REST API 调用 1
├─ GetPositions()      REST API 调用 2
├─ GetOpenOrders()     REST API 调用 3
├─ GetMarketPrice()    REST API 调用 4
├─ GetMarketData()     REST API 调用 5+
├─ AI 决策             AI API 调用
└─ 下单/撤单           REST API 调用 N

总计: 5-15 次 REST API 调用 + 1 次 AI 调用 / 5分钟
```

### WebSocket 事件驱动模式（当前）

```
K线收盘时执行 RunGridCycle():
├─ GetOpenOrders()     REST API 调用 1 (必需)
├─ GetBalance()        📦 使用 WS 缓存（0 API）
├─ GetPositions()      📦 使用 WS 缓存（0 API）
├─ GetMarketPrice()    📦 使用 WS 缓存（0 API）
├─ GetMarketData()     📦 使用 WS K线缓存（0 API）
├─ AI 决策             AI API 调用
└─ 下单/撤单           REST API 调用 N

总计: 1-N 次 REST API 调用 + 1 次 AI 调用 / 5分钟

持仓/订单变动时执行系统级扫描:
└─ GetOpenOrders()     REST API 调用 1

总计: 1 次 REST API 调用（无 AI 调用）
```

### 性能提升

| 指标 | 轮询模式 | WS 模式 | 提升 |
|------|---------|---------|------|
| REST API 调用/周期 | 5-15 次 | 1-N 次 | 70-90% ↓ |
| 延迟（数据新鲜度） | 最高 5 分钟 | 实时 | 99% ↓ |
| 网络带宽 | 高 | 低 | 80% ↓ |
| 响应速度 | 轮询间隔 | 事件驱动 | 即时 |


## 🛡️ 容错与降级机制

### 1. WebSocket 断线重连

```go
func (ws *OKXWebSocket) maintainConnection(conn **websocket.Conn, url string, isPrivate bool) {
    for {
        select {
        case <-ws.ctx.Done():
            return
        default:
        }
        
        // 检查连接状态
        if *conn == nil {
            newConn, err := websocket.Dial(url, ...)
            if err != nil {
                logger.Warnf("[OKX WS] reconnect failed: %v", err)
                time.Sleep(5 * time.Second)
                continue
            }
            *conn = newConn
            // 重新订阅所有频道
            ws.resubscribeAll(conn, isPrivate)
        }
        
        time.Sleep(1 * time.Second)
    }
}
```

### 2. 定时器降级（Fallback）

当 WebSocket 连接失败或长时间无数据时，自动降级到定时器模式：

```go
func (at *AutoTrader) Run() error {
    ticker := time.NewTicker(scanInterval)  // 例如 5 分钟
    defer ticker.Stop()
    
    for {
        select {
        case <-at.wsGridCycleCh:
            // 优先使用 WS 事件
            at.RunGridCycle()
            
        case <-ticker.C:
            // 定时器降级检查
            gridCfg := at.config.StrategyConfig.GridConfig
            triggerPeriod := parseTriggerTfDuration(gridCfg.AITriggerTf)  // 5m
            lastKline := time.Unix(0, atomic.LoadInt64(&at.wsLastKlineClose))
            
            // 如果 WS 超过 (触发周期 + 30秒) 无数据 → 手动触发
            if time.Since(lastKline) > triggerPeriod+30*time.Second {
                select {
                case at.wsGridCycleCh <- struct{}{}:  // 手动触发
                    logger.Warnf("[Grid] WS silent for %v, fallback to timer trigger",
                        time.Since(lastKline))
                default:
                }
            }
            
        case <-at.stopMonitorCh:
            return nil
        }
    }
}
```

**降级逻辑**：
- WS 正常 → 每 5 分钟收到 K线收盘事件
- WS 断开 → 超过 5分30秒无事件，定时器手动触发
- 自动恢复 → WS 重连后立即恢复事件驱动

### 3. 缓存失效处理

```go
func (t *OKXTrader) GetBalance() (map[string]interface{}, error) {
    // 优先使用 WS 缓存
    if t.ws != nil {
        t.ws.balanceMu.RLock()
        if t.ws.balanceOk {
            cached := t.ws.wsBalance
            t.ws.balanceMu.RUnlock()
            return cached, nil
        }
        t.ws.balanceMu.RUnlock()
    }
    
    // 缓存未就绪 → 降级到 REST
    return t.getBalanceREST()
}
```

**缓存就绪标记**：
- `balanceOk` - 余额缓存有效
- `positionsOk` - 持仓缓存有效
- `ordersOk` - 订单缓存有效
- `pricesOk[symbol]` - 价格缓存有效


# 网格交易记录分析 (grid_trade_logs)

系统会将所有交易动作写入 `grid_trade_logs` 数据库表，方便事后分析交易系统的行为。

---

## 记录来源 (source)

| source | 含义 |
|--------|------|
| `ai` | AI 决策动作（每个周期 AI 的所有指令） |
| `ttrade` | T 字操作自动标记 / 触发事件 |
| `profit_reduce` | 盈利逐步减持系统触发的限价减仓单 |
| `profit_drawdown` | 利润回撤保护触发的平仓（预留） |

---

## 记录动作 (action)

### AI 动作 (`source = "ai"`)
AI 可以下达的所有指令均会记录，包括：

| action | 说明 |
|--------|------|
| `hold` | 本周期不操作 |
| `place_buy_limit` | 补买网格层 |
| `place_sell_limit` | 补卖网格层 |
| `reduce_long` | 限价减多仓 |
| `reduce_short` | 限价减空仓 |
| `close_long` | 市价平多（全部） |
| `close_short` | 市价平空（全部） |
| `cancel_order` | 取消某笔挂单 |
| `adjust_grid` | 调整网格参数 |
| `pause_grid` / `resume_grid` | 暂停 / 恢复网格 |

### T 字操作 (`source = "ttrade"`)

| action | 说明 |
|--------|------|
| `ttrade_tag` | 系统自动标记了一笔网格挂单作为触发单 |
| `ttrade_fill` | 触发单成交，已通知 AI 执行减仓 |

### 盈利减持 (`source = "profit_reduce"`)

| action | 说明 |
|--------|------|
| `profit_reduce` | 限价减仓（每 10% 利润触发一次） |
| `profit_reduce_close` | 利润 > 12% 且持仓价值 < $100，整体平仓 |

---

## 字段说明

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | int | 自增主键 |
| `instance_id` | text | 网格实例 ID |
| `created_at` | timestamp | 动作发生时间 |
| `source` | text | 来源（见上表） |
| `action` | text | 具体动作类型 |
| `symbol` | text | 交易对，如 `HYPEUSDT` |
| `side` | text | `long` / `short` |
| `quantity` | numeric | 下单数量 |
| `price` | numeric | 委托价格（0 = 市价） |
| `entry_price` | numeric | 动作发生时的持仓均价 |
| `mark_price` | numeric | 动作发生时的标记价格 |
| `margin_profit` | numeric | 动作发生时的保证金收益率（%） |
| `unrealized_pl` | numeric | 动作发生时的未实现盈亏（USDT） |
| `reason` | text | AI 的理由 / 系统备注 |
| `order_id` | text | 交易所返回的订单 ID |
| `success` | bool | 是否执行成功 |
| `error_msg` | text | 失败时的错误信息 |

---

## 常用查询示例

### 查看最近 100 条动作记录
```sql
SELECT created_at, source, action, side, quantity, price,
       margin_profit, unrealized_pl, success, reason
FROM grid_trade_logs
WHERE instance_id = 'your-instance-id'
ORDER BY created_at DESC
LIMIT 100;
```

### 按来源统计各类动作次数和平均利润
```sql
SELECT source, action, COUNT(*) AS count,
       ROUND(AVG(margin_profit)::numeric, 2) AS avg_profit_pct
FROM grid_trade_logs
WHERE instance_id = 'your-instance-id'
GROUP BY source, action
ORDER BY count DESC;
```

### 查看所有 T 字操作记录
```sql
SELECT created_at, action, side, quantity, price, reason, order_id
FROM grid_trade_logs
WHERE instance_id = 'your-instance-id'
  AND source = 'ttrade'
ORDER BY created_at DESC;
```

### 查看 AI 非 hold 的有效操作
```sql
SELECT created_at, action, side, quantity, price, reason, success
FROM grid_trade_logs
WHERE instance_id = 'your-instance-id'
  AND source = 'ai'
  AND action != 'hold'
ORDER BY created_at DESC;
```

### 查看盈利减持触发记录
```sql
SELECT created_at, action, side, quantity, price,
       margin_profit, unrealized_pl, success, error_msg
FROM grid_trade_logs
WHERE instance_id = 'your-instance-id'
  AND source = 'profit_reduce'
ORDER BY created_at DESC;
```

### 查看失败的动作
```sql
SELECT created_at, source, action, side, quantity, error_msg
FROM grid_trade_logs
WHERE instance_id = 'your-instance-id'
  AND success = false
ORDER BY created_at DESC;
```

### 按时间段汇总（某天的操作）
```sql
SELECT source, action, COUNT(*) AS count
FROM grid_trade_logs
WHERE instance_id = 'your-instance-id'
  AND created_at >= '2026-04-11'
  AND created_at < '2026-04-12'
GROUP BY source, action
ORDER BY count DESC;
```

---

## 注意事项

- **AI 动作记录**：`price = 0` 表示市价单（如 `close_long` / `close_short`）
- **T 字标记**：`ttrade_tag` 只记录标记事件，不代表已成交
- **盈利减持**：`margin_profit` 记录触发时的实时保证金收益率，与最终成交价无关
- 数据库使用 UTC 时间，查询时注意时区转换

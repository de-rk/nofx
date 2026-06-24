# 🐛 Bug修复: 利润减仓同一阶梯重复触发

## 问题描述

利润减仓在同一阶梯（step）重复触发，即使已经在该阶梯执行过减仓操作。

### 用户日志证据

```
22:15:04  profit_reduce  SHORT  pos=102.6  target=18%  profit=+18.6%
22:21:40  profit_reduce  SHORT  pos=100.8  target=18%  profit=+21.4%  ← 问题！
```

**分析**:
- 第一次：利润 18.6%，触发 18% 阶梯减仓 ✅
- 中间：减仓成功，持仓从 102.6 降到 100.8
- 第二次：利润 21.4%（仍在 18-24% 范围），**又触发 18% 阶梯** ❌

**预期行为**:
- 18% 阶梯触发后，应记录状态 `ShortProfitReducedPct = 18`
- 利润上涨到 21.4% 时，因为 `floor(21.4/6)*6 = 18`，与已记录的 18% 相等
- 应该**跳过**，不再触发
- 只有利润达到 24% 时才应该再次触发

## 根本原因分析

代码逻辑**本身是正确的**：

```go
alreadyReduced := at.gridState.ShortProfitReducedPct  // 从状态读取已触发的阶梯
targetReducePct := math.Floor(profitPct/step) * step   // 计算当前应该在哪个阶梯
if targetReducePct <= alreadyReduced {
    continue  // 如果当前阶梯 <= 已触发阶梯，跳过
}
```

但问题可能出现在以下几种情况：

### 可能原因 1: 状态未正确更新（最可能）

下单成功后，代码确实更新了状态：

```go
at.gridState.mu.Lock()
if info.side == "short" {
    at.gridState.ShortProfitReducedPct = newPct  // 更新为 18
}
at.gridState.mu.Unlock()
```

但这个更新是**内存中的**，如果：
- 交易员在两次触发之间**重启**
- 从日志恢复状态时出现问题
- 状态恢复逻辑有 bug

就会导致 `ShortProfitReducedPct` 被重置为 0，从而重新触发。

### 可能原因 2: 并发竞争（较小可能）

如果两个 AI 周期几乎同时执行：
1. 周期 A：读取 `alreadyReduced = 0`，计算 `targetReducePct = 18`，触发减仓
2. 周期 B：**同时**读取 `alreadyReduced = 0`，计算 `targetReducePct = 18`，也触发减仓
3. 周期 A：更新 `ShortProfitReducedPct = 18`
4. 周期 B：更新 `ShortProfitReducedPct = 18`（覆盖了 A 的更新）

但这种情况应该被**已存在订单检查**拦截。

### 可能原因 3: 订单成交前日志已写入

当前代码流程：
1. 下单成功
2. **写入日志**（包含 `target=18%`）
3. 更新内存状态
4. 如果此时崩溃，日志已写但状态未更新

恢复时：
- 从日志读取 `target=18%`
- 恢复 `ShortProfitReducedPct = 18`

这个应该是正常的。但如果有其他日志（如 `profit_reduce_close` 或 `profit_drawdown_close`）在之后写入，可能会导致状态被重置。

## 解决方案

### 方案 1: 增强日志和状态一致性（推荐）

当前已有的逻辑：
- ✅ 下单前检查已存在订单
- ✅ 下单后更新状态
- ✅ 启动时从日志恢复状态

**需要增强的部分**：

#### 1.1 添加详细的调试日志

帮助诊断问题发生时的状态：

```go
logger.Infof("[Grid] Profit-reduce %s: profitPct=%.2f%% targetStep=%.0f%% alreadyReduced=%.0f%% step=%.0f%%",
    info.side, profitPct, targetReducePct, alreadyReduced, step)

if targetReducePct <= alreadyReduced {
    logger.Infof("[Grid] Profit-reduce %s: skipping — already reduced at %.0f%% (current target %.0f%%)",
        info.side, alreadyReduced, targetReducePct)
    continue
}
```

#### 1.2 记录状态更新日志

确认状态是否正确更新：

```go
logger.Infof("[Grid] Profit-reduce %s: state updated from %.0f%% to %.0f%%", 
    info.side, oldPct, newPct)
```

#### 1.3 启动时记录恢复状态

在 `RestoreProfitReduceProgress()` 中添加：

```go
logger.Infof("[Grid] Restored %s profit-reduce progress from log: %.0f%% (orderID=%s, time=%s)", 
    side, targetPct, reduceEntry.OrderID, reduceEntry.CreatedAt)
```

### 方案 2: 在日志中添加 step 级别记录（备选）

除了记录 `target=18%`，还记录是第几个 step：

```go
// 日志格式变更：
// 当前: "pos=100.8 target=18% closeAll=false"
// 新增: "pos=100.8 target=18% step=3 closeAll=false"
stepNumber := int(targetReducePct / step)
fmt.Sprintf("pos=%.4f target=%.0f%% step=%d closeAll=%v", 
    info.size, a.targetReducePct, stepNumber, a.closeAll)
```

恢复时可以更精确地判断是否在同一 step。

### 方案 3: 强制状态持久化（备选）

每次更新 `ProfitReducedPct` 后，强制写入数据库或配置文件：

```go
at.gridState.mu.Lock()
at.gridState.ShortProfitReducedPct = newPct
at.gridState.mu.Unlock()

// 持久化到数据库（新增）
at.store.Grid().SaveGridState(at.id, at.gridState)
```

但这会增加数据库操作，可能影响性能。

## 已实现的修复

### 1. 增强日志输出

**文件**: `trader/auto_trader_grid.go`

**修改位置**: 约 1260-1280 行

```go
alreadyReduced := at.gridState.LongProfitReducedPct
if info.side == "short" {
    alreadyReduced = at.gridState.ShortProfitReducedPct
}
targetReducePct := math.Floor(profitPct/step) * step

// 新增：详细调试日志
logger.Infof("[Grid] Profit-reduce %s: profitPct=%.2f%% targetStep=%.0f%% alreadyReduced=%.0f%% step=%.0f%%",
    info.side, profitPct, targetReducePct, alreadyReduced, step)

if targetReducePct <= alreadyReduced {
    // 新增：跳过时记录原因
    logger.Infof("[Grid] Profit-reduce %s: skipping — already reduced at %.0f%% (current target %.0f%%)",
        info.side, alreadyReduced, targetReducePct)
    continue
}
```

### 2. 记录状态更新

**修改位置**: 约 1400-1425 行

```go
at.gridState.mu.Lock()
if a.closeAll {
    // ... closeAll 逻辑
    logger.Infof("[Grid] Profit-reduce %s: state updated to 0%% (closeAll)", info.side)
} else {
    newPct := a.targetReducePct
    oldPct := at.gridState.LongProfitReducedPct
    if info.side == "short" {
        oldPct = at.gridState.ShortProfitReducedPct
    }
    if info.side == "long" {
        at.gridState.LongProfitReducedPct = newPct
    } else {
        at.gridState.ShortProfitReducedPct = newPct
    }
    // 新增：记录状态变化
    logger.Infof("[Grid] Profit-reduce %s: state updated from %.0f%% to %.0f%%", 
        info.side, oldPct, newPct)
}
at.gridState.mu.Unlock()
```

### 3. 增强测试用例

**文件**: `trader/profit_reduce_duplicate_test.go`

新增测试：
- `TestProfitReduceSameStepNoRetrigger`: 验证同一阶梯不重复触发
- `TestProfitReduceNextStep`: 验证下一阶梯正确触发

## 验证方法

### 1. 查看日志输出

下次触发减仓时，应该看到：

```
[Grid] Profit-reduce check: short entry=50.00 mark=60.42 upl=107.00 margin=500.00 profit=21.40%
[Grid] Profit-reduce short: profitPct=21.40% targetStep=18% alreadyReduced=18% step=6%
[Grid] Profit-reduce short: skipping — already reduced at 18% (current target 18%)
```

如果看到：
```
[Grid] Profit-reduce short: profitPct=21.40% targetStep=18% alreadyReduced=0% step=6%
```

说明状态丢失了（`alreadyReduced=0%`），需要检查：
- 交易员是否重启过
- 日志恢复逻辑是否正常

### 2. 检查状态恢复

启动交易员时，查看：

```
[Grid] Restored short profit-reduce progress from log: 18%
```

如果没有这条日志，说明恢复逻辑未执行。

### 3. 单元测试

```bash
go test -v ./trader -run TestProfitReduceSameStepNoRetrigger
```

预期输出：
```
=== RUN   TestProfitReduceSameStepNoRetrigger
--- PASS: TestProfitReduceSameStepNoRetrigger (0.00s)
```

## 临时解决方案（用户侧）

在修复完成前，你可以：

### 方案 A: 手动重置状态（API）

如果发现重复触发，可以通过 API 重置状态：

```bash
# 重置 short 的利润减仓进度
curl -X POST http://localhost:8080/api/v1/traders/{id}/profit-reduce/reset \
  -H "Content-Type: application/json" \
  -d '{"side": "short"}'
```

### 方案 B: 调整步长配置

增大 `profit_reduce_step_pct`，减少触发频率：

```yaml
# 当前可能是 6%
profit_reduce_step_pct: 6

# 调整为 10%
profit_reduce_step_pct: 10
```

这样阶梯变为 10%, 20%, 30%...，触发频率降低。

### 方案 C: 监控并手动干预

在看到重复触发时：
1. 立即取消多余的减仓单
2. 记录时间和日志
3. 反馈给开发团队

## 测试计划

### 1. 本地测试

```bash
# 编译
go build .

# 运行单元测试
go test -v ./trader -run TestProfitReduce

# 启动交易员（测试环境）
# 配置 profit_reduce_step_pct: 6
# 手动制造场景：持仓盈利 18% → 21% → 24%
# 观察日志输出
```

### 2. 预期日志输出

#### 场景 1: 首次触发（18%）

```
[Grid] Profit-reduce check: short profit=18.6%
[Grid] Profit-reduce short: profitPct=18.60% targetStep=18% alreadyReduced=0% step=6%
[Grid] Profit-reduce: short reducing 1.8 (target=18%)
[Grid] Profit-reduce short: state updated from 0% to 18%
```

#### 场景 2: 同一阶梯不触发（21%）

```
[Grid] Profit-reduce check: short profit=21.4%
[Grid] Profit-reduce short: profitPct=21.40% targetStep=18% alreadyReduced=18% step=6%
[Grid] Profit-reduce short: skipping — already reduced at 18% (current target 18%)
```

#### 场景 3: 下一阶梯触发（25%）

```
[Grid] Profit-reduce check: short profit=25.2%
[Grid] Profit-reduce short: profitPct=25.20% targetStep=24% alreadyReduced=18% step=6%
[Grid] Profit-reduce: short reducing 2.4 (target=24%)
[Grid] Profit-reduce short: state updated from 18% to 24%
```

## 后续优化

### 短期（本周）
1. ✅ 添加详细日志
2. ✅ 增强测试覆盖
3. ⏳ 收集用户日志反馈
4. ⏳ 确认根本原因

### 中期（本月）
1. 如果确认是状态丢失问题，添加状态持久化
2. 如果确认是并发问题，添加更严格的锁机制
3. 优化状态恢复逻辑，添加更多边界情况处理

### 长期（季度）
1. 重构状态管理，使用专门的状态存储
2. 添加状态审计日志
3. 实现状态快照和恢复机制

## 文件清单

### 代码修改
```
trader/auto_trader_grid.go           - 添加详细日志
trader/profit_reduce_duplicate_test.go  - 增强测试用例
```

### 文档
```
BUGFIX_PROFIT_REDUCE_RETRIGGER.md   - 本文档
```

## 下一步行动

1. **立即**: 部署增强日志的版本
2. **观察**: 收集详细日志，确认根本原因
3. **反馈**: 当再次发生时，提供完整日志（包括启动日志和触发日志）
4. **修复**: 根据日志分析，实施针对性修复

---

**创建时间**: 2026-06-24  
**状态**: 🟡 诊断阶段 - 已添加详细日志  
**下一步**: 等待用户日志反馈，确认根本原因

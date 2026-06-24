# 🐛 紧急修复: K线事件无法触发 AI 周期

## 问题概述

**严重程度**: 🔴 严重  
**影响范围**: 所有网格策略交易员  
**症状**: 启动后只执行一次 AI 决策，后续 K线收盘不再触发

## 快速诊断

如果你的交易员出现以下情况，说明遇到了这个 bug：

```
✅ 启动时执行了一次 AI 决策
❌ 15分钟后（K线收盘）没有再执行
❌ 日志中没有 "K-line close event" 或 "Fallback timer" 消息
```

## 核心问题

1. **事件监听 goroutine 缺少定时器降级** - 只监听 WS 事件，WS 失败时永远阻塞
2. **主循环降级逻辑有缺陷** - 周期不匹配，触发条件不合理
3. **WebSocket 失败无降级标记** - 启动失败只记录警告，没有触发降级

## 修复方案

在 `trader/auto_trader.go` 的 AI 周期监听 goroutine 中添加独立的降级定时器：

```go
go func() {
    // 创建降级定时器（使用 AITriggerTf 周期）
    fallbackTicker := time.NewTicker(triggerPeriod)
    defer fallbackTicker.Stop()
    
    for {
        select {
        case <-at.wsGridCycleCh:
            // WebSocket 事件（优先）
            logger.Infof("[Grid] 🔔 K-line close event received")
            at.RunGridCycle()
            
        case <-fallbackTicker.C:
            // 降级定时器
            lastKline := time.Unix(0, atomic.LoadInt64(&at.wsLastKlineClose))
            if lastKline.IsZero() || time.Since(lastKline) > triggerPeriod+time.Minute {
                logger.Infof("[Grid] ⏰ Fallback timer triggered")
                at.RunGridCycle()
            }
            
        case <-at.stopMonitorCh:
            return
        }
    }
}()
```

## 修复效果

### 修复前
```
00:00  启动 ✅
00:15  K线收盘 ❌ 不触发
00:30  K线收盘 ❌ 不触发
00:45  K线收盘 ❌ 不触发
```

### 修复后
```
00:00  启动 ✅
00:15  K线收盘 ✅ WS 触发 或 定时器降级触发
00:30  K线收盘 ✅ 持续触发
00:45  K线收盘 ✅ 持续触发
```

## 升级指南

### 1. 更新代码

```bash
git pull origin main
```

### 2. 重启交易员

```bash
# Docker 部署
docker compose restart

# 或手动部署
./nofx
```

### 3. 验证修复

启动后观察日志，应该看到：

```
✅ [Grid] AI cycle monitor started: WS trigger=15m, fallback timer=15m
```

然后每 15 分钟（或你配置的周期）看到以下之一：

```
✅ [Grid] 🔔 K-line close event received, executing AI cycle
或
✅ [Grid] ⏰ Fallback timer: no WS events yet, executing AI cycle
```

## 配置检查

确保你的策略配置中设置了 K线周期：

```json
{
  "grid_config": {
    "ai_trigger_tf": "15m"  // 设置你想要的周期
  }
}
```

如果未设置，系统会使用 `scan_interval` 作为降级默认值。

## 回滚方案

如果升级后出现问题，可以临时回滚：

```bash
git checkout <previous-commit>
docker compose restart
```

但建议先检查日志排查问题，因为修复本身向后兼容。

## 技术细节

- **修改文件**: `trader/auto_trader.go`
- **修改位置**: 约 542-600 行
- **关键改动**: 在事件监听 goroutine 中添加 `fallbackTicker`
- **向后兼容**: ✅ 完全兼容现有配置

## 支持

如有问题，请：
1. 查看详细文档: `docs/bugfix-ws-event-not-triggering.md`
2. 检查日志输出
3. 提交 GitHub Issue

---

**修复时间**: 2026-06-24  
**优先级**: P0 (最高)  
**建议**: 立即升级

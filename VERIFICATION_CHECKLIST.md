# ✅ WebSocket 事件修复验证清单

## 部署前检查

- [x] 代码修复完成 (`trader/auto_trader.go`)
- [x] 添加 fallbackTicker 到事件监听 goroutine
- [x] 简化主循环定时器逻辑
- [x] 添加详细日志输出
- [x] 文档编写完成
- [ ] 代码审查通过
- [ ] 编译测试通过

## 编译验证

```bash
# 1. 编译检查
cd /Users/drk/nofx
go build .

# 预期: 编译成功，无错误
```

## 功能测试

### 测试 1: WebSocket 正常工作

```bash
# 配置
{
  "grid_config": {
    "symbol": "HYPEUSDT",
    "ai_trigger_tf": "5m"
  }
}

# 步骤
1. 启动交易员
2. 观察日志 5 分钟

# 预期日志
✅ [Grid] AI cycle monitor started: WS trigger=5m, fallback timer=5m
✅ [Grid] OKX WS started for HYPEUSDT
✅ [Grid] event-driven mode: AI cycle on 5m kline close
⏰ 5分钟后
✅ [Grid] 🔔 K-line close event received, executing AI cycle
✅ 🤖 [Grid] Calling AI for grid decisions...

# 验证点
□ 看到 "AI cycle monitor started" 消息
□ 看到 "OKX WS started" 消息
□ 5 分钟后看到 "🔔 K-line close event received"
□ AI 决策成功执行
```

### 测试 2: WebSocket 启动失败

```bash
# 配置（使用错误的 API 凭证或断网）
{
  "grid_config": {
    "symbol": "HYPEUSDT",
    "ai_trigger_tf": "5m"
  }
}

# 步骤
1. 断网或配置错误凭证
2. 启动交易员
3. 观察日志 5 分钟

# 预期日志
✅ [Grid] AI cycle monitor started: WS trigger=5m, fallback timer=5m
⚠️ [Grid] OKX WS start failed (falling back to REST): ...
⏰ 5分钟后
✅ [Grid] ⏰ Fallback timer: no WS events yet, executing AI cycle
✅ 🤖 [Grid] Calling AI for grid decisions...

# 验证点
□ 看到 "AI cycle monitor started" 消息
□ 看到 "OKX WS start failed" 警告
□ 5 分钟后看到 "⏰ Fallback timer: no WS events yet"
□ AI 决策通过定时器触发
```

### 测试 3: 不同 K线周期

```bash
# 测试配置 1: 1 分钟
ai_trigger_tf: "1m"
预期: 每 1 分钟触发

# 测试配置 2: 15 分钟
ai_trigger_tf: "15m"
预期: 每 15 分钟触发

# 测试配置 3: 30 分钟
ai_trigger_tf: "30m"
预期: 每 30 分钟触发

# 测试配置 4: 未设置（使用 scan_interval）
ai_trigger_tf: ""
scan_interval: "20m"
预期: 每 20 分钟触发

# 验证点
□ 各周期触发频率正确
□ 日志显示正确的 trigger period
```

### 测试 4: WebSocket 中途断开

```bash
# 步骤
1. 正常启动（WS 工作）
2. 等待第一次 WS 触发
3. 手动断开网络
4. 观察 15-20 分钟

# 预期日志
00:00 ✅ [Grid] 🔔 K-line close event received  (WS 正常)
00:05 ⚠️ [OKX WS] connection lost, reconnecting...
00:05 ✅ [Grid] ⏰ Fallback timer: WS silent for 5m  (跳过，太短)
00:10 ✅ [Grid] ⏰ Fallback timer: WS silent for 10m  (跳过)
00:15 ✅ [Grid] ⏰ Fallback timer: WS silent for 15m  (跳过)
00:20 ✅ [Grid] ⏰ Fallback timer: WS silent for 20m  (触发！)

# 验证点
□ WS 断开后系统继续运行
□ 约 16-20 分钟后定时器接管
□ 不需要重启即可恢复
```

## 性能测试

```bash
# 指标监控（运行 24 小时）

1. CPU 使用率
   □ 无异常增加
   
2. 内存使用
   □ 无内存泄漏
   
3. API 调用频率
   □ WS 模式: 1-2 次/周期（主要是 GetOpenOrders）
   □ 降级模式: 5-10 次/周期（REST 轮询）
   
4. AI 调用频率
   □ 严格按照 ai_trigger_tf 周期
   □ 无重复调用
```

## 回归测试

```bash
# 确保未破坏现有功能

□ T-Trade 扫描正常工作
□ 利润减仓正常触发
□ 持仓/订单变动事件正常
□ 网格暂停/恢复功能正常
□ 风险控制检查正常
□ 订单同步正常
```

## 日志审查

### 必须出现的日志

```bash
# 启动时
✅ [Grid] AI cycle monitor started: WS trigger=XX, fallback timer=XX

# WebSocket 成功
✅ [Grid] OKX WS started for SYMBOL
✅ [Grid] event-driven mode: AI cycle on XX kline close

# WebSocket 失败
⚠️ [Grid] OKX WS start failed (falling back to REST)

# 触发时（二选一）
✅ [Grid] 🔔 K-line close event received, executing AI cycle
或
✅ [Grid] ⏰ Fallback timer: no WS events yet, executing AI cycle
或
✅ [Grid] ⏰ Fallback timer: WS silent for XXm, executing AI cycle
```

### 不应出现的日志

```bash
❌ 长时间无 AI 决策日志（超过 2 个周期）
❌ panic 或 fatal 错误
❌ goroutine 泄漏警告
```

## 边界条件测试

### 极端配置

```bash
# 测试 1: 最短周期
ai_trigger_tf: "1m"
□ 每分钟正常触发

# 测试 2: 最长周期
ai_trigger_tf: "4h"
□ 每 4 小时触发

# 测试 3: 零配置
ai_trigger_tf: ""
scan_interval: 0
□ 使用默认值（不崩溃）
```

### 并发安全

```bash
# 测试同时运行多个交易员
□ 创建 5 个不同的网格交易员
□ 同时启动
□ 观察 1 小时
□ 验证每个交易员独立触发
□ 无竞态条件
```

## 用户验证

### 已知问题用户

```bash
# 联系报告问题的用户
□ 提供修复版本
□ 请求验证是否修复
□ 收集反馈
```

### 新用户测试

```bash
# 邀请测试用户
□ 全新部署
□ 不同配置组合
□ 24 小时运行
□ 收集使用体验
```

## 部署策略

### 灰度发布

```bash
阶段 1: 测试环境（1-2 天）
□ 部署到测试服务器
□ 运行完整测试套件
□ 修复发现的问题

阶段 2: 金丝雀发布（1-2 天）
□ 10% 用户升级
□ 监控错误率
□ 监控 AI 调用频率

阶段 3: 全量发布
□ 100% 用户升级
□ 持续监控 7 天
□ 准备回滚方案
```

## 回滚计划

```bash
# 如果发现严重问题

1. 立即回滚命令
   git revert <commit-hash>
   docker compose restart

2. 通知用户
   - 发布回滚公告
   - 说明原因
   - 提供临时解决方案

3. 问题修复
   - 分析失败原因
   - 完善测试用例
   - 重新修复和测试
```

## 成功标准

```bash
✅ 所有测试场景通过
✅ 无性能退化
✅ 日志输出正确
✅ 用户反馈积极
✅ 无严重 bug 报告
✅ 运行稳定 7 天以上
```

## 文档更新

```bash
□ 更新 README.md (如需要)
□ 更新 CHANGELOG.md
□ 创建迁移指南
□ 更新 API 文档 (如需要)
□ 发布 Release Notes
```

## 签署

```
测试人员: _________________  日期: ______
审查人员: _________________  日期: ______
发布人员: _________________  日期: ______
```

---

**注意**: 
- 所有 ✅ 项必须完成才能部署到生产环境
- 发现任何问题立即停止发布
- 保持回滚方案随时可用

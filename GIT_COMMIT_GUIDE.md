# 🚀 Git 提交指南

## 今日修复提交步骤

### 方案 1: 分开提交（推荐）

更清晰的提交历史，便于追踪和回滚。

```bash
cd /Users/drk/nofx

# ===== Commit 1: 利润减仓重复下单修复 =====
git add trader/auto_trader_grid.go
git add trader/profit_reduce_duplicate_test.go
git add docs/bugfix-profit-reduce-duplicate.md
git add docs/diagrams/profit-reduce-fix.md
git add BUGFIX_SUMMARY.md
git commit -F COMMIT_MESSAGE.txt

# ===== Commit 2: WebSocket K线事件修复 =====
git add trader/auto_trader.go
git add docs/bugfix-ws-event-not-triggering.md
git add docs/architecture/EVENT_DRIVEN_GRID.md
git add docs/diagrams/ws-event-bug-fix.md
git add BUGFIX_WS_EVENT.md
git add VERIFICATION_CHECKLIST.md
git commit -F COMMIT_MESSAGE_WS.txt

# ===== Commit 3: 文档汇总 =====
git add BUGFIX_COMPILE_ERROR.md
git add DAILY_FIXES_2026-06-24.md
git add GIT_COMMIT_GUIDE.md
git commit -m "docs: add daily fixes summary and commit guide"

# ===== 推送到远程 =====
git push origin main
```

### 方案 2: 合并提交

更快速，但提交历史不够详细。

```bash
cd /Users/drk/nofx

# 添加所有修改
git add trader/auto_trader.go
git add trader/auto_trader_grid.go
git add trader/profit_reduce_duplicate_test.go
git add docs/
git add *.md

# 使用合并提交信息
git commit -m "fix(grid): fix profit-reduce duplicates + WS event not triggering + compile error

- Fix profit-reduce placing duplicate orders
- Fix WebSocket K-line event not triggering AI cycle
- Fix OpenOrder.ReduceOnly compile error

See DAILY_FIXES_2026-06-24.md for details."

# 推送
git push origin main
```

---

## 提交前检查清单

### 代码检查
```bash
# 1. 确认编译通过
go build .

# 2. 运行测试
go test ./trader/profit_reduce_duplicate_test.go -v

# 3. 格式化代码
go fmt ./...

# 4. 检查语法
go vet ./...
```

### Git 检查
```bash
# 1. 查看修改状态
git status

# 2. 查看具体改动
git diff

# 3. 查看暂存的文件
git diff --cached
```

---

## 推送后验证

### GitHub Actions
```bash
# 1. 访问 GitHub Actions 页面
# https://github.com/de-rk/nofx/actions

# 2. 检查最新 workflow run
# 预期: ✅ 所有检查通过

# 3. 如果失败，查看日志
# 点击失败的 job → 查看详细错误
```

### Docker 构建
```bash
# 1. 本地测试 Docker 构建
docker compose -f docker-compose.yml build

# 2. 预期: 构建成功
# 3. 如果失败，检查 Dockerfile 和依赖
```

---

## 回滚步骤

如果推送后发现问题：

### 方案 1: Revert（推荐）
保留错误提交的历史记录。

```bash
# 回滚最近一次提交
git revert HEAD

# 回滚最近 3 次提交
git revert HEAD~2..HEAD

# 推送回滚
git push origin main
```

### 方案 2: Reset（危险）
完全删除错误提交（仅用于未推送的提交）。

```bash
# ⚠️ 警告: 仅在未推送前使用

# 撤销最近一次提交，保留修改
git reset --soft HEAD~1

# 撤销最近一次提交，丢弃修改
git reset --hard HEAD~1
```

---

## 常见问题

### Q: 提交信息写错了怎么办？

```bash
# 修改最近一次提交信息（未推送）
git commit --amend -m "新的提交信息"

# 修改最近一次提交信息（已推送）
git commit --amend -m "新的提交信息"
git push --force origin main  # ⚠️ 谨慎使用
```

### Q: 忘记添加文件了怎么办？

```bash
# 添加遗漏的文件到最近一次提交（未推送）
git add forgotten_file.go
git commit --amend --no-edit

# 已推送的话，建议创建新的提交
git add forgotten_file.go
git commit -m "chore: add missing file"
git push origin main
```

### Q: 提交到错误的分支了怎么办？

```bash
# 1. 创建新分支保存当前提交
git branch wrong-branch

# 2. 回到正确分支
git checkout main

# 3. Cherry-pick 需要的提交
git cherry-pick <commit-hash>

# 4. 推送
git push origin main

# 5. 删除错误分支（可选）
git branch -D wrong-branch
```

---

## 最佳实践

### 提交信息规范

遵循 Conventional Commits 规范：

```
<type>(<scope>): <subject>

<body>

<footer>
```

**Type**:
- `fix`: Bug 修复
- `feat`: 新功能
- `docs`: 文档更新
- `style`: 代码格式（不影响功能）
- `refactor`: 重构（不修复 bug，不添加功能）
- `test`: 测试相关
- `chore`: 构建/工具相关

**示例**:
```
fix(grid): prevent duplicate profit-reduce orders

Added check for existing reduce orders before placing new ones.
Uses heuristic based on order direction and price proximity.

Fixes #123
```

### 提交频率

- ✅ 每个独立功能一个提交
- ✅ 每个 bug 修复一个提交
- ✅ 相关的修改可以合并提交
- ❌ 不要把无关的修改放在一起
- ❌ 不要提交半完成的功能

### 代码审查

在推送前：
1. 自己审查一遍代码
2. 确认没有调试代码
3. 确认没有敏感信息
4. 确认测试通过
5. 确认文档更新

---

## 快速命令参考

```bash
# 查看状态
git status

# 查看改动
git diff
git diff --cached

# 添加文件
git add <file>
git add .

# 提交
git commit -m "message"
git commit -F file

# 推送
git push origin main

# 拉取
git pull origin main

# 查看日志
git log --oneline
git log --graph --oneline --all

# 回滚
git revert HEAD
git reset --soft HEAD~1

# 分支
git branch
git checkout -b new-branch
git merge branch-name

# 暂存
git stash
git stash pop
```

---

**提示**: 使用 `git add -p` 可以交互式选择要暂存的改动，适合精细化提交。

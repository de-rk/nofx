#!/bin/bash

# 修复验证测试脚本
# 用法: ./test-fixes.sh

set -e  # 遇到错误立即退出

echo "════════════════════════════════════════════════════════"
echo "  NOFX 修复验证测试脚本 - 2026-06-24"
echo "════════════════════════════════════════════════════════"
echo ""

# 颜色定义
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

success() {
    echo -e "${GREEN}✅ $1${NC}"
}

error() {
    echo -e "${RED}❌ $1${NC}"
}

warning() {
    echo -e "${YELLOW}⚠️  $1${NC}"
}

info() {
    echo -e "ℹ️  $1"
}

# 检查是否在正确的目录
if [ ! -f "main.go" ]; then
    error "请在项目根目录运行此脚本"
    exit 1
fi

echo "📋 开始验证..."
echo ""

# ============================================================================
# 测试 1: 编译检查
# ============================================================================
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "🔨 测试 1: 编译检查"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

info "正在编译项目..."
if go build -o /tmp/nofx-test . 2>&1 | tee /tmp/build.log; then
    success "编译成功"
    rm -f /tmp/nofx-test
else
    error "编译失败"
    cat /tmp/build.log
    exit 1
fi
echo ""

# ============================================================================
# 测试 2: 代码格式检查
# ============================================================================
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "🎨 测试 2: 代码格式检查"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

info "检查代码格式..."
if [ -n "$(gofmt -l trader/auto_trader.go trader/auto_trader_grid.go 2>&1)" ]; then
    warning "代码格式需要调整"
    info "运行: go fmt ./..."
else
    success "代码格式正确"
fi
echo ""

# ============================================================================
# 测试 3: 语法检查
# ============================================================================
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "🔍 测试 3: 语法检查"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

info "运行 go vet..."
if go vet ./trader/... 2>&1 | tee /tmp/vet.log; then
    success "语法检查通过"
else
    warning "发现潜在问题，请查看详情"
    cat /tmp/vet.log
fi
echo ""

# ============================================================================
# 测试 4: 单元测试
# ============================================================================
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "🧪 测试 4: 单元测试"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

if [ -f "trader/profit_reduce_duplicate_test.go" ]; then
    info "运行利润减仓重复测试..."
    if go test -v ./trader/profit_reduce_duplicate_test.go ./trader/auto_trader_grid.go 2>&1; then
        success "单元测试通过"
    else
        error "单元测试失败"
        exit 1
    fi
else
    warning "未找到测试文件，跳过"
fi
echo ""

# ============================================================================
# 测试 5: 关键函数检查
# ============================================================================
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "🔎 测试 5: 关键函数检查"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

info "检查 checkProfitReduce 函数..."
if grep -q "GetOpenOrders" trader/auto_trader_grid.go; then
    success "✓ 包含订单检查逻辑"
else
    error "✗ 缺少订单检查逻辑"
    exit 1
fi

if grep -q "priceDiff.*math.Abs" trader/auto_trader_grid.go; then
    success "✓ 包含价格差计算"
else
    error "✗ 缺少价格差计算"
    exit 1
fi

info "检查 AI 周期监听 goroutine..."
if grep -q "fallbackTicker" trader/auto_trader.go; then
    success "✓ 包含降级定时器"
else
    error "✗ 缺少降级定时器"
    exit 1
fi

if grep -q "wsGridCycleCh" trader/auto_trader.go; then
    success "✓ 包含 WS 事件通道"
else
    error "✗ 缺少 WS 事件通道"
    exit 1
fi
echo ""

# ============================================================================
# 测试 6: 日志输出检查
# ============================================================================
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "📝 测试 6: 日志输出检查"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

info "检查关键日志输出..."
if grep -q "AI cycle monitor started" trader/auto_trader.go; then
    success "✓ 包含启动日志"
else
    warning "✗ 缺少启动日志"
fi

if grep -q "K-line close event received" trader/auto_trader.go; then
    success "✓ 包含 WS 事件日志"
else
    warning "✗ 缺少 WS 事件日志"
fi

if grep -q "Fallback timer" trader/auto_trader.go; then
    success "✓ 包含降级日志"
else
    warning "✗ 缺少降级日志"
fi

if grep -q "skipping.*reduce.*order.*exists" trader/auto_trader_grid.go; then
    success "✓ 包含跳过重复日志"
else
    warning "✗ 缺少跳过重复日志"
fi
echo ""

# ============================================================================
# 测试 7: 文档完整性检查
# ============================================================================
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "📚 测试 7: 文档完整性检查"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

docs=(
    "docs/bugfix-profit-reduce-duplicate.md"
    "docs/bugfix-ws-event-not-triggering.md"
    "docs/diagrams/profit-reduce-fix.md"
    "docs/diagrams/ws-event-bug-fix.md"
    "BUGFIX_SUMMARY.md"
    "BUGFIX_WS_EVENT.md"
    "BUGFIX_COMPILE_ERROR.md"
    "VERIFICATION_CHECKLIST.md"
    "COMMIT_MESSAGE.txt"
    "COMMIT_MESSAGE_WS.txt"
    "DAILY_FIXES_2026-06-24.md"
)

missing_docs=0
for doc in "${docs[@]}"; do
    if [ -f "$doc" ]; then
        success "✓ $doc"
    else
        error "✗ $doc 缺失"
        missing_docs=$((missing_docs + 1))
    fi
done

if [ $missing_docs -eq 0 ]; then
    success "所有文档完整"
else
    warning "$missing_docs 个文档缺失"
fi
echo ""

# ============================================================================
# 测试总结
# ============================================================================
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "📊 测试总结"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

success "所有测试完成！"
echo ""
info "下一步操作:"
echo "  1. 查看测试结果"
echo "  2. 如果所有测试通过，执行: ./git-commit.sh"
echo "  3. 推送到远程: git push origin main"
echo "  4. 观察 GitHub Actions 构建结果"
echo ""

# ============================================================================
# Git 状态显示
# ============================================================================
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "📦 Git 状态"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
git status --short
echo ""

echo "════════════════════════════════════════════════════════"
echo "  测试完成 ✅"
echo "════════════════════════════════════════════════════════"

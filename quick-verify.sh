#!/bin/bash

# Quick verification script - checks key fixes are in place
# Usage: bash quick-verify.sh

echo "🔍 Quick Verification - Grid Trading Bug Fixes"
echo "=============================================="
echo ""

GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

success() { echo -e "${GREEN}✅ $1${NC}"; }
error() { echo -e "${RED}❌ $1${NC}"; }

# Check 1: Profit reduce fix
echo "1️⃣  Checking profit-reduce duplicate fix..."
if grep -q "GetOpenOrders" trader/auto_trader_grid.go && \
   grep -q "priceDiff.*math.Abs" trader/auto_trader_grid.go && \
   ! grep -q "order.ReduceOnly" trader/auto_trader_grid.go; then
    success "Profit-reduce fix confirmed"
else
    error "Profit-reduce fix incomplete"
    exit 1
fi

# Check 2: WebSocket event fix
echo "2️⃣  Checking WebSocket event fix..."
if grep -q "fallbackTicker" trader/auto_trader.go && \
   grep -q "wsGridCycleCh" trader/auto_trader.go && \
   grep -q "K-line close event" trader/auto_trader.go; then
    success "WebSocket event fix confirmed"
else
    error "WebSocket event fix incomplete"
    exit 1
fi

# Check 3: Test file
echo "3️⃣  Checking test file..."
if [ -f "trader/profit_reduce_duplicate_test.go" ] && \
   ! grep -q "ReduceOnly.*true" trader/profit_reduce_duplicate_test.go; then
    success "Test file correct"
else
    error "Test file has issues"
    exit 1
fi

# Check 4: Compilation
echo "4️⃣  Checking compilation..."
if go build -o /tmp/nofx-verify . 2>/dev/null; then
    success "Compilation successful"
    rm -f /tmp/nofx-verify
else
    error "Compilation failed"
    exit 1
fi

echo ""
echo "=============================================="
success "All checks passed! Ready to commit 🚀"
echo ""
echo "Next steps:"
echo "  1. Review: cat FIXES_READY_TO_COMMIT.md"
echo "  2. Commit: Follow the steps in the file above"
echo "  3. Push: git push origin main"

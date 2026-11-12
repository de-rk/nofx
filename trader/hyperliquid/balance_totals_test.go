package hyperliquid

import (
	"math"
	"testing"
)

// TestComputeBalanceTotalsDefault pins the classic (non-unified) model: spot and
// perps are separate accounts, so both count.
func TestComputeBalanceTotalsDefault(t *testing.T) {
	got := computeBalanceTotals(
		1000, 50, // perp equity, unrealized
		900,    // withdrawable
		500, 0, // spot total, hold
		200, 20, // xyz equity, unrealized
		false,
	)

	if want := 1000.0 - 50 + 500 + (200 - 20); got.WalletBalance != want {
		t.Errorf("wallet balance = %.4f, want %.4f", got.WalletBalance, want)
	}
	if want := 1000 + 500 + 200.0; got.Equity != want {
		t.Errorf("equity = %.4f, want %.4f", got.Equity, want)
	}
	if got.Available != 900 {
		t.Errorf("available = %.4f, want the withdrawable value 900", got.Available)
	}

	// Downstream derives equity as wallet + unrealized; it must line up.
	if derived := got.WalletBalance + 50 + 20; math.Abs(derived-got.Equity) > 1e-9 {
		t.Errorf("wallet+upnl = %.4f, equity = %.4f, want equal", derived, got.Equity)
	}
}

// TestComputeBalanceTotalsUnifiedDoesNotDoubleCount is the regression that
// matters: when the perp summary already includes spot USDC as collateral, the
// old code added spot on top and reported roughly double the real equity.
func TestComputeBalanceTotalsUnifiedDoesNotDoubleCount(t *testing.T) {
	// 1500 perp equity already contains the 500 spot USDC.
	got := computeBalanceTotals(1500, 0, 1200, 500, 0, 0, 0, true)

	if got.Equity != 1500 {
		t.Errorf("equity = %.4f, want 1500 (spot must not be added twice)", got.Equity)
	}
	if got.Available != 1200 {
		t.Errorf("available = %.4f, want 1200 (withdrawable already covers spot)", got.Available)
	}
}

// TestComputeBalanceTotalsUnifiedSpotOnly covers accounts where the perp summary
// reads zero and the whole balance sits in spot.
func TestComputeBalanceTotalsUnifiedSpotOnly(t *testing.T) {
	got := computeBalanceTotals(0, 0, 0, 500, 120, 0, 0, true)

	if got.Equity != 500 {
		t.Errorf("equity = %.4f, want 500", got.Equity)
	}
	// 120 is reserved by open orders, so only 380 is usable.
	if got.Available != 380 {
		t.Errorf("available = %.4f, want 380", got.Available)
	}
}

// TestComputeBalanceTotalsUnifiedKeepsXyzSeparate: xyz dex is a distinct account
// even for unified users, so it still adds on top.
func TestComputeBalanceTotalsUnifiedKeepsXyzSeparate(t *testing.T) {
	got := computeBalanceTotals(1500, 30, 1000, 500, 0, 200, 20, true)

	if want := 1500.0 + 200; got.Equity != want {
		t.Errorf("equity = %.4f, want %.4f", got.Equity, want)
	}
	// wallet = (1500-30) + (200-20) = 1650
	if want := 1650.0; math.Abs(got.WalletBalance-want) > 1e-9 {
		t.Errorf("wallet balance = %.4f, want %.4f", got.WalletBalance, want)
	}
	if derived := got.WalletBalance + 30 + 20; math.Abs(derived-got.Equity) > 1e-9 {
		t.Errorf("wallet+upnl = %.4f, equity = %.4f, want equal", derived, got.Equity)
	}
}

// TestComputeBalanceTotalsUnifiedShortSession guards the case where the perp
// summary is larger than spot because of unrealized gains: equity should track
// the perp summary, not the smaller spot figure.
func TestComputeBalanceTotalsUnifiedPerpExceedsSpot(t *testing.T) {
	got := computeBalanceTotals(2000, 300, 1700, 500, 0, 0, 0, true)

	if got.Equity != 2000 {
		t.Errorf("equity = %.4f, want 2000", got.Equity)
	}
	if want := 1700.0; got.WalletBalance != want {
		t.Errorf("wallet balance = %.4f, want %.4f", got.WalletBalance, want)
	}
}

package okx

import "testing"

func TestPositionSideFromOKX(t *testing.T) {
	tests := []struct {
		name      string
		posSide   string
		contracts float64
		want      string
	}{
		{name: "hedge long", posSide: "long", contracts: 1.6, want: "long"},
		{name: "hedge short", posSide: "short", contracts: 1.6, want: "short"},
		{name: "net long", posSide: "net", contracts: 1.6, want: "long"},
		{name: "net short", posSide: "net", contracts: -1.6, want: "short"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := positionSideFromOKX(tt.posSide, tt.contracts); got != tt.want {
				t.Errorf("positionSideFromOKX(%q, %.1f) = %q, want %q", tt.posSide, tt.contracts, got, tt.want)
			}
		})
	}
}

func TestOrderPosSide(t *testing.T) {
	netTrader := &OKXTrader{positionMode: "net_mode"}
	if got := netTrader.orderPosSide("short"); got != "short" {
		t.Errorf("short order posSide = %q, want short", got)
	}
	if got := netTrader.orderPosSide("long"); got != "long" {
		t.Errorf("long order posSide = %q, want long", got)
	}

	hedgeTrader := &OKXTrader{positionMode: "long_short_mode"}
	if got := hedgeTrader.orderPosSide("short"); got != "short" {
		t.Errorf("hedge-mode short order posSide = %q, want short", got)
	}
}

func TestHedgeProtectionDirection(t *testing.T) {
	tests := []struct {
		positionSide string
		wantSide     string
		wantPosSide  string
		wantErr      bool
	}{
		{positionSide: "LONG", wantSide: "sell", wantPosSide: "long"},
		{positionSide: "short", wantSide: "buy", wantPosSide: "short"},
		{positionSide: "net", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.positionSide, func(t *testing.T) {
			side, posSide, err := hedgeProtectionDirection(tt.positionSide)
			if (err != nil) != tt.wantErr {
				t.Fatalf("hedgeProtectionDirection(%q) error = %v, wantErr %v", tt.positionSide, err, tt.wantErr)
			}
			if !tt.wantErr && (side != tt.wantSide || posSide != tt.wantPosSide) {
				t.Fatalf("hedgeProtectionDirection(%q) = (%q, %q), want (%q, %q)", tt.positionSide, side, posSide, tt.wantSide, tt.wantPosSide)
			}
		})
	}
}

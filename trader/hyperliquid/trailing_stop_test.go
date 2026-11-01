package hyperliquid

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/sonirico/go-hyperliquid"
	"github.com/vmihailenco/msgpack/v5"
)

// TestTrailingStopActionMsgpackShape pins the L1 wire encoding of the native
// trailingStop action. Hyperliquid hashes the msgpack bytes, so a field order
// or tag drift silently produces a signature the exchange rejects.
func TestTrailingStopActionMsgpackShape(t *testing.T) {
	pct := "1.500%"
	action := trailingStopActionWire{
		Type:        "trailingStop",
		Asset:       3,
		IsBuy:       false,
		Sz:          "0.01",
		ReduceOnly:  true,
		Retracement: trailingStopRetracementWire{Pct: &pct},
	}

	var buf bytes.Buffer
	enc := msgpack.NewEncoder(&buf)
	enc.UseCompactInts(true)
	if err := enc.Encode(action); err != nil {
		t.Fatalf("msgpack encode failed: %v", err)
	}
	t.Logf("msgpack (no activationPx) = %s", hex.EncodeToString(buf.Bytes()))

	// Round-trip into a map so the key set is inspectable.
	var decoded map[string]any
	if err := msgpack.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("msgpack decode failed: %v", err)
	}
	keys := make([]string, 0, len(decoded))
	for k := range decoded {
		keys = append(keys, k)
	}
	t.Logf("keys = %v", keys)

	for _, want := range []string{"type", "asset", "isBuy", "sz", "reduceOnly", "retracement"} {
		if _, ok := decoded[want]; !ok {
			t.Errorf("missing expected msgpack key %q", want)
		}
	}
	// activationPx has no omitempty on purpose: the schema is "String | null",
	// and null means "activate immediately". A strict deserialiser could reject
	// a missing key, so the key is always present. Pin that behaviour.
	v, ok := decoded["activationPx"]
	if !ok {
		t.Errorf("activationPx must always be present (schema is String|null), keys=%v", keys)
	}
	if v != nil {
		t.Errorf("activationPx = %v, want nil when no activation price", v)
	}
	if decoded["type"] != "trailingStop" {
		t.Errorf("type = %v, want trailingStop", decoded["type"])
	}

	retracement, ok := decoded["retracement"].(map[string]any)
	if !ok {
		t.Fatalf("retracement is not a map: %#v", decoded["retracement"])
	}
	if retracement["pct"] != "1.500%" {
		t.Errorf("retracement.pct = %v, want 1.500%%", retracement["pct"])
	}
	if _, ok := retracement["px"]; ok {
		t.Errorf("retracement.px must be omitted when nil, got %v", retracement["px"])
	}

	// Now with an activation price populated.
	px := "71000.5"
	action.ActivationPx = &px
	buf.Reset()
	enc = msgpack.NewEncoder(&buf)
	enc.UseCompactInts(true)
	if err := enc.Encode(action); err != nil {
		t.Fatalf("msgpack encode (activationPx) failed: %v", err)
	}
	t.Logf("msgpack (with activationPx) = %s", hex.EncodeToString(buf.Bytes()))

	decoded = nil
	if err := msgpack.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("msgpack decode (activationPx) failed: %v", err)
	}
	if decoded["activationPx"] != "71000.5" {
		t.Errorf("activationPx = %v, want 71000.5", decoded["activationPx"])
	}
}

// TestTrailingStopActionSignable proves the SDK's SignL1Action accepts the
// custom action type. actionHash panics on unencodable input, so this also
// covers the serialisation path the real request takes.
func TestTrailingStopActionSignable(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	pct := "2.500%"
	action := trailingStopActionWire{
		Type:        "trailingStop",
		Asset:       3,
		IsBuy:       false,
		Sz:          "0.01",
		ReduceOnly:  true,
		Retracement: trailingStopRetracementWire{Pct: &pct},
	}

	nonce := int64(1700000000000)
	sig, err := hyperliquid.SignL1Action(key, action, "", nonce, nil, true)
	if err != nil {
		t.Fatalf("SignL1Action rejected the trailingStop action: %v", err)
	}
	if sig.R == "" || sig.S == "" || sig.V == 0 {
		t.Fatalf("incomplete signature: %+v", sig)
	}
	t.Logf("signature ok: r=%s s=%s v=%d", sig.R, sig.S, sig.V)
}

// setProbeEnv controls the probe switch and restores the original value after.
// These tests mutate process env, so they must not run in parallel.
func setProbeEnv(t *testing.T, value string, present bool) {
	t.Helper()
	old, had := os.LookupEnv("HYPERLIQUID_PROBE_TRAILING_STOP")
	t.Cleanup(func() {
		if had {
			os.Setenv("HYPERLIQUID_PROBE_TRAILING_STOP", old)
		} else {
			os.Unsetenv("HYPERLIQUID_PROBE_TRAILING_STOP")
		}
	})
	if present {
		os.Setenv("HYPERLIQUID_PROBE_TRAILING_STOP", value)
	} else {
		os.Unsetenv("HYPERLIQUID_PROBE_TRAILING_STOP")
	}
}

func TestShouldProbeTrailingStop(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		present bool
		testnet bool
		want    bool
	}{
		{"mainnet defaults off (real order)", "", false, false, false},
		{"testnet defaults on", "", false, true, true},
		{"explicit on overrides mainnet default", "1", true, false, true},
		{"explicit true", "true", true, false, true},
		{"explicit yes", "YES", true, false, true},
		{"explicit off overrides testnet default", "0", true, true, false},
		{"empty value is not empty-unset", "", true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setProbeEnv(t, tc.value, tc.present)
			if got := shouldProbeTrailingStop(tc.testnet); got != tc.want {
				t.Errorf("shouldProbeTrailingStop(testnet=%v) = %v, want %v (env=%q present=%v)",
					tc.testnet, got, tc.want, tc.value, tc.present)
			}
		})
	}
}

// The probe places a real order, so when it is disabled it must not run at all
// — and must not leave a cached result that would short-circuit SetTrailingStop.
func TestProbeTrailingStopSupportIfEnabledSkipsWhenDisabled(t *testing.T) {
	setProbeEnv(t, "0", true)
	tr := &HyperliquidTrader{ctx: context.Background(), isTestnet: false}

	if tr.ProbeTrailingStopSupportIfEnabled() {
		t.Fatal("probe must report it did not run when disabled")
	}
	if tr.trailingProbe != nil {
		t.Fatalf("probe ran despite being disabled, cached: %+v", tr.trailingProbe)
	}
}

func TestTruncateForLog(t *testing.T) {
	if got := truncateForLog("short", 300); got != "short" {
		t.Errorf("got %q, want unchanged", got)
	}
	long := strings.Repeat("x", 500)
	got := truncateForLog(long, 300)
	if len(got) != 303 { // 300 + "..."
		t.Errorf("len = %d, want 303", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("must end with ellipsis, got %q", got)
	}
}

// A probe that ran and failed must stop us from sending an entry-side trailing
// stop that would fail anyway (and would waste an AI decision).
func TestSetTrailingStopShortCircuitsWhenProbeFailed(t *testing.T) {
	tr := &HyperliquidTrader{ctx: context.Background(), isTestnet: true}
	tr.trailingProbe = &trailingProbeResult{Supported: false, Detail: "probe order rejected: status=err"}

	err := tr.SetTrailingStop("BTCUSDT", "LONG", 0.01, 0, 1.5)
	if err == nil {
		t.Fatal("expected an error when the probe proved support is missing")
	}
	if !strings.Contains(err.Error(), "native trailing stop unavailable") {
		t.Errorf("unexpected error: %v", err)
	}
}

// An inconclusive probe is NOT evidence of missing support, so it must fall
// through and still attempt the order (here it fails later, on missing meta,
// which proves it was not short-circuited).
func TestSetTrailingStopProceedsWhenProbeInconclusive(t *testing.T) {
	tr := &HyperliquidTrader{ctx: context.Background(), isTestnet: true}
	tr.trailingProbe = &trailingProbeResult{Inconclusive: true, Detail: "no open perp position"}

	err := tr.SetTrailingStop("BTCUSDT", "LONG", 0.01, 0, 1.5)
	if err == nil {
		t.Fatal("expected an error from the downstream path")
	}
	if strings.Contains(err.Error(), "native trailing stop unavailable") {
		t.Errorf("inconclusive probe must not short-circuit, got: %v", err)
	}
}

// TestProtectionOrderKind pins how protective orders are told apart from grid
// legs. Misclassifying a resting grid leg as protection would tear down the
// grid every time TP/SL is reset — the bug this classification exists to fix.
func TestProtectionOrderKind(t *testing.T) {
	const mark = 50000.0

	cases := []struct {
		name  string
		order hyperliquid.FrontendOpenOrder
		want  string
	}{
		{
			name:  "stop loss: trigger below mark, sell side",
			order: hyperliquid.FrontendOpenOrder{Coin: "BTC", IsTrigger: true, ReduceOnly: true, OrderType: "Stop Market", TriggerPx: 45000, Side: hyperliquid.OrderSideAsk},
			want:  "sl",
		},
		{
			name:  "take profit: trigger above mark, sell side",
			order: hyperliquid.FrontendOpenOrder{Coin: "BTC", IsTrigger: true, ReduceOnly: true, OrderType: "Take Profit Market", TriggerPx: 55000, Side: hyperliquid.OrderSideAsk},
			want:  "tp",
		},
		{
			name:  "trailing stop counts as protection",
			order: hyperliquid.FrontendOpenOrder{Coin: "BTC", IsTrigger: true, ReduceOnly: true, OrderType: "Trailing Stop", TriggerPx: 48000, Side: hyperliquid.OrderSideAsk},
			want:  "sl",
		},
		{
			name:  "buy-side stop above mark (closing a short)",
			order: hyperliquid.FrontendOpenOrder{Coin: "BTC", IsTrigger: true, ReduceOnly: true, OrderType: "Stop Market", TriggerPx: 55000, Side: hyperliquid.OrderSideBid},
			want:  "sl",
		},
		{
			name:  "grid leg: resting limit, never a trigger",
			order: hyperliquid.FrontendOpenOrder{Coin: "BTC", IsTrigger: false, ReduceOnly: false, OrderType: "Limit", LimitPx: 49000, Side: hyperliquid.OrderSideBid},
			want:  "",
		},
		{
			name:  "trigger but not reduce-only is not protection",
			order: hyperliquid.FrontendOpenOrder{Coin: "BTC", IsTrigger: true, ReduceOnly: false, OrderType: "Stop Market", TriggerPx: 45000, Side: hyperliquid.OrderSideAsk},
			want:  "",
		},
		{
			name:  "unlabelled trigger falls back to protection",
			order: hyperliquid.FrontendOpenOrder{Coin: "BTC", IsTrigger: true, ReduceOnly: true, OrderType: "", TriggerPx: 0, Side: hyperliquid.OrderSideAsk},
			want:  "sl",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := protectionOrderKind(tc.order, mark); got != tc.want {
				t.Errorf("protectionOrderKind = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestReserveNonceMonotonic guards the nonce handoff with the SDK counter.
// Reusing a nonce gets the action rejected by the exchange.
func TestReserveNonceMonotonic(t *testing.T) {
	var tr HyperliquidTrader
	var prev int64
	for i := 0; i < 200; i++ {
		n := tr.reserveNonce()
		if n <= prev {
			t.Fatalf("nonce did not increase: got %d after %d", n, prev)
		}
		prev = n
	}
}

// TestTrailingStopJSONBody pins the HTTP body shape sent to POST /exchange.
func TestTrailingStopJSONBody(t *testing.T) {
	pct := "1.200%"
	px := "70000"
	action := trailingStopActionWire{
		Type:         "trailingStop",
		Asset:        3,
		IsBuy:        true,
		Sz:           "0.5",
		ReduceOnly:   true,
		Retracement:  trailingStopRetracementWire{Pct: &pct},
		ActivationPx: &px,
	}
	payload := map[string]any{
		"action":    action,
		"nonce":     int64(1700000000000),
		"signature": map[string]any{"r": "0xr", "s": "0xs", "v": 27},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	t.Logf("body = %s", string(b))

	var check struct {
		Action struct {
			Type        string `json:"type"`
			Asset       int    `json:"asset"`
			IsBuy       bool   `json:"isBuy"`
			Sz          string `json:"sz"`
			ReduceOnly  bool   `json:"reduceOnly"`
			Retracement struct {
				Pct *string `json:"pct"`
			} `json:"retracement"`
			ActivationPx *string `json:"activationPx"`
		} `json:"action"`
		Nonce int64 `json:"nonce"`
	}
	if err := json.Unmarshal(b, &check); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if check.Action.Type != "trailingStop" || check.Action.Asset != 3 || !check.Action.ReduceOnly {
		t.Errorf("unexpected action: %+v", check.Action)
	}
	if check.Action.Retracement.Pct == nil || *check.Action.Retracement.Pct != "1.200%" {
		t.Errorf("unexpected retracement: %+v", check.Action.Retracement)
	}
	if check.Action.ActivationPx == nil || *check.Action.ActivationPx != "70000" {
		t.Errorf("unexpected activationPx: %v", check.Action.ActivationPx)
	}
}

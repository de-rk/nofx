package hyperliquid

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/sonirico/go-hyperliquid"
)

// Mainnet payload shape that crashed go-hyperliquid v0.26.0:
// the token array is dense (positions 0..1) while spot pairs reference token ids
// (3) that are far beyond it. The SDK indexes the array by token id, so the
// lookup "index out of range [3] with length 2" panicked during NewExchange.
const spotMetaSparseIDs = `{
  "universe": [
    {"name": "USDC", "tokens": [0, 1], "index": 0, "isCanonical": true},
    {"name": "@1", "tokens": [1, 0], "index": 1, "isCanonical": true},
    {"name": "@2", "tokens": [3, 0], "index": 2, "isCanonical": false},
    {"name": "@3", "tokens": [9, 0], "index": 3, "isCanonical": false}
  ],
  "tokens": [
    {"name": "USDC", "szDecimals": 8, "weiDecimals": 8, "index": 0, "tokenId": "0xaaa", "isCanonical": true},
    {"name": "PURR", "szDecimals": 1, "weiDecimals": 1, "index": 1, "tokenId": "0xbbb", "isCanonical": true},
    {"name": "HYPE", "szDecimals": 2, "weiDecimals": 2, "index": 3, "tokenId": "0xccc", "isCanonical": true}
  ]
}`

func decodeSpotMeta(t *testing.T, payload string) *hyperliquid.SpotMeta {
	t.Helper()
	var spotMeta hyperliquid.SpotMeta
	if err := json.Unmarshal([]byte(payload), &spotMeta); err != nil {
		t.Fatalf("decode spot meta: %v", err)
	}
	return &spotMeta
}

func TestNormalizeSpotMetaMakesTokensIndexAddressable(t *testing.T) {
	normalized := normalizeSpotMeta(decodeSpotMeta(t, spotMetaSparseIDs))

	// Padding must cover the highest token id so id-based lookups stay in range.
	if got, want := len(normalized.Tokens), 4; got != want {
		t.Fatalf("len(Tokens) = %d, want %d", got, want)
	}

	// Same expression the SDK uses, which used to panic.
	for _, pair := range normalized.Universe {
		tokenID := pair.Tokens[0]
		if tokenID < 0 || tokenID >= len(normalized.Tokens) {
			t.Fatalf("pair %s token id %d out of range", pair.Name, tokenID)
		}
		if normalized.Tokens[tokenID].Name == "" {
			t.Fatalf("pair %s resolves to an empty token at id %d", pair.Name, tokenID)
		}
	}

	if got := normalized.Tokens[3].SzDecimals; got != 2 {
		t.Errorf("HYPE szDecimals = %d, want 2", got)
	}
	if got := normalized.Tokens[1].Name; got != "PURR" {
		t.Errorf("token 1 = %q, want PURR", got)
	}
}

func TestNormalizeSpotMetaDropsPairsWithUnknownTokenID(t *testing.T) {
	normalized := normalizeSpotMeta(decodeSpotMeta(t, spotMetaSparseIDs))

	// "@3" references token id 9 with no metadata; keeping it would hand the
	// order path a zero szDecimals precision.
	for _, pair := range normalized.Universe {
		if pair.Name == "@3" {
			t.Fatalf("pair with unknown token id must be dropped")
		}
	}
	if got, want := len(normalized.Universe), 3; got != want {
		t.Fatalf("len(Universe) = %d, want %d", got, want)
	}
}

func TestNormalizeSpotMetaHandlesEmptyAndNil(t *testing.T) {
	for name, input := range map[string]*hyperliquid.SpotMeta{
		"nil":   nil,
		"empty": {},
	} {
		normalized := normalizeSpotMeta(input)
		if normalized == nil {
			t.Fatalf("%s: got nil", name)
		}
		if len(normalized.Tokens) != 0 || len(normalized.Universe) != 0 {
			t.Fatalf("%s: expected empty metadata, got %d tokens / %d pairs",
				name, len(normalized.Tokens), len(normalized.Universe))
		}
	}
}

// The SDK panics instead of returning an error when its metadata fetch fails.
// Restoring traders runs on the main goroutine at startup, so an unrecovered
// panic is a crash loop, not a failed trader.
func TestNewExchangeSafeConvertsPanicToError(t *testing.T) {
	privateKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Port 1 refuses connections immediately, so the SDK's internal Meta() fails
	// and panics.
	exchange, err := newExchangeSafe(ctx, privateKey, "http://127.0.0.1:1", "0xabc", nil)
	if err == nil {
		t.Fatalf("expected error, got exchange %v", exchange)
	}
	if exchange != nil {
		t.Fatalf("expected nil exchange on failure")
	}
	if !strings.Contains(err.Error(), "hyperliquid SDK initialization failed") {
		t.Fatalf("error does not mention SDK initialization: %v", err)
	}
}

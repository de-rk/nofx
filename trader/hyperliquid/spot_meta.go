package hyperliquid

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"nofx/logger"

	"github.com/sonirico/go-hyperliquid"
)

// SDK clients panic (not error) on several exchange-state conditions, and the
// panic happens inside NewExchange -> NewInfo, i.e. before we ever get a value
// to check. A single such panic while restoring traders from the store takes the
// whole process down on every restart (crash loop), so every SDK construction
// goes through here and a panic becomes an ordinary error.
//
// Known trigger: go-hyperliquid v0.26.0 builds the spot asset table with
//
//	info.assetToDecimal[asset] = spotMeta.Tokens[spotInfo.Tokens[0]].SzDecimals
//
// treating Tokens as if it were indexed by token id. Token ids are stable and
// grow well past the array length (mainnet: len(Tokens)=515, max id=1334, and
// 42 spot pairs reference an id >= 515), so the lookup panics with
// "index out of range [610] with length 515" for every account.
func newExchangeSafe(
	ctx context.Context,
	privateKey *ecdsa.PrivateKey,
	apiURL string,
	walletAddr string,
	spotMeta *hyperliquid.SpotMeta,
) (exchange *hyperliquid.Exchange, err error) {
	defer func() {
		if r := recover(); r != nil {
			exchange = nil
			err = fmt.Errorf("hyperliquid SDK initialization failed: %v", r)
		}
	}()

	return hyperliquid.NewExchange(
		ctx,
		privateKey,
		apiURL,
		nil,        // Meta is fetched by the SDK (safe: perp assets are index-addressed)
		"",         // vault address (empty for personal account)
		walletAddr, // account address that holds the funds
		spotMeta,
	), nil
}

// loadSpotMeta fetches spot metadata and rewrites it into the shape the SDK
// expects (Tokens addressable by token id). Returns nil when the metadata cannot
// be fetched or normalized: spot metadata is only needed for spot asset
// precision, and every call site here trades perps, so a missing table must not
// block the trader from loading.
func loadSpotMeta(ctx context.Context, apiURL string) *hyperliquid.SpotMeta {
	raw, err := fetchSpotMeta(ctx, apiURL)
	if err != nil {
		logger.Infof("⚠️ Failed to fetch Hyperliquid spot metadata (%v), continuing without spot asset table", err)
		return nil
	}

	normalized := normalizeSpotMeta(raw)
	if dropped := len(raw.Universe) - len(normalized.Universe); dropped > 0 {
		logger.Infof("⚠️ Dropped %d spot pairs with unknown token ids from Hyperliquid metadata", dropped)
	}
	return normalized
}

func fetchSpotMeta(ctx context.Context, apiURL string) (*hyperliquid.SpotMeta, error) {
	jsonBody, err := json.Marshal(map[string]string{"type": "spotMeta"})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL+"/info", bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var spotMeta hyperliquid.SpotMeta
	if err := json.Unmarshal(body, &spotMeta); err != nil {
		return nil, fmt.Errorf("failed to decode spot metadata: %w", err)
	}
	return &spotMeta, nil
}

// normalizeSpotMeta makes spotMeta.Tokens addressable by token id, which is how
// lookups inside the SDK index it. Tokens arrive as a dense array whose position
// is unrelated to the (sparse, monotonic) token id, so we re-place each token at
// its own id and drop pairs whose token id we have no metadata for instead of
// leaving them with a zero szDecimals.
func normalizeSpotMeta(raw *hyperliquid.SpotMeta) *hyperliquid.SpotMeta {
	if raw == nil {
		return &hyperliquid.SpotMeta{}
	}

	maxIndex := -1
	for _, token := range raw.Tokens {
		if token.Index > maxIndex {
			maxIndex = token.Index
		}
	}

	tokens := make([]hyperliquid.SpotTokenInfo, maxIndex+1)
	known := make([]bool, maxIndex+1)
	for _, token := range raw.Tokens {
		if token.Index < 0 {
			continue
		}
		tokens[token.Index] = token
		known[token.Index] = true
	}

	universe := make([]hyperliquid.SpotAssetInfo, 0, len(raw.Universe))
	for _, pair := range raw.Universe {
		if len(pair.Tokens) == 0 {
			continue
		}
		base := pair.Tokens[0]
		if base < 0 || base > maxIndex || !known[base] {
			continue
		}
		universe = append(universe, pair)
	}

	return &hyperliquid.SpotMeta{
		Universe: universe,
		Tokens:   tokens,
	}
}

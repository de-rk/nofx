package backtest

import (
	"math"
	"math/rand"
)

// paramBounds constrains the search space so perturbations never produce
// nonsensical configs (e.g. grid_count=1, leverage=0).
type paramBounds struct {
	GridCountMin, GridCountMax                           int
	ATRMultiplierMin, ATRMultiplierMax                   float64
	LeverageMin, LeverageMax                             int
	ProfitReduceStepMin, ProfitReduceStepMax             float64
	ProfitReduceMultiplierMin, ProfitReduceMultiplierMax float64
}

func defaultBounds() paramBounds {
	return paramBounds{
		GridCountMin: 10, GridCountMax: 50,
		ATRMultiplierMin: 1.0, ATRMultiplierMax: 10.0,
		LeverageMin: 1, LeverageMax: 10,
		ProfitReduceStepMin: 3, ProfitReduceStepMax: 20,
		ProfitReduceMultiplierMin: 0.05, ProfitReduceMultiplierMax: 1.0,
	}
}

var distributions = []string{"uniform", "gaussian", "pyramid"}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// neighbor proposes a randomly perturbed parameter set within bounds.
// Each call nudges 1-3 fields; distribution is occasionally flipped outright
// since it has no meaningful "distance" metric.
func neighbor(rng *rand.Rand, cur GridParams, b paramBounds) GridParams {
	next := cur.Clone()

	nudgeCount := 1 + rng.Intn(3)
	fields := rng.Perm(5)[:nudgeCount]

	for _, f := range fields {
		switch f {
		case 0:
			delta := rng.Intn(7) - 3 // -3..+3
			next.GridCount = clampInt(next.GridCount+delta, b.GridCountMin, b.GridCountMax)
		case 1:
			delta := (rng.Float64() - 0.5) * 2.0 // -1..+1
			next.ATRMultiplier = clampFloat(next.ATRMultiplier+delta, b.ATRMultiplierMin, b.ATRMultiplierMax)
		case 2:
			delta := rng.Intn(3) - 1 // -1..+1
			next.Leverage = clampInt(next.Leverage+delta, b.LeverageMin, b.LeverageMax)
		case 3:
			delta := (rng.Float64() - 0.5) * 4.0 // -2..+2
			next.ProfitReduceStepPct = clampFloat(next.ProfitReduceStepPct+delta, b.ProfitReduceStepMin, b.ProfitReduceStepMax)
		case 4:
			delta := (rng.Float64() - 0.5) * 0.2 // -0.1..+0.1
			next.ProfitReduceMultiplier = clampFloat(next.ProfitReduceMultiplier+delta, b.ProfitReduceMultiplierMin, b.ProfitReduceMultiplierMax)
		}
	}

	if rng.Float64() < 0.15 {
		next.Distribution = distributions[rng.Intn(len(distributions))]
	}

	return next
}

// AnnealConfig controls the search schedule.
type AnnealConfig struct {
	Iterations  int
	InitialTemp float64
	CoolingRate float64 // temp *= CoolingRate each iteration, e.g. 0.995
	Seed        int64
	// OnProgress, if set, is called after every iteration with the
	// 1-based iteration number and the best score found so far. Callers
	// (e.g. an SSE HTTP handler) use this to stream progress without the
	// annealing loop knowing anything about transport. Must return quickly —
	// it runs on the hot path between iterations.
	OnProgress func(iteration int, bestScore float64)
}

// AnnealResult is the best parameter set found and its evaluated outcome.
type AnnealResult struct {
	Best       GridParams
	BestResult SimResult
	History    []float64 // best score seen so far, per iteration (for a convergence log)
}

// Evaluate scores one parameter set against a backtest window. It's a
// function value so the caller can wrap Simulate with caching, walk-forward
// windows, or multiple symbols later without changing the annealing loop.
type Evaluate func(GridParams) SimResult

// Anneal runs simulated annealing: starting from `start`, repeatedly proposes
// a neighbor, and accepts it if it scores better, or — with probability
// exp((newScore-curScore)/temp) — even if it scores worse, so the search can
// escape local optima early on (temp high) while converging late (temp low).
func Anneal(start GridParams, eval Evaluate, cfg AnnealConfig) AnnealResult {
	b := defaultBounds()
	rng := rand.New(rand.NewSource(cfg.Seed))

	cur := start
	curResult := eval(cur)
	best := cur
	bestResult := curResult

	temp := cfg.InitialTemp
	history := make([]float64, 0, cfg.Iterations)

	for i := 0; i < cfg.Iterations; i++ {
		cand := neighbor(rng, cur, b)
		candResult := eval(cand)

		accept := candResult.Score >= curResult.Score
		if !accept && temp > 1e-9 {
			delta := candResult.Score - curResult.Score
			p := math.Exp(delta / temp)
			accept = rng.Float64() < p
		}

		if accept {
			cur, curResult = cand, candResult
			if curResult.Score > bestResult.Score {
				best, bestResult = cur, curResult
			}
		}

		history = append(history, bestResult.Score)
		temp *= cfg.CoolingRate

		if cfg.OnProgress != nil {
			cfg.OnProgress(i+1, bestResult.Score)
		}
	}

	return AnnealResult{Best: best, BestResult: bestResult, History: history}
}

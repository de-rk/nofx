package manager

import (
	"fmt"
	"nofx/logger"
	"nofx/market"
	"nofx/store"
	"sync"
	"time"
)

// StartHandoffBinding starts a polling monitor for one enabled binding.
func (tm *TraderManager) StartHandoffBinding(st *store.Store, binding *store.HandoffBinding) {
	if !binding.Enabled {
		return
	}
	tm.handoffMu.Lock()
	if _, exists := tm.handoffMonitors[binding.ID]; exists {
		tm.handoffMu.Unlock()
		return
	}
	tm.handoffMonitors[binding.ID] = struct{}{}
	tm.handoffMu.Unlock()
	go func() {
		tm.monitorHandoff(st, binding)
		tm.handoffMu.Lock()
		delete(tm.handoffMonitors, binding.ID)
		tm.handoffMu.Unlock()
	}()
}

// StartHandoffMonitoring starts polling monitors for all enabled bindings.
// Polling uses the trader adapter's market price so it works across exchanges.
func (tm *TraderManager) StartHandoffMonitoring(st *store.Store) {
	bindings, err := st.Handoff().ListAll()
	if err != nil {
		logger.Infof("⚠️ Failed to load handoff bindings: %v", err)
		return
	}
	for _, binding := range bindings {
		tm.StartHandoffBinding(st, binding)
	}
}

func (tm *TraderManager) monitorHandoff(st *store.Store, binding *store.HandoffBinding) {
	interval := time.Second
	window := time.Duration(binding.WindowSeconds) * time.Second
	if window <= 0 {
		window = 3 * time.Minute
	}
	threshold := binding.ThresholdPct
	if threshold <= 0 {
		threshold = 3
	}
	for {
		current, err := st.Handoff().Get(binding.UserID, binding.ID)
		if err != nil || !current.Enabled || current.State != store.HandoffMonitoring {
			return
		}
		binding = current
		source, err := tm.GetTrader(binding.SourceTraderID)
		if err != nil {
			time.Sleep(10 * time.Second)
			continue
		}
		status := source.GetStatus()
		running, _ := status["is_running"].(bool)
		symbol, _ := status["grid_symbol"].(string)
		if !running || symbol == "" {
			time.Sleep(10 * time.Second)
			continue
		}
		price, err := source.GetUnderlyingTrader().GetMarketPrice(symbol)
		if err == nil && price > 0 {
			stateKey := binding.ID + ":" + symbol
			change, ready := handoffPrices.Add(stateKey, market.PricePoint{Timestamp: time.Now(), Price: price}, window)
			if ready && abs(change) >= threshold {
				reference := handoffPrices.LastReference(stateKey)
				claimed, claimErr := st.Handoff().Claim(binding.ID, time.Now(), price, reference, change)
				if claimErr != nil {
					logger.Infof("⚠️ Failed to claim handoff %s: %v", binding.ID, claimErr)
				} else if claimed {
					go tm.executeHandoff(st, binding, symbol, price, change)
					return
				}
			}
		}
		time.Sleep(interval)
	}
}

func (tm *TraderManager) executeHandoff(st *store.Store, binding *store.HandoffBinding, symbol string, price, change float64) {
	execution := &store.HandoffExecution{
		ID:             fmt.Sprintf("handoff_%d", time.Now().UnixNano()),
		BindingID:      binding.ID,
		TriggerAt:      time.Now(),
		LatestPrice:    price,
		ReferencePrice: handoffPrices.LastReference(binding.ID + ":" + symbol),
		ChangePct:      change,
		Phase:          store.HandoffTriggered,
	}
	if err := st.Handoff().CreateExecution(execution); err != nil {
		logger.Infof("⚠️ Failed to record handoff %s: %v", binding.ID, err)
	}

	setPhase := func(phase string, err error) bool {
		message := ""
		if err != nil {
			message = err.Error()
		}
		if setErr := st.Handoff().SetState(binding.ID, phase, message); setErr != nil {
			logger.Infof("⚠️ Failed to persist handoff %s state: %v", binding.ID, setErr)
		}
		if err != nil {
			logger.Infof("❌ Handoff %s failed at %s: %v", binding.ID, phase, err)
			return false
		}
		execution.Phase = phase
		return true
	}

	source, err := tm.GetTrader(binding.SourceTraderID)
	if err != nil || source == nil {
		setPhase(store.HandoffFailed, fmt.Errorf("source trader unavailable: %w", err))
		return
	}
	if !setPhase(store.HandoffPausingSource, nil) {
		return
	}
	if err := source.PauseGridForHandoff("emergency volatility handoff"); err != nil {
		setPhase(store.HandoffFailed, err)
		return
	}
	if !setPhase(store.HandoffCancelingOrders, nil) {
		return
	}
	direction := "long"
	if change < 0 {
		direction = "short"
	}
	if err := source.CancelGridOrdersByDirection(direction); err != nil {
		setPhase(store.HandoffFailed, err)
		return
	}
	if !setPhase(store.HandoffStartingTarget, nil) {
		return
	}
	target, err := tm.GetTrader(binding.TargetTraderID)
	if err != nil || target == nil {
		setPhase(store.HandoffFailed, fmt.Errorf("target trader unavailable: %w", err))
		return
	}
	if targetStatus := target.GetStatus(); targetStatus["is_running"] == true {
		setPhase(store.HandoffFailed, fmt.Errorf("target trader is already running"))
		return
	}
	if err := st.Trader().UpdateStatus(binding.UserID, binding.TargetTraderID, true); err != nil {
		setPhase(store.HandoffFailed, err)
		return
	}
	go func() {
		if err := target.Run(); err != nil {
			logger.Infof("❌ Handoff target %s failed: %v", binding.TargetTraderID, err)
			_ = st.Trader().UpdateStatus(binding.UserID, binding.TargetTraderID, false)
		}
	}()
	setPhase(store.HandoffCompleted, nil)
}

type handoffPriceBook struct {
	mu    sync.Mutex
	byKey map[string]*market.RollingChange
}

func newHandoffPriceBook() *handoffPriceBook {
	return &handoffPriceBook{byKey: make(map[string]*market.RollingChange)}
}

func (b *handoffPriceBook) Add(key string, point market.PricePoint, window time.Duration) (float64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	tracker := b.byKey[key]
	if tracker == nil {
		tracker = market.NewRollingChange(window)
		b.byKey[key] = tracker
	}
	return tracker.Add(point)
}

func (b *handoffPriceBook) LastReference(key string) float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if tracker := b.byKey[key]; tracker != nil {
		return tracker.LastReference()
	}
	return 0
}

var handoffPrices = newHandoffPriceBook()

func abs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

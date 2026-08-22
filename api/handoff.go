package api

import (
	"encoding/json"
	"net/http"
	"nofx/market"
	"nofx/store"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type HandoffRequest struct {
	SourceTraderID  string  `json:"source_trader_id" binding:"required"`
	TargetTraderID  string  `json:"target_trader_id" binding:"required"`
	Enabled         bool    `json:"enabled"`
	WindowSeconds   int     `json:"window_seconds"`
	ThresholdPct    float64 `json:"threshold_pct"`
	CooldownSeconds int     `json:"cooldown_seconds"`
}

func (s *Server) handleListHandoffs(c *gin.Context) {
	bindings, err := s.store.Handoff().List(c.GetString("user_id"))
	if err != nil {
		SafeInternalError(c, "Failed to list handoffs", err)
		return
	}
	c.JSON(http.StatusOK, bindings)
}

func (s *Server) handleCreateHandoff(c *gin.Context) {
	userID := c.GetString("user_id")
	var req HandoffRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		SafeBadRequest(c, "Invalid handoff configuration")
		return
	}
	binding, err := s.newHandoffBinding(userID, req)
	if err != nil {
		SafeBadRequest(c, err.Error())
		return
	}
	binding.ID = uuid.NewString()
	if err := s.store.Handoff().Create(binding); err != nil {
		SafeInternalError(c, "Failed to create handoff", err)
		return
	}
	s.traderManager.StartHandoffBinding(s.store, binding)
	c.JSON(http.StatusCreated, binding)
}

func (s *Server) handleUpdateHandoff(c *gin.Context) {
	userID := c.GetString("user_id")
	var req HandoffRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		SafeBadRequest(c, "Invalid handoff configuration")
		return
	}
	binding, err := s.newHandoffBinding(userID, req)
	if err != nil {
		SafeBadRequest(c, err.Error())
		return
	}
	binding.ID = c.Param("id")
	if err := s.store.Handoff().Update(binding); err != nil {
		SafeInternalError(c, "Failed to update handoff", err)
		return
	}
	s.traderManager.StartHandoffBinding(s.store, binding)
	c.JSON(http.StatusOK, binding)
}

func (s *Server) handleDeleteHandoff(c *gin.Context) {
	if err := s.store.Handoff().Delete(c.GetString("user_id"), c.Param("id")); err != nil {
		SafeInternalError(c, "Failed to delete handoff", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) newHandoffBinding(userID string, req HandoffRequest) (*store.HandoffBinding, error) {
	source, err := s.store.Trader().GetFullConfig(userID, req.SourceTraderID)
	if err != nil {
		return nil, err
	}
	target, err := s.store.Trader().GetFullConfig(userID, req.TargetTraderID)
	if err != nil {
		return nil, err
	}
	if source.Trader.ExchangeID != target.Trader.ExchangeID {
		return nil, errHandoff("source and target must use the same exchange account")
	}
	if !isGridStrategy(source.Strategy) {
		return nil, errHandoff("source trader must use a grid strategy")
	}
	if isGridStrategy(target.Strategy) {
		return nil, errHandoff("target trader must use an AI strategy")
	}
	gridConfig := strategyConfig(source.Strategy)
	targetConfig := strategyConfig(target.Strategy)
	if gridConfig == nil || gridConfig.GridConfig == nil || targetConfig == nil || targetConfig.CoinSource.SourceType != "static" || len(targetConfig.CoinSource.StaticCoins) != 1 {
		return nil, errHandoff("target trader must be a single-symbol static AI strategy")
	}
	gridSymbol := market.Normalize(gridConfig.GridConfig.Symbol)
	targetSymbol := market.Normalize(targetConfig.CoinSource.StaticCoins[0])
	if gridSymbol == "" || gridSymbol != targetSymbol {
		return nil, errHandoff("target static symbol must match the grid symbol")
	}
	if req.WindowSeconds == 0 {
		req.WindowSeconds = 180
	}
	if req.WindowSeconds < 60 || req.WindowSeconds > 900 {
		return nil, errHandoff("window_seconds must be between 60 and 900")
	}
	if req.ThresholdPct == 0 {
		req.ThresholdPct = 3
	}
	if req.ThresholdPct < 0.1 || req.ThresholdPct > 30 {
		return nil, errHandoff("threshold_pct must be between 0.1 and 30")
	}
	if req.CooldownSeconds == 0 {
		req.CooldownSeconds = 900
	}
	return &store.HandoffBinding{
		UserID: userID, SourceTraderID: req.SourceTraderID, TargetTraderID: req.TargetTraderID,
		Enabled: req.Enabled, WindowSeconds: req.WindowSeconds, ThresholdPct: req.ThresholdPct,
		CooldownSeconds: req.CooldownSeconds, State: store.HandoffMonitoring,
	}, nil
}

type errHandoff string

func (e errHandoff) Error() string { return string(e) }

func strategyConfig(strategy *store.Strategy) *store.StrategyConfig {
	if strategy == nil {
		return nil
	}
	var config store.StrategyConfig
	if json.Unmarshal([]byte(strategy.Config), &config) != nil {
		return nil
	}
	return &config
}

func isGridStrategy(strategy *store.Strategy) bool {
	config := strategyConfig(strategy)
	return config != nil && config.StrategyType == "grid_trading" && config.GridConfig != nil
}

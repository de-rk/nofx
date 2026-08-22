package store

import (
	"time"

	"gorm.io/gorm"
)

const (
	HandoffMonitoring            = "monitoring"
	HandoffTriggered             = "triggered"
	HandoffPausingSource         = "pausing_source"
	HandoffCancelingOrders       = "canceling_orders"
	HandoffClosingPositions      = "closing_positions"
	HandoffWaitingFlat           = "waiting_flat"
	HandoffStoppingSource        = "stopping_source"
	HandoffStartingTarget        = "starting_target"
	HandoffCompleted             = "completed"
	HandoffFailed                = "failed"
	HandoffManualIntervention    = "manual_intervention_required"
	HandoffMarketDataUnavailable = "market_data_unavailable"
)

// HandoffBinding configures an explicit grid-to-AI trader takeover.
type HandoffBinding struct {
	ID              string     `gorm:"primaryKey" json:"id"`
	UserID          string     `gorm:"column:user_id;not null;index" json:"user_id"`
	SourceTraderID  string     `gorm:"column:source_trader_id;not null;uniqueIndex" json:"source_trader_id"`
	TargetTraderID  string     `gorm:"column:target_trader_id;not null;uniqueIndex" json:"target_trader_id"`
	Enabled         bool       `gorm:"column:enabled;default:false" json:"enabled"`
	WindowSeconds   int        `gorm:"column:window_seconds;default:180" json:"window_seconds"`
	ThresholdPct    float64    `gorm:"column:threshold_pct;default:3" json:"threshold_pct"`
	CooldownSeconds int        `gorm:"column:cooldown_seconds;default:900" json:"cooldown_seconds"`
	State           string     `gorm:"column:state;not null;default:monitoring;index" json:"state"`
	LastTriggeredAt *time.Time `gorm:"column:last_triggered_at" json:"last_triggered_at,omitempty"`
	LastError       string     `gorm:"column:last_error;default:''" json:"last_error,omitempty"`
	CreatedAt       time.Time  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

func (HandoffBinding) TableName() string { return "trader_handoff_bindings" }

// HandoffExecution records the trigger that started a takeover.
type HandoffExecution struct {
	ID             string     `gorm:"primaryKey" json:"id"`
	BindingID      string     `gorm:"column:binding_id;not null;index" json:"binding_id"`
	TriggerAt      time.Time  `gorm:"column:trigger_at;not null" json:"trigger_at"`
	LatestPrice    float64    `gorm:"column:latest_price" json:"latest_price"`
	ReferencePrice float64    `gorm:"column:reference_price" json:"reference_price"`
	ChangePct      float64    `gorm:"column:change_pct" json:"change_pct"`
	Phase          string     `gorm:"column:phase;not null" json:"phase"`
	RetryCount     int        `gorm:"column:retry_count;default:0" json:"retry_count"`
	LastError      string     `gorm:"column:last_error;default:''" json:"last_error,omitempty"`
	CompletedAt    *time.Time `gorm:"column:completed_at" json:"completed_at,omitempty"`
	CreatedAt      time.Time  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt      time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

func (HandoffExecution) TableName() string { return "trader_handoff_executions" }

type HandoffStore struct{ db *gorm.DB }

func NewHandoffStore(db *gorm.DB) *HandoffStore { return &HandoffStore{db: db} }

func (s *HandoffStore) initTables() error {
	return s.db.AutoMigrate(&HandoffBinding{}, &HandoffExecution{})
}

func (s *HandoffStore) List(userID string) ([]*HandoffBinding, error) {
	var bindings []*HandoffBinding
	err := s.db.Where("user_id = ?", userID).Order("created_at ASC").Find(&bindings).Error
	return bindings, err
}

func (s *HandoffStore) ListAll() ([]*HandoffBinding, error) {
	var bindings []*HandoffBinding
	err := s.db.Order("created_at ASC").Find(&bindings).Error
	return bindings, err
}

func (s *HandoffStore) Get(userID, id string) (*HandoffBinding, error) {
	var binding HandoffBinding
	if err := s.db.Where("id = ? AND user_id = ?", id, userID).First(&binding).Error; err != nil {
		return nil, err
	}
	return &binding, nil
}

func (s *HandoffStore) GetBySource(userID, sourceTraderID string) (*HandoffBinding, error) {
	var binding HandoffBinding
	if err := s.db.Where("user_id = ? AND source_trader_id = ?", userID, sourceTraderID).First(&binding).Error; err != nil {
		return nil, err
	}
	return &binding, nil
}

func (s *HandoffStore) Create(binding *HandoffBinding) error { return s.db.Create(binding).Error }

func (s *HandoffStore) Update(binding *HandoffBinding) error {
	return s.db.Model(&HandoffBinding{}).
		Where("id = ? AND user_id = ?", binding.ID, binding.UserID).
		Updates(map[string]interface{}{
			"source_trader_id": binding.SourceTraderID,
			"target_trader_id": binding.TargetTraderID,
			"enabled":          binding.Enabled,
			"window_seconds":   binding.WindowSeconds,
			"threshold_pct":    binding.ThresholdPct,
			"cooldown_seconds": binding.CooldownSeconds,
			"state":            HandoffMonitoring,
			"last_error":       "",
		}).Error
}

func (s *HandoffStore) Delete(userID, id string) error {
	return s.db.Where("id = ? AND user_id = ?", id, userID).Delete(&HandoffBinding{}).Error
}

// Claim atomically changes a monitoring binding into a triggered execution.
func (s *HandoffStore) Claim(bindingID string, when time.Time, latest, reference, change float64) (bool, error) {
	result := s.db.Model(&HandoffBinding{}).Where("id = ? AND enabled = ? AND state = ?", bindingID, true, HandoffMonitoring).
		Updates(map[string]interface{}{"state": HandoffTriggered, "last_triggered_at": when, "last_error": ""})
	return result.RowsAffected == 1, result.Error
}

func (s *HandoffStore) SetState(bindingID, state, lastError string) error {
	return s.db.Model(&HandoffBinding{}).Where("id = ?", bindingID).Updates(map[string]interface{}{"state": state, "last_error": lastError}).Error
}

func (s *HandoffStore) CreateExecution(execution *HandoffExecution) error {
	return s.db.Create(execution).Error
}

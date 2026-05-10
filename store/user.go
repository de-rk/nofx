package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"time"

	"gorm.io/gorm"
)

// UserStore user storage
type UserStore struct {
	db *gorm.DB
}

// OTPDeviceToken records a per-device OTP-skip token
type OTPDeviceToken struct {
	TokenHash string    `gorm:"primaryKey;column:token_hash" json:"-"`
	UserID    string    `gorm:"column:user_id;index;not null" json:"user_id"`
	ExpiresAt time.Time `gorm:"column:expires_at;index" json:"expires_at"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

func (OTPDeviceToken) TableName() string { return "otp_device_tokens" }

// hashDeviceToken returns the SHA-256 hex digest used as the primary key.
func hashDeviceToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// User user model
type User struct {
	ID           string    `gorm:"primaryKey" json:"id"`
	Email        string    `gorm:"uniqueIndex:idx_users_email;not null" json:"email"`
	PasswordHash string    `gorm:"column:password_hash;not null" json:"-"`
	OTPSecret    string    `gorm:"column:otp_secret" json:"-"`
	OTPVerified  bool      `gorm:"column:otp_verified;default:false" json:"otp_verified"`
	LastOTPAt    time.Time `gorm:"column:last_otp_at" json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (User) TableName() string { return "users" }

// GenerateOTPSecret generates OTP secret
func GenerateOTPSecret() (string, error) {
	secret := make([]byte, 20)
	_, err := rand.Read(secret)
	if err != nil {
		return "", err
	}
	return base32.StdEncoding.EncodeToString(secret), nil
}

// NewUserStore creates a new UserStore
func NewUserStore(db *gorm.DB) *UserStore {
	return &UserStore{db: db}
}

func (s *UserStore) initTables() error {
	// For PostgreSQL with existing table, skip AutoMigrate to avoid index conflicts
	if s.db.Dialector.Name() == "postgres" {
		var tableExists int64
		s.db.Raw(`SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'users'`).Scan(&tableExists)

		if tableExists > 0 {
			// Table exists - manually ensure all columns exist
			// Core columns (should already exist)
			s.db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS email TEXT NOT NULL DEFAULT ''`)
			s.db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS password_hash TEXT NOT NULL DEFAULT ''`)
			s.db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP`)
			s.db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP`)
			// OTP columns (added later)
			s.db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS otp_secret TEXT DEFAULT ''`)
			s.db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS otp_verified BOOLEAN DEFAULT FALSE`)
			s.db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS last_otp_at TIMESTAMP`)

			// Ensure unique index exists on email (don't care about the name)
			var indexExists int64
			s.db.Raw(`
				SELECT COUNT(*) FROM pg_indexes
				WHERE tablename = 'users' AND indexdef LIKE '%email%' AND indexdef LIKE '%UNIQUE%'
			`).Scan(&indexExists)

			if indexExists == 0 {
				s.db.Exec("CREATE UNIQUE INDEX idx_users_email ON users(email)")
			}

			// Ensure device-token table exists on postgres
			s.db.Exec(`CREATE TABLE IF NOT EXISTS otp_device_tokens (
				token_hash TEXT PRIMARY KEY,
				user_id TEXT NOT NULL,
				expires_at TIMESTAMP NOT NULL,
				created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
			)`)
			s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_otp_device_tokens_user_id ON otp_device_tokens(user_id)`)
			s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_otp_device_tokens_expires_at ON otp_device_tokens(expires_at)`)

			return nil
		}
	}
	if err := s.db.AutoMigrate(&User{}); err != nil {
		return err
	}
	return s.db.AutoMigrate(&OTPDeviceToken{})
}

// Create creates user
func (s *UserStore) Create(user *User) error {
	return s.db.Create(user).Error
}

// GetByEmail gets user by email
func (s *UserStore) GetByEmail(email string) (*User, error) {
	var user User
	err := s.db.Where("email = ?", email).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// GetByID gets user by ID
func (s *UserStore) GetByID(userID string) (*User, error) {
	var user User
	err := s.db.Where("id = ?", userID).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// Count returns the total number of users
func (s *UserStore) Count() (int, error) {
	var count int64
	err := s.db.Model(&User{}).Count(&count).Error
	return int(count), err
}

// GetAllIDs gets all user IDs
func (s *UserStore) GetAllIDs() ([]string, error) {
	var userIDs []string
	err := s.db.Model(&User{}).Order("id").Pluck("id", &userIDs).Error
	return userIDs, err
}

// UpdateOTPVerified updates OTP verification status
func (s *UserStore) UpdateOTPVerified(userID string, verified bool) error {
	return s.db.Model(&User{}).Where("id = ?", userID).Update("otp_verified", verified).Error
}

// UpdateLastOTPAt records the timestamp of a successful OTP verification
func (s *UserStore) UpdateLastOTPAt(userID string, t time.Time) error {
	return s.db.Model(&User{}).Where("id = ?", userID).Update("last_otp_at", t).Error
}

// CreateOTPDeviceToken generates a new per-device OTP-skip token valid for ttl.
// Returns the raw token to hand to the client; only its hash is stored.
func (s *UserStore) CreateOTPDeviceToken(userID string, ttl time.Duration) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	entry := &OTPDeviceToken{
		TokenHash: hashDeviceToken(token),
		UserID:    userID,
		ExpiresAt: time.Now().UTC().Add(ttl),
		CreatedAt: time.Now().UTC(),
	}
	if err := s.db.Create(entry).Error; err != nil {
		return "", err
	}
	// Opportunistic cleanup: drop expired rows on each create
	s.db.Where("expires_at < ?", time.Now().UTC()).Delete(&OTPDeviceToken{})
	return token, nil
}

// ValidateOTPDeviceToken returns true if the token belongs to userID and is not expired.
func (s *UserStore) ValidateOTPDeviceToken(userID, token string) bool {
	if token == "" {
		return false
	}
	var entry OTPDeviceToken
	err := s.db.Where("token_hash = ? AND user_id = ?", hashDeviceToken(token), userID).First(&entry).Error
	if err != nil {
		return false
	}
	return time.Now().UTC().Before(entry.ExpiresAt)
}

// RevokeOTPDeviceToken deletes a single device token (called on logout).
func (s *UserStore) RevokeOTPDeviceToken(token string) error {
	if token == "" {
		return nil
	}
	return s.db.Where("token_hash = ?", hashDeviceToken(token)).Delete(&OTPDeviceToken{}).Error
}

// RevokeAllOTPDeviceTokens deletes every device token for a user
// (called on password change / reset so stale devices stop bypassing OTP).
func (s *UserStore) RevokeAllOTPDeviceTokens(userID string) error {
	return s.db.Where("user_id = ?", userID).Delete(&OTPDeviceToken{}).Error
}

// UpdatePassword updates password
func (s *UserStore) UpdatePassword(userID, passwordHash string) error {
	return s.db.Model(&User{}).Where("id = ?", userID).Updates(map[string]interface{}{
		"password_hash": passwordHash,
		"updated_at":    time.Now().UTC(),
	}).Error
}

// EnsureAdmin ensures admin user exists
func (s *UserStore) EnsureAdmin() error {
	var count int64
	s.db.Model(&User{}).Where("id = ?", "admin").Count(&count)
	if count > 0 {
		return nil
	}
	return s.Create(&User{
		ID:           "admin",
		Email:        "admin@localhost",
		PasswordHash: "",
		OTPSecret:    "",
		OTPVerified:  true,
	})
}

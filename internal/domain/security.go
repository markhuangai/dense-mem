package domain

import "time"

const (
	SecurityBanSourceAuto   = "auto"
	SecurityBanSourceManual = "manual"
)

// SecuritySettings controls app-level auth failure tracking and IP bans.
type SecuritySettings struct {
	Enabled              bool
	FailureThreshold     int
	FailureWindowSeconds int
	BanDurationSeconds   int
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// SecurityIPFailure is the rolling auth-failure counter for one client IP.
type SecurityIPFailure struct {
	IP            string
	FailureCount  int
	FirstFailedAt time.Time
	LastFailedAt  time.Time
	LastReason    string
	LastSurface   string
	UpdatedAt     time.Time
}

// SecurityIPBan blocks requests from one normalized client IP.
type SecurityIPBan struct {
	IP           string
	Reason       string
	Source       string
	FailureCount int
	BannedAt     time.Time
	ExpiresAt    *time.Time
	LastFailedAt *time.Time
	Metadata     map[string]any
	CreatedAt    time.Time
	UpdatedAt    time.Time
	RevokedAt    *time.Time
}

func (b SecurityIPBan) ActiveAt(now time.Time) bool {
	if b.RevokedAt != nil {
		return false
	}
	if b.ExpiresAt == nil {
		return true
	}
	return b.ExpiresAt.After(now)
}

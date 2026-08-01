package domain

import (
	"time"

	"github.com/google/uuid"
)

type ControlAdminGroup struct {
	ID         uuid.UUID
	ProviderID uuid.UUID
	GroupID    string
	GroupName  string
	Enabled    bool
	RetiredAt  *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type ControlOAuthState struct {
	StateHash    string
	ProviderID   uuid.UUID
	PKCEVerifier string
	Nonce        string
	ExpiresAt    time.Time
	CreatedAt    time.Time
}

type ControlSession struct {
	SessionHash string
	IdentityID  uuid.UUID
	ProviderID  uuid.UUID
	GroupIDs    []string
	CSRFHash    string
	ExpiresAt   time.Time
	CreatedAt   time.Time
	LastSeenAt  time.Time
}

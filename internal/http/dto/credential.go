package dto

import (
	"encoding/json"
	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
)

func RejectMemoryBinding(value json.RawMessage) error {
	if len(value) == 0 {
		return nil
	}
	return httperr.New(httperr.VALIDATION_ERROR, "memory_binding is no longer supported; each credential has private memory, and remember.target_space selects private or team_shared for each write")
}

// CreateCredentialRequest represents a request to create a new API credential.
// Validation rules:
//   - Name: optional credential name
//   - Scopes: optional permission set; accepted values are read, write, and feedback:read
//   - RateLimit: optional rate limit per minute
//   - ExpiresAt: optional expiration time
type CreateCredentialRequest struct {
	Name          string          `json:"name" validate:"omitempty,min=1,max=100,notblank"`
	Scopes        *[]string       `json:"scopes,omitempty" validate:"omitempty,min=1,max=3,dive,notblank"`
	RateLimit     int             `json:"rate_limit"`
	ExpiresAt     *string         `json:"expires_at"`
	MemoryBinding json.RawMessage `json:"memory_binding,omitempty"`
}

// UpdateCredentialRequest represents a protected API request to update a member
// credential. Role changes are intentionally control-portal only.
type UpdateCredentialRequest struct {
	Name   string    `json:"name" validate:"omitempty,min=1,max=100,notblank"`
	Scopes *[]string `json:"scopes,omitempty" validate:"omitempty,min=1,max=3,dive,notblank"`
}

// CredentialResponse represents a credential and its single active API credential in API responses.
type CredentialResponse struct {
	ID           uuid.UUID           `json:"id"`
	TeamID       uuid.UUID           `json:"team_id"`
	Name         string              `json:"name"`
	KeySuffix    string              `json:"key_suffix"`
	Scopes       []string            `json:"scopes"`
	Role         string              `json:"role"`
	RateLimit    int                 `json:"rate_limit"`
	LastUsedAt   *string             `json:"last_used_at"`
	ExpiresAt    *string             `json:"expires_at"`
	CreatedAt    string              `json:"created_at"`
	MemoryAccess domain.MemoryAccess `json:"memory_access"`
}

// CredentialListItem represents a single credential in a list response.
type CredentialListItem struct {
	ID           uuid.UUID           `json:"id"`
	TeamID       uuid.UUID           `json:"team_id"`
	Name         string              `json:"name"`
	KeySuffix    string              `json:"key_suffix"`
	Scopes       []string            `json:"scopes"`
	Role         string              `json:"role"`
	RateLimit    int                 `json:"rate_limit"`
	LastUsedAt   *string             `json:"last_used_at"`
	ExpiresAt    *string             `json:"expires_at"`
	CreatedAt    string              `json:"created_at"`
	MemoryAccess domain.MemoryAccess `json:"memory_access"`
}

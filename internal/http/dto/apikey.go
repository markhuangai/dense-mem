package dto

import (
	"github.com/google/uuid"
)

// CreateAPIKeyRequest represents a request to create a new API key.
// Validation rules:
//   - Name: optional team profile name
//   - RateLimit: optional rate limit per minute
//   - ExpiresAt: optional expiration time
type CreateAPIKeyRequest struct {
	Name      string  `json:"name" validate:"omitempty,min=1,max=100,notblank"`
	RateLimit int     `json:"rate_limit"`
	ExpiresAt *string `json:"expires_at"`
}

// APIKeyResponse represents a team profile and its single active API key in API responses.
type APIKeyResponse struct {
	ID         uuid.UUID `json:"id"`
	TeamID     uuid.UUID `json:"team_id"`
	Name       string    `json:"name"`
	KeySuffix  string    `json:"key_suffix"`
	RateLimit  int       `json:"rate_limit"`
	LastUsedAt *string   `json:"last_used_at"`
	ExpiresAt  *string   `json:"expires_at"`
	CreatedAt  string    `json:"created_at"`
}

// APIKeyListItem represents a single team profile in a list response.
type APIKeyListItem struct {
	ID         uuid.UUID `json:"id"`
	TeamID     uuid.UUID `json:"team_id"`
	Name       string    `json:"name"`
	KeySuffix  string    `json:"key_suffix"`
	RateLimit  int       `json:"rate_limit"`
	LastUsedAt *string   `json:"last_used_at"`
	ExpiresAt  *string   `json:"expires_at"`
	CreatedAt  string    `json:"created_at"`
}

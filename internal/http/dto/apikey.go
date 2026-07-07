package dto

import (
	"github.com/google/uuid"
)

// CreateAPIKeyRequest represents a request to create a new API key.
// Validation rules:
//   - Name: optional team profile name
//   - Scopes: optional permission set; accepted values are read, write, and feedback:read
//   - RateLimit: optional rate limit per minute
//   - ExpiresAt: optional expiration time
type CreateAPIKeyRequest struct {
	Name      string    `json:"name" validate:"omitempty,min=1,max=100,notblank"`
	Scopes    *[]string `json:"scopes,omitempty" validate:"omitempty,min=1,max=3,dive,notblank"`
	RateLimit int       `json:"rate_limit"`
	ExpiresAt *string   `json:"expires_at"`
}

// UpdateAPIKeyRequest represents a protected API request to rename a member
// team profile. Role changes are intentionally control-portal only.
type UpdateAPIKeyRequest struct {
	Name string `json:"name" validate:"required,min=1,max=100,notblank"`
}

// APIKeyResponse represents a team profile and its single active API key in API responses.
type APIKeyResponse struct {
	ID         uuid.UUID `json:"id"`
	TeamID     uuid.UUID `json:"team_id"`
	Name       string    `json:"name"`
	KeySuffix  string    `json:"key_suffix"`
	Scopes     []string  `json:"scopes"`
	Role       string    `json:"role"`
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
	Scopes     []string  `json:"scopes"`
	Role       string    `json:"role"`
	RateLimit  int       `json:"rate_limit"`
	LastUsedAt *string   `json:"last_used_at"`
	ExpiresAt  *string   `json:"expires_at"`
	CreatedAt  string    `json:"created_at"`
}

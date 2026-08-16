package dto

import (
	"github.com/google/uuid"
)

// CreateTeamRequest represents a request to create a team.
// Validation rules:
//   - Name: 3-100 characters, not blank
//   - Description: max 500 characters
//   - Metadata: serialized size <= 10KB
//   - Config: serialized size <= 10KB
type CreateTeamRequest struct {
	Name        string         `json:"name" validate:"required,min=3,max=100,notblank"`
	Description string         `json:"description" validate:"max=500"`
	Metadata    map[string]any `json:"metadata" validate:"maxbytes=10240"`
	Config      map[string]any `json:"config" validate:"maxbytes=10240"`
}

// UpdateTeamRequest represents a request to update a team.
// Validation rules:
//   - Name: 3-100 characters, not blank (optional)
//   - Description: max 500 characters (optional)
//   - Metadata: serialized size <= 10KB (optional)
//   - Config: serialized size <= 10KB (optional)
type UpdateTeamRequest struct {
	Name        string         `json:"name" validate:"omitempty,min=3,max=100,notblank"`
	Description string         `json:"description" validate:"max=500"`
	Metadata    map[string]any `json:"metadata" validate:"maxbytes=10240"`
	Config      map[string]any `json:"config" validate:"maxbytes=10240"`
}

// TeamResponse represents a team in API responses.
type TeamResponse struct {
	ID          uuid.UUID      `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Metadata    map[string]any `json:"metadata"`
	Config      map[string]any `json:"config"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
}

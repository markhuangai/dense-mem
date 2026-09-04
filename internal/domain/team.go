package domain

import (
	"time"

	"github.com/google/uuid"
)

// TeamModel is the companion interface for Team.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type TeamModel interface {
	GetID() uuid.UUID
	GetName() string
	GetDescription() string
	GetMetadata() map[string]any
	GetConfig() map[string]any
	GetCreatedAt() time.Time
	GetUpdatedAt() time.Time
	GetDeletedAt() *time.Time
}

// Team represents a team knowledge base.
type Team struct {
	ID          uuid.UUID
	Name        string
	Description string
	Metadata    map[string]any
	Config      map[string]any
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time
}

// Ensure Team implements TeamModel
var _ TeamModel = (*Team)(nil)

// Getters for TeamModel interface
func (t *Team) GetID() uuid.UUID            { return t.ID }
func (t *Team) GetName() string             { return t.Name }
func (t *Team) GetDescription() string      { return t.Description }
func (t *Team) GetMetadata() map[string]any { return t.Metadata }
func (t *Team) GetConfig() map[string]any   { return t.Config }
func (t *Team) GetCreatedAt() time.Time     { return t.CreatedAt }
func (t *Team) GetUpdatedAt() time.Time     { return t.UpdatedAt }
func (t *Team) GetDeletedAt() *time.Time    { return t.DeletedAt }

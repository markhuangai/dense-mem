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
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time
}

// Profile is retained as a compatibility alias during the team rename.
type Profile = Team

// ProfileModel is retained as a compatibility alias during the team rename.
type ProfileModel = TeamModel

// Ensure Team implements TeamModel
var _ TeamModel = (*Team)(nil)

// Getters for TeamModel interface
func (p *Team) GetID() uuid.UUID            { return p.ID }
func (p *Team) GetName() string             { return p.Name }
func (p *Team) GetDescription() string      { return p.Description }
func (p *Team) GetMetadata() map[string]any { return p.Metadata }
func (p *Team) GetConfig() map[string]any   { return p.Config }
func (p *Team) GetCreatedAt() time.Time     { return p.CreatedAt }
func (p *Team) GetUpdatedAt() time.Time     { return p.UpdatedAt }
func (p *Team) GetDeletedAt() *time.Time    { return p.DeletedAt }

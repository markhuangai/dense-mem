package domain

import (
	"time"

	"github.com/google/uuid"
)

// AuditEntryModel is the companion interface for AuditLogEntry.
// Consumers and tests depend on this abstraction rather than the concrete struct.
// Field names mirror the audit_log table schema so the domain model is a
// truthful representation of persistence.
type AuditEntryModel interface {
	GetID() uuid.UUID
	GetProfileID() *uuid.UUID
	GetTimestamp() time.Time
	GetOperation() string
	GetEntityType() string
	GetEntityID() string
	GetBeforePayload() map[string]any
	GetAfterPayload() map[string]any
	GetActorKeyID() *uuid.UUID
	GetActorRole() string
	GetClientIP() string
	GetCorrelationID() string
	GetMetadata() map[string]any
}

// AuditLogEntry is a 1:1 representation of a row in the audit_log table.
// Nullable columns are represented with pointer types so callers can
// distinguish missing values from zero values when scanning rows.
type AuditLogEntry struct {
	ID            uuid.UUID
	ProfileID     *uuid.UUID
	Timestamp     time.Time
	Operation     string
	EntityType    string
	EntityID      string
	BeforePayload map[string]any
	AfterPayload  map[string]any
	ActorKeyID    *uuid.UUID
	ActorRole     string
	ClientIP      string
	CorrelationID string
	Metadata      map[string]any
}

// Ensure AuditLogEntry implements AuditEntryModel
var _ AuditEntryModel = (*AuditLogEntry)(nil)

func (a *AuditLogEntry) GetID() uuid.UUID                 { return a.ID }
func (a *AuditLogEntry) GetProfileID() *uuid.UUID         { return a.ProfileID }
func (a *AuditLogEntry) GetTimestamp() time.Time          { return a.Timestamp }
func (a *AuditLogEntry) GetOperation() string             { return a.Operation }
func (a *AuditLogEntry) GetEntityType() string            { return a.EntityType }
func (a *AuditLogEntry) GetEntityID() string              { return a.EntityID }
func (a *AuditLogEntry) GetBeforePayload() map[string]any { return a.BeforePayload }
func (a *AuditLogEntry) GetAfterPayload() map[string]any  { return a.AfterPayload }
func (a *AuditLogEntry) GetActorKeyID() *uuid.UUID        { return a.ActorKeyID }
func (a *AuditLogEntry) GetActorRole() string             { return a.ActorRole }
func (a *AuditLogEntry) GetClientIP() string              { return a.ClientIP }
func (a *AuditLogEntry) GetCorrelationID() string         { return a.CorrelationID }
func (a *AuditLogEntry) GetMetadata() map[string]any      { return a.Metadata }

// Package contract defines the narrow persistence boundary for audit events.
package contract

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// CredentialMemorySpaceLookup is a validated intent from audit policy. The
// adapter executes the lookup but does not decide when it is eligible.
type CredentialMemorySpaceLookup struct {
	TeamID       uuid.UUID
	CredentialID uuid.UUID
}

// Entry is the serialized audit record exchanged between audit policy and its
// persistence adapter. Payload redaction and JSON validation happen before an
// Entry reaches the adapter; the adapter only maps it to the audit_log table.
type Entry struct {
	ID                          string
	ProfileID                   *string
	MemorySpaceID               *string
	Timestamp                   time.Time
	Operation                   string
	EntityType                  string
	EntityID                    string
	BeforePayload               []byte
	AfterPayload                []byte
	ActorKeyID                  *string
	ActorRole                   string
	ClientIP                    any
	CorrelationID               string
	Metadata                    []byte
	CredentialMemorySpaceLookup *CredentialMemorySpaceLookup
}

// Store persists and reads audit entries without exposing a database handle to
// the application service.
type Store interface {
	Append(context.Context, Entry) error
	List(context.Context, string, int, int) ([]Entry, error)
	Count(context.Context, string) (int, error)
}

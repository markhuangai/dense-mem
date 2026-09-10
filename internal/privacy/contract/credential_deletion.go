package contract

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// CredentialDeletionStore owns the complete credential retirement boundary.
// Access supplies the operation timestamp and authenticated actor metadata
// while Privacy keeps credential, audit, and private-memory writes in one
// transaction.
type CredentialDeletionStore interface {
	RetireCredential(context.Context, uuid.UUID, uuid.UUID, time.Time, *CredentialDeletionAuditInput) (int64, error)
}

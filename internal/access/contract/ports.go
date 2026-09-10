// Package contract contains the dependency-safe contracts shared by the
// Access application and its PostgreSQL adapter.
package contract

import (
	"errors"
	"time"

	"github.com/google/uuid"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	privacycontract "github.com/markhuangai/dense-mem/internal/privacy/contract"
)

var (
	ErrTeamInactive                     = knowledgecontract.ErrTeamInactive
	ErrDirectoryIdentityNotProvisioned  = errors.New("directory identity is not provisioned and active")
	ErrDirectoryManagedMapping          = errors.New("directory-managed sso mappings are read-only")
	ErrDirectoryResourceConflict        = errors.New("directory resource conflict")
	ErrDirectoryInvalidValue            = errors.New("directory resource is invalid")
	ErrDirectoryReconcileStale          = errors.New("directory reconcile plan is stale")
	ErrSSOIdentityConflict              = errors.New("sso identity conflicts with a different external identity")
	ErrSSOProtectedResourceProfileLimit = errors.New("sso protected-resource profile limit exceeded")
)

// LastUsedUpdate is one admitted API-key activity timestamp.
type LastUsedUpdate struct {
	ID uuid.UUID
	At time.Time
}

// CredentialDeletionAuditInput carries actor metadata for the atomic
// credential-delete and audit transaction. Privacy owns this value because it
// is consumed by the privacy-owned transaction boundary.
type CredentialDeletionAuditInput = privacycontract.CredentialDeletionAuditInput

// CredentialDeletionStore is retained as an Access-facing alias while the
// Privacy contract owns the retirement transaction boundary.
type CredentialDeletionStore = privacycontract.CredentialDeletionStore

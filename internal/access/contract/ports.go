// Package contract contains the dependency-safe contracts shared by the
// Access application and its PostgreSQL adapter.
package contract

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
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

func NormalizeDirectoryUserPageRequest(request domain.DirectoryPageRequest) (domain.DirectoryPageRequest, error) {
	return normalizeDirectoryPageRequest(request, "user", "userName")
}

func NormalizeDirectoryGroupPageRequest(request domain.DirectoryPageRequest) (domain.DirectoryPageRequest, error) {
	return normalizeDirectoryPageRequest(request, "group", "displayName")
}

func normalizeDirectoryPageRequest(request domain.DirectoryPageRequest, resource, nameField string) (domain.DirectoryPageRequest, error) {
	if request.Offset < 0 || request.Limit < 0 || request.Limit > domain.DirectoryPageMaxResults {
		return domain.DirectoryPageRequest{}, fmt.Errorf("%w: directory page bounds are invalid", ErrDirectoryInvalidValue)
	}
	request.FilterField = strings.TrimSpace(request.FilterField)
	request.FilterValue = strings.TrimSpace(request.FilterValue)
	switch request.FilterField {
	case "", nameField, "externalId":
	case "id":
		id, err := uuid.Parse(request.FilterValue)
		if err != nil {
			return domain.DirectoryPageRequest{}, fmt.Errorf("%w: directory %s id filter is invalid", ErrDirectoryInvalidValue, resource)
		}
		request.FilterValue = id.String()
	default:
		return domain.DirectoryPageRequest{}, fmt.Errorf("%w: directory %s filter is invalid", ErrDirectoryInvalidValue, resource)
	}
	return request, nil
}

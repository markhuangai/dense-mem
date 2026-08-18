package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// MemorySpaceKind is the public, stable label for a team-scoped memory space.
type MemorySpaceKind string

const (
	MemorySpaceTeamShared        MemorySpaceKind = "team_shared"
	MemorySpaceProfilePrivate    MemorySpaceKind = "profile_private"
	MemorySpaceCredentialPrivate MemorySpaceKind = "credential_private"
)

// CredentialMemoryBinding controls which private branch a credential may use.
// It is immutable after credential creation.
type CredentialMemoryBinding string

const (
	CredentialBindingSharedOnly        CredentialMemoryBinding = "shared_only"
	CredentialBindingProfilePrivate    CredentialMemoryBinding = "profile_private"
	CredentialBindingCredentialPrivate CredentialMemoryBinding = "credential_private"
)

// MemorySpaceAccess is the bounded authorization projection carried by an
// authenticated request. The UUID remains internal; APIs expose only Kind.
type MemorySpaceAccess struct {
	ID   uuid.UUID
	Kind MemorySpaceKind
}

// MemorySpace identifies a durable team-owned space.
type MemorySpace struct {
	ID                uuid.UUID
	TeamID            uuid.UUID
	Kind              MemorySpaceKind
	OwnerProfileID    *uuid.UUID
	OwnerCredentialID *uuid.UUID
	Generation        int64
	LifecycleState    MemorySpaceLifecycleState
	PrivateContentAt  *time.Time
	SealedAt          *time.Time
	RetiredAt         *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type MemorySpaceLifecycleState string

const (
	MemorySpaceActive  MemorySpaceLifecycleState = "active"
	MemorySpaceSealed  MemorySpaceLifecycleState = "sealed"
	MemorySpaceRetired MemorySpaceLifecycleState = "retired"
)

func (s MemorySpaceLifecycleState) Valid() bool {
	switch s {
	case MemorySpaceActive, MemorySpaceSealed, MemorySpaceRetired:
		return true
	default:
		return false
	}
}

func (k MemorySpaceKind) Valid() bool {
	switch k {
	case MemorySpaceTeamShared, MemorySpaceProfilePrivate, MemorySpaceCredentialPrivate:
		return true
	default:
		return false
	}
}

func (b CredentialMemoryBinding) Valid() bool {
	switch b {
	case CredentialBindingSharedOnly, CredentialBindingProfilePrivate, CredentialBindingCredentialPrivate:
		return true
	default:
		return false
	}
}

func (b CredentialMemoryBinding) SpaceKind() MemorySpaceKind {
	switch b {
	case CredentialBindingProfilePrivate:
		return MemorySpaceProfilePrivate
	case CredentialBindingCredentialPrivate:
		return MemorySpaceCredentialPrivate
	default:
		return MemorySpaceTeamShared
	}
}

func NormalizeCredentialMemoryBinding(raw string) (CredentialMemoryBinding, error) {
	binding := CredentialMemoryBinding(raw)
	if binding == "" {
		return CredentialBindingSharedOnly, nil
	}
	if !binding.Valid() {
		return "", fmt.Errorf("invalid credential memory binding %q", raw)
	}
	return binding, nil
}

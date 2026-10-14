package domain

import (
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
)

// MemorySpaceKind is the public, stable label for a team-scoped memory space.
type MemorySpaceKind string

const (
	MemorySpaceTeamShared MemorySpaceKind = "team_shared"
	MemorySpacePrivate    MemorySpaceKind = "private"
)

// MemorySpaceAccess is the bounded authorization projection carried by an
// authenticated request. The UUID remains internal; APIs expose only Kind.
type MemorySpaceAccess struct {
	ID         uuid.UUID
	Kind       MemorySpaceKind
	Generation int64
}

// MemorySpace identifies a durable team-owned space.
type MemorySpace struct {
	ID                uuid.UUID
	TeamID            uuid.UUID
	Kind              MemorySpaceKind
	OwnerMembershipID *uuid.UUID
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
	case MemorySpaceTeamShared, MemorySpacePrivate:
		return true
	default:
		return false
	}
}

var (
	ErrMemoryTargetRequired  = errors.New("target_space is required; choose private or team_shared")
	ErrMemoryTargetInvalid   = errors.New("target_space must be private or team_shared")
	ErrMemoryWriteDenied     = errors.New("memory destination does not permit this write")
	ErrMemoryOperationErased = errors.New("this operation was erased; a retry cannot restore private memory")
)

type MemoryPermission struct {
	Read  bool `json:"read"`
	Write bool `json:"write"`
}

type MemoryAccess struct {
	Private    MemoryPermission `json:"private"`
	TeamShared MemoryPermission `json:"team_shared"`
}

func (c *Credential) MemorySpaces() []MemorySpaceAccess {
	if c == nil {
		return nil
	}
	return compactMemorySpaces([]MemorySpaceAccess{
		{ID: c.TeamSharedSpaceID, Kind: MemorySpaceTeamShared, Generation: c.TeamSharedSpaceGeneration},
		{ID: c.MemorySpaceID, Kind: MemorySpacePrivate, Generation: c.MemorySpaceGeneration},
	})
}

func (c *Credential) MemoryCapabilities() MemoryAccess {
	if c == nil {
		return MemoryAccess{}
	}
	return MemoryCapabilities(c.EffectiveMemoryGrants, c.MemorySpaces())
}

func (m Membership) MemorySpaces() []MemorySpaceAccess {
	return compactMemorySpaces([]MemorySpaceAccess{
		{ID: m.TeamSharedSpaceID, Kind: MemorySpaceTeamShared, Generation: m.TeamSharedSpaceGeneration},
		{ID: m.MemorySpaceID, Kind: MemorySpacePrivate, Generation: m.MemorySpaceGeneration},
	})
}

func MemoryCapabilities(grants []string, spaces []MemorySpaceAccess) MemoryAccess {
	read := slices.Contains(grants, "read")
	write := slices.Contains(grants, "write")
	var access MemoryAccess
	for _, kind := range []MemorySpaceKind{MemorySpacePrivate, MemorySpaceTeamShared} {
		if _, ok := uniqueMemorySpace(spaces, kind); !ok {
			continue
		}
		permission := MemoryPermission{Read: read, Write: read && (kind == MemorySpacePrivate || write)}
		if kind == MemorySpacePrivate {
			access.Private = permission
		} else {
			access.TeamShared = permission
		}
	}
	return access
}

func ResolveMemoryWriteSpace(target MemorySpaceKind, grants []string, spaces []MemorySpaceAccess) (MemorySpaceAccess, error) {
	if target == "" {
		return MemorySpaceAccess{}, ErrMemoryTargetRequired
	}
	if !target.Valid() {
		return MemorySpaceAccess{}, ErrMemoryTargetInvalid
	}
	space, ok := uniqueMemorySpace(spaces, target)
	if !ok {
		return MemorySpaceAccess{}, ErrMemoryWriteDenied
	}
	access := MemoryCapabilities(grants, spaces)
	if (target == MemorySpacePrivate && !access.Private.Write) ||
		(target == MemorySpaceTeamShared && !access.TeamShared.Write) {
		return MemorySpaceAccess{}, ErrMemoryWriteDenied
	}
	return space, nil
}

func AuthorizeMemoryMutation(spaceID uuid.UUID, grants []string, spaces []MemorySpaceAccess) error {
	for _, space := range spaces {
		if space.ID != spaceID {
			continue
		}
		_, err := ResolveMemoryWriteSpace(space.Kind, grants, spaces)
		return err
	}
	return ErrMemoryWriteDenied
}

func uniqueMemorySpace(spaces []MemorySpaceAccess, kind MemorySpaceKind) (MemorySpaceAccess, bool) {
	var selected MemorySpaceAccess
	for _, space := range spaces {
		if space.Kind != kind {
			continue
		}
		if selected.ID != uuid.Nil || space.ID == uuid.Nil || space.Generation < 1 {
			return MemorySpaceAccess{}, false
		}
		selected = space
	}
	return selected, selected.ID != uuid.Nil
}

func compactMemorySpaces(spaces []MemorySpaceAccess) []MemorySpaceAccess {
	return slices.DeleteFunc(spaces, func(space MemorySpaceAccess) bool { return space.ID == uuid.Nil })
}

package domain

import (
	"time"

	"github.com/google/uuid"
)

type PrivateMemoryErasureStatus string

const (
	PrivateMemoryErasureQueued     PrivateMemoryErasureStatus = "queued"
	PrivateMemoryErasureProcessing PrivateMemoryErasureStatus = "processing"
	PrivateMemoryErasureCompleted  PrivateMemoryErasureStatus = "completed"
	PrivateMemoryErasureFailed     PrivateMemoryErasureStatus = "failed"
)

type PrivateMemoryErasureAction string

const (
	PrivateMemoryEraseProfilePrivate    PrivateMemoryErasureAction = "erase_profile_private"
	PrivateMemoryEraseCredentialPrivate PrivateMemoryErasureAction = "erase_credential_private"
	PrivateMemoryRetireCredential       PrivateMemoryErasureAction = "retire_credential"
	PrivateMemoryRetentionPurge         PrivateMemoryErasureAction = "retention_purge"
)

type PrivateMemoryActorClass string

const (
	PrivateMemoryActorOwnerSSO        PrivateMemoryActorClass = "owner_sso"
	PrivateMemoryActorOwnerCredential PrivateMemoryActorClass = "owner_credential"
	PrivateMemoryActorControl         PrivateMemoryActorClass = "control"
	PrivateMemoryActorRetention       PrivateMemoryActorClass = "retention"
)

type PrivateMemoryErasureOperation struct {
	ID                 uuid.UUID
	TeamID             uuid.UUID
	SpaceID            *uuid.UUID
	SpaceKind          *MemorySpaceKind
	TargetCredentialID *uuid.UUID
	Action             PrivateMemoryErasureAction
	ActorClass         PrivateMemoryActorClass
	ReasonCode         string
	TargetGeneration   *int64
	RetireSpace        bool
	Status             PrivateMemoryErasureStatus
	ManifestPosition   int
	DeletedCounts      map[string]int64
	AttemptCount       int
	Fence              int64
	WorkerID           string
	LeaseUntil         *time.Time
	NextAttemptAt      *time.Time
	LastErrorCode      string
	RequestedAt        time.Time
	StartedAt          *time.Time
	CompletedAt        *time.Time
	UpdatedAt          time.Time
}

type PrivateMemoryLegalHold struct {
	ID         uuid.UUID
	TeamID     uuid.UUID
	SpaceID    uuid.UUID
	ReasonCode string
	ActorClass string
	PlacedAt   time.Time
	ReleasedAt *time.Time
}

type PrivateMemoryRetentionRun struct {
	ID            uuid.UUID
	ActorClass    PrivateMemoryActorClass
	Cutoff        time.Time
	RetentionDays int
	QueuedCount   int
	Status        string
	StartedAt     time.Time
	CompletedAt   *time.Time
}

type PrivateMemorySpaceMetadata struct {
	Space      MemorySpace
	ActiveHold *PrivateMemoryLegalHold
}

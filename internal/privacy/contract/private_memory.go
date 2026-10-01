// Package contract contains the privacy capability's application and storage
// ports. It is intentionally independent of PostgreSQL and transport code.
package contract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
)

var (
	ErrPrivateMemoryNotFound          = errors.New("private memory target not found")
	ErrPrivateMemoryLegalHold         = errors.New("private memory is under legal hold")
	ErrPrivateMemoryIdempotency       = errors.New("private memory idempotency conflict")
	ErrPrivateMemoryOperationConflict = errors.New("private memory erasure is already in progress")
	ErrPrivateMemoryManifest          = errors.New("private memory erasure manifest mismatch")
	ErrPrivateMemoryClaimLost         = errors.New("private memory erasure claim lost")
	ErrPrivateMemoryRetentionDisabled = errors.New("private memory retention is disabled")
	ErrPrivateMemoryHoldConflict      = errors.New("private memory legal hold conflict")
	ErrPrivateMemoryInternal          = errors.New("private memory storage operation failed")
)

const MaximumAttempts = 5

const (
	retryBaseDelay    = time.Second
	retryMaximumDelay = time.Minute
)

type RetryDecision struct {
	Status        domain.PrivateMemoryErasureStatus
	NextAttemptAt *time.Time
}

func Hash(parts ...string) string {
	digest := sha256.New()
	for _, part := range parts {
		_, _ = digest.Write([]byte(fmt.Sprintf("%d:", len(part))))
		_, _ = digest.Write([]byte(part))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func RetryDelay(attemptCount int) time.Duration {
	delay := retryBaseDelay
	for attempt := 1; attempt < attemptCount; attempt++ {
		if delay >= retryMaximumDelay/2 {
			return retryMaximumDelay
		}
		delay *= 2
	}
	return delay
}

func DecideRetry(attemptCount int, now time.Time) RetryDecision {
	if attemptCount >= MaximumAttempts {
		return RetryDecision{Status: domain.PrivateMemoryErasureFailed}
	}
	retryAt := now.Add(RetryDelay(attemptCount))
	return RetryDecision{Status: domain.PrivateMemoryErasureQueued, NextAttemptAt: &retryAt}
}

type PrivateMemoryErasureRequest struct {
	TeamID                    uuid.UUID
	OwnerID                   uuid.UUID
	CredentialID              uuid.UUID
	IdempotencyScopeHash      string
	RequestHash               string
	ReasonCode                string
	CredentialRevocationAudit *PrivateMemoryCredentialRevocationAudit
}

type PrivateMemoryCredentialRevocationAudit struct {
	ActorProfileID    *string
	ActorCredentialID *string
	ActorRole         string
	ClientIP          string
	CorrelationID     string
}

type CredentialDeletionAuditInput struct {
	ActorCredentialID *string
	ActorRole         string
	ClientIP          string
	CorrelationID     string
}

type PrivateMemoryRetentionRequest struct {
	ActorClass           domain.PrivateMemoryActorClass
	IdempotencyScopeHash string
	RequestHash          string
	RetentionDays        int
	BatchSize            int
	Now                  time.Time
}

type PrivateMemoryRepository interface {
	Prepare(context.Context) error
	RequestProfileErasure(context.Context, PrivateMemoryErasureRequest) (*domain.PrivateMemoryErasureOperation, bool, error)
	RequestCredentialErasure(context.Context, PrivateMemoryErasureRequest) (*domain.PrivateMemoryErasureOperation, bool, error)
	RequestControlErasure(context.Context, uuid.UUID, string, string, string) (*domain.PrivateMemoryErasureOperation, bool, error)
	DisableSSOCredential(context.Context, PrivateMemoryErasureRequest) (*domain.PrivateMemoryErasureOperation, bool, error)
	GetOwnerOperation(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID, *uuid.UUID) (*domain.PrivateMemoryErasureOperation, error)
	GetOperation(context.Context, uuid.UUID) (*domain.PrivateMemoryErasureOperation, error)
	ListOperations(context.Context, int, int) ([]domain.PrivateMemoryErasureOperation, error)
	ListSpaces(context.Context, int, int) ([]domain.PrivateMemorySpaceMetadata, error)
	PlaceLegalHold(context.Context, uuid.UUID, string) (*domain.PrivateMemoryLegalHold, bool, error)
	ReleaseLegalHold(context.Context, uuid.UUID) (*domain.PrivateMemoryLegalHold, bool, error)
	RunRetention(context.Context, PrivateMemoryRetentionRequest) (*domain.PrivateMemoryRetentionRun, bool, error)
	ListRetentionRuns(context.Context, int, int) ([]domain.PrivateMemoryRetentionRun, error)
	ClaimNext(context.Context, string, time.Duration) (*domain.PrivateMemoryErasureOperation, error)
	ExecuteClaim(context.Context, uuid.UUID, string, int64) (*domain.PrivateMemoryErasureOperation, error)
	ReleaseClaim(context.Context, uuid.UUID, string, int64, string) error
}

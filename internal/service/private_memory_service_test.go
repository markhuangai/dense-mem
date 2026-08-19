package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

type privateMemoryRepositoryStub struct {
	repository.PrivateMemoryRepository

	prepareErr error
	operation  *domain.PrivateMemoryErasureOperation
	requestErr error

	profileRequest     repository.PrivateMemoryErasureRequest
	credentialRequest  repository.PrivateMemoryErasureRequest
	disableRequest     repository.PrivateMemoryErasureRequest
	controlSpaceID     uuid.UUID
	controlScopeHash   string
	controlRequestHash string
	controlReason      string

	ownerTeamID       uuid.UUID
	ownerOperationID  uuid.UUID
	ownerIdentityID   *uuid.UUID
	ownerCredentialID *uuid.UUID
	operations        []domain.PrivateMemoryErasureOperation
	spaces            []domain.PrivateMemorySpaceMetadata
	hold              *domain.PrivateMemoryLegalHold
	holdChanged       bool
	holdSpaceID       uuid.UUID
	holdReason        string
	retentionInput    repository.PrivateMemoryRetentionRequest
	retentionRun      *domain.PrivateMemoryRetentionRun
	retentionRuns     []domain.PrivateMemoryRetentionRun
	runtimeErr        error

	claim         *domain.PrivateMemoryErasureOperation
	claimErr      error
	execute       *domain.PrivateMemoryErasureOperation
	executeErr    error
	releaseErr    error
	releases      int
	releaseID     uuid.UUID
	releaseWorker string
	releaseFence  int64
	releaseCode   string
}

func (r *privateMemoryRepositoryStub) Prepare(context.Context) error {
	return r.prepareErr
}

func (r *privateMemoryRepositoryStub) RequestProfileErasure(_ context.Context, input repository.PrivateMemoryErasureRequest) (*domain.PrivateMemoryErasureOperation, bool, error) {
	r.profileRequest = input
	return r.operation, true, r.requestErr
}

func (r *privateMemoryRepositoryStub) RequestCredentialErasure(_ context.Context, input repository.PrivateMemoryErasureRequest) (*domain.PrivateMemoryErasureOperation, bool, error) {
	r.credentialRequest = input
	return r.operation, true, r.requestErr
}

func (r *privateMemoryRepositoryStub) RequestControlErasure(_ context.Context, spaceID uuid.UUID, scopeHash, requestHash, reason string) (*domain.PrivateMemoryErasureOperation, bool, error) {
	r.controlSpaceID = spaceID
	r.controlScopeHash = scopeHash
	r.controlRequestHash = requestHash
	r.controlReason = reason
	return r.operation, true, r.requestErr
}

func (r *privateMemoryRepositoryStub) DisableSSOCredential(_ context.Context, input repository.PrivateMemoryErasureRequest) (*domain.PrivateMemoryErasureOperation, bool, error) {
	r.disableRequest = input
	return r.operation, true, r.requestErr
}

func (r *privateMemoryRepositoryStub) GetOwnerOperation(_ context.Context, teamID, operationID uuid.UUID, identityID, credentialID *uuid.UUID) (*domain.PrivateMemoryErasureOperation, error) {
	r.ownerTeamID = teamID
	r.ownerOperationID = operationID
	r.ownerIdentityID = identityID
	r.ownerCredentialID = credentialID
	return r.operation, r.requestErr
}

func (r *privateMemoryRepositoryStub) GetOperation(context.Context, uuid.UUID) (*domain.PrivateMemoryErasureOperation, error) {
	return r.operation, r.requestErr
}

func (r *privateMemoryRepositoryStub) ListOperations(context.Context, int, int) ([]domain.PrivateMemoryErasureOperation, error) {
	return r.operations, r.requestErr
}

func (r *privateMemoryRepositoryStub) ListSpaces(context.Context, int, int) ([]domain.PrivateMemorySpaceMetadata, error) {
	return r.spaces, r.requestErr
}

func (r *privateMemoryRepositoryStub) PlaceLegalHold(_ context.Context, spaceID uuid.UUID, reason string) (*domain.PrivateMemoryLegalHold, bool, error) {
	r.holdSpaceID = spaceID
	r.holdReason = reason
	return r.hold, r.holdChanged, r.requestErr
}

func (r *privateMemoryRepositoryStub) ReleaseLegalHold(_ context.Context, spaceID uuid.UUID) (*domain.PrivateMemoryLegalHold, bool, error) {
	r.holdSpaceID = spaceID
	return r.hold, r.holdChanged, r.requestErr
}

func (r *privateMemoryRepositoryStub) RunRetention(_ context.Context, input repository.PrivateMemoryRetentionRequest) (*domain.PrivateMemoryRetentionRun, bool, error) {
	r.retentionInput = input
	return r.retentionRun, true, r.runtimeErr
}

func (r *privateMemoryRepositoryStub) ListRetentionRuns(context.Context, int, int) ([]domain.PrivateMemoryRetentionRun, error) {
	return r.retentionRuns, r.requestErr
}

func (r *privateMemoryRepositoryStub) ClaimNext(context.Context, string, time.Duration) (*domain.PrivateMemoryErasureOperation, error) {
	return r.claim, r.claimErr
}

func (r *privateMemoryRepositoryStub) ExecuteClaim(context.Context, uuid.UUID, string, int64) (*domain.PrivateMemoryErasureOperation, error) {
	return r.execute, r.executeErr
}

func (r *privateMemoryRepositoryStub) ReleaseClaim(_ context.Context, operationID uuid.UUID, workerID string, fence int64, errorCode string) error {
	r.releases++
	r.releaseID = operationID
	r.releaseWorker = workerID
	r.releaseFence = fence
	r.releaseCode = errorCode
	return r.releaseErr
}

type privateMemoryRuntimeConfigStub struct {
	config domain.PrivateMemoryRuntimeConfig
	err    error
}

func (s *privateMemoryRuntimeConfigStub) PrivateMemoryRuntimeConfig(context.Context) (domain.PrivateMemoryRuntimeConfig, error) {
	return s.config, s.err
}

type privateMemorySessionInvalidatorStub struct {
	teamID       string
	credentialID string
	err          error
}

func (s *privateMemorySessionInvalidatorStub) InvalidateCredentialSessions(_ context.Context, teamID, credentialID string) error {
	s.teamID = teamID
	s.credentialID = credentialID
	return s.err
}

type privateMemoryAuditStub struct {
	AuditService
	calls           int
	teamID          string
	credentialID    string
	beforePayload   map[string]interface{}
	actorCredential *string
	actorRole       string
	clientIP        string
	correlationID   string
}

func (s *privateMemoryAuditStub) CredentialRevoked(_ context.Context, teamID *string, credentialID string, beforePayload map[string]interface{}, actorCredentialID *string, actorRole, clientIP, correlationID string) error {
	s.calls++
	if teamID != nil {
		s.teamID = *teamID
	}
	s.credentialID = credentialID
	s.beforePayload = beforePayload
	s.actorCredential = actorCredentialID
	s.actorRole = actorRole
	s.clientIP = clientIP
	s.correlationID = correlationID
	return nil
}

func TestPrivateMemoryServiceValidatesAndScopesOwnerCommands(t *testing.T) {
	ctx := context.Background()
	teamID := uuid.New()
	identityID := uuid.New()
	credentialID := uuid.New()
	spaceID := uuid.New()
	operation := &domain.PrivateMemoryErasureOperation{ID: uuid.New()}
	repo := &privateMemoryRepositoryStub{operation: operation}
	invalidator := &privateMemorySessionInvalidatorStub{err: errors.New("redis unavailable")}
	audit := &privateMemoryAuditStub{}
	logger := &activityLogger{}
	svc := NewPrivateMemoryService(PrivateMemoryServiceConfig{
		Repository: repo, SessionInvalidator: invalidator, AuditService: audit, Logger: logger,
		WorkerID: " worker-a ", WorkerPoll: -1, WorkerLease: -1, RetentionPoll: -1,
	})

	require.Equal(t, "worker-a", svc.workerID)
	require.Equal(t, defaultPrivateMemoryWorkerPoll, svc.workerPoll)
	require.Equal(t, defaultPrivateMemoryWorkerLease, svc.workerLease)
	require.Equal(t, defaultPrivateMemoryRetentionPoll, svc.retentionPoll)
	require.NoError(t, svc.Prepare(ctx))
	repo.prepareErr = errors.New("manifest rejected")
	require.ErrorIs(t, svc.Prepare(ctx), repo.prepareErr)
	require.Error(t, (*PrivateMemoryService)(nil).Prepare(ctx))

	_, err := svc.RequestSSOProfileErasure(ctx, teamID, identityID, PrivateMemoryCommand{})
	require.ErrorIs(t, err, ErrPrivateMemoryAcknowledgementRequired)
	_, err = svc.RequestSSOProfileErasure(ctx, teamID, identityID, PrivateMemoryCommand{
		AcknowledgeIrreversible: true,
	})
	require.ErrorIs(t, err, ErrPrivateMemoryIdempotencyKeyRequired)
	_, err = svc.RequestSSOProfileErasure(ctx, teamID, identityID, PrivateMemoryCommand{
		AcknowledgeIrreversible: true,
		IdempotencyKey:          strings.Repeat("x", maximumPrivateMemoryIdempotencyRunes+1),
	})
	require.ErrorIs(t, err, ErrPrivateMemoryIdempotencyKeyRequired)
	_, err = svc.RequestSSOProfileErasure(ctx, teamID, identityID, PrivateMemoryCommand{
		AcknowledgeIrreversible: true, IdempotencyKey: "request-1", ReasonCode: "Bad Reason",
	})
	require.ErrorIs(t, err, ErrPrivateMemoryInvalidReason)

	command := PrivateMemoryCommand{AcknowledgeIrreversible: true, IdempotencyKey: " request-1 "}
	result, err := svc.RequestSSOProfileErasure(ctx, teamID, identityID, command)
	require.NoError(t, err)
	require.Same(t, operation, result)
	require.Equal(t, teamID, repo.profileRequest.TeamID)
	require.Equal(t, identityID, repo.profileRequest.OwnerID)
	require.Equal(t, "owner_request", repo.profileRequest.ReasonCode)
	require.Len(t, repo.profileRequest.IdempotencyScopeHash, sha256HexLength)
	require.Len(t, repo.profileRequest.RequestHash, sha256HexLength)

	result, err = svc.RequestCredentialErasure(ctx, teamID, credentialID, command)
	require.NoError(t, err)
	require.Same(t, operation, result)
	require.Equal(t, credentialID, repo.credentialRequest.OwnerID)
	require.Equal(t, credentialID, repo.credentialRequest.CredentialID)
	require.NotEqual(t, repo.profileRequest.IdempotencyScopeHash, repo.credentialRequest.IdempotencyScopeHash)
	_, err = svc.RequestCredentialErasure(ctx, teamID, credentialID, PrivateMemoryCommand{})
	require.ErrorIs(t, err, ErrPrivateMemoryAcknowledgementRequired)

	result, err = svc.DeleteSSOCredential(ctx, teamID, identityID, credentialID, command, PrivateMemoryAuditContext{
		ActorRole: "member", ClientIP: "198.51.100.10", CorrelationID: "corr-delete",
	})
	require.NoError(t, err)
	require.Same(t, operation, result)
	require.Equal(t, identityID, repo.disableRequest.OwnerID)
	require.Equal(t, credentialID, repo.disableRequest.CredentialID)
	require.Equal(t, "credential_deleted", repo.disableRequest.ReasonCode)
	require.Equal(t, teamID.String(), invalidator.teamID)
	require.Equal(t, credentialID.String(), invalidator.credentialID)
	require.NotEmpty(t, logger.warnings)
	require.Equal(t, 1, audit.calls)
	require.Equal(t, teamID.String(), audit.teamID)
	require.Equal(t, credentialID.String(), audit.credentialID)
	require.Equal(t, identityID.String(), audit.beforePayload["owner_identity_id"])
	require.Equal(t, "active", audit.beforePayload["status"])
	require.Nil(t, audit.beforePayload["revoked_at"])
	require.Equal(t, "member", audit.actorRole)
	require.Equal(t, "198.51.100.10", audit.clientIP)
	require.Equal(t, "corr-delete", audit.correlationID)
	_, err = svc.DeleteSSOCredential(ctx, teamID, identityID, credentialID, PrivateMemoryCommand{}, PrivateMemoryAuditContext{})
	require.ErrorIs(t, err, ErrPrivateMemoryAcknowledgementRequired)

	controlCommand := command
	controlCommand.ReasonCode = "privacy_request"
	result, err = svc.RequestControlErasure(ctx, spaceID, controlCommand)
	require.NoError(t, err)
	require.Same(t, operation, result)
	require.Equal(t, spaceID, repo.controlSpaceID)
	require.Equal(t, "privacy_request", repo.controlReason)
	require.Len(t, repo.controlScopeHash, sha256HexLength)
	require.Len(t, repo.controlRequestHash, sha256HexLength)
	_, err = svc.RequestControlErasure(ctx, spaceID, PrivateMemoryCommand{})
	require.ErrorIs(t, err, ErrPrivateMemoryAcknowledgementRequired)
}

func TestPrivateMemoryServiceDelegatesAuthorizedReadsHoldsAndRetention(t *testing.T) {
	ctx := context.Background()
	teamID := uuid.New()
	identityID := uuid.New()
	credentialID := uuid.New()
	operationID := uuid.New()
	spaceID := uuid.New()
	operation := &domain.PrivateMemoryErasureOperation{ID: operationID}
	hold := &domain.PrivateMemoryLegalHold{ID: uuid.New(), SpaceID: spaceID}
	run := &domain.PrivateMemoryRetentionRun{ID: uuid.New()}
	repo := &privateMemoryRepositoryStub{
		operation:  operation,
		operations: []domain.PrivateMemoryErasureOperation{*operation},
		spaces:     []domain.PrivateMemorySpaceMetadata{{Space: domain.MemorySpace{ID: spaceID}}},
		hold:       hold, holdChanged: true,
		retentionRun: run, retentionRuns: []domain.PrivateMemoryRetentionRun{*run},
	}
	runtime := &privateMemoryRuntimeConfigStub{config: domain.PrivateMemoryRuntimeConfig{RetentionDays: 30}}
	svc := NewPrivateMemoryService(PrivateMemoryServiceConfig{Repository: repo, RuntimeConfig: runtime})
	fixedNow := time.Date(2026, 8, 18, 12, 0, 0, 0, time.FixedZone("offset", 3600))
	svc.now = func() time.Time { return fixedNow }

	loaded, err := svc.GetOwnerOperation(ctx, teamID, operationID, &identityID, &credentialID)
	require.NoError(t, err)
	require.Same(t, operation, loaded)
	require.Equal(t, teamID, repo.ownerTeamID)
	require.Equal(t, operationID, repo.ownerOperationID)
	require.Equal(t, identityID, *repo.ownerIdentityID)
	require.Equal(t, credentialID, *repo.ownerCredentialID)
	loaded, err = svc.GetOperation(ctx, operationID)
	require.NoError(t, err)
	require.Same(t, operation, loaded)
	operations, err := svc.ListOperations(ctx, 20, 5)
	require.NoError(t, err)
	require.Len(t, operations, 1)
	spaces, err := svc.ListSpaces(ctx, 20, 5)
	require.NoError(t, err)
	require.Len(t, spaces, 1)

	_, _, err = svc.PlaceLegalHold(ctx, spaceID, "Bad Reason")
	require.ErrorIs(t, err, ErrPrivateMemoryInvalidReason)
	placed, created, err := svc.PlaceLegalHold(ctx, spaceID, " legal_hold ")
	require.NoError(t, err)
	require.True(t, created)
	require.Same(t, hold, placed)
	require.Equal(t, "legal_hold", repo.holdReason)
	released, changed, err := svc.ReleaseLegalHold(ctx, spaceID)
	require.NoError(t, err)
	require.True(t, changed)
	require.Same(t, hold, released)

	command := PrivateMemoryCommand{AcknowledgeIrreversible: true, IdempotencyKey: "retention-1"}
	_, err = svc.RunRetention(ctx, PrivateMemoryCommand{}, domain.PrivateMemoryActorControl)
	require.ErrorIs(t, err, ErrPrivateMemoryAcknowledgementRequired)
	retention, err := svc.RunRetention(ctx, command, domain.PrivateMemoryActorControl)
	require.NoError(t, err)
	require.Same(t, run, retention)
	require.Equal(t, domain.PrivateMemoryActorControl, repo.retentionInput.ActorClass)
	require.Equal(t, 30, repo.retentionInput.RetentionDays)
	require.Equal(t, fixedNow.UTC(), repo.retentionInput.Now)
	require.Len(t, repo.retentionInput.IdempotencyScopeHash, sha256HexLength)
	runs, err := svc.ListRetentionRuns(ctx, 10, 2)
	require.NoError(t, err)
	require.Len(t, runs, 1)

	svc.runtimeConfig = nil
	_, err = svc.RunRetention(ctx, command, domain.PrivateMemoryActorControl)
	require.EqualError(t, err, "private memory runtime config is unavailable")
	runtime.err = errors.New("config unavailable")
	svc.runtimeConfig = runtime
	_, err = svc.RunRetention(ctx, command, domain.PrivateMemoryActorControl)
	require.ErrorIs(t, err, runtime.err)
}

func TestPrivateMemoryServiceWorkerAndAutomaticRetentionPolicy(t *testing.T) {
	ctx := context.Background()
	logger := &activityLogger{}
	runtime := &privateMemoryRuntimeConfigStub{}
	repo := &privateMemoryRepositoryStub{}
	svc := NewPrivateMemoryService(PrivateMemoryServiceConfig{
		Repository: repo, RuntimeConfig: runtime, Logger: logger, WorkerID: "worker-a",
		WorkerPoll: time.Millisecond, RetentionPoll: time.Millisecond,
	})

	repo.claimErr = errors.New("claim failed")
	require.False(t, svc.processOne(ctx))
	repo.claimErr = nil
	require.False(t, svc.processOne(ctx))

	kind := domain.MemorySpaceCredentialPrivate
	claimed := &domain.PrivateMemoryErasureOperation{
		ID: uuid.New(), WorkerID: "worker-a", Fence: 7, SpaceKind: &kind,
	}
	completed := &domain.PrivateMemoryErasureOperation{ID: claimed.ID, SpaceKind: &kind}
	repo.claim = claimed
	repo.execute = completed
	require.True(t, svc.processOne(ctx))

	repo.executeErr = repository.ErrPrivateMemoryLegalHold
	repo.releaseErr = errors.New("release failed")
	require.False(t, svc.processOne(ctx))
	require.Equal(t, 1, repo.releases)
	require.Equal(t, claimed.ID, repo.releaseID)
	require.Equal(t, "worker-a", repo.releaseWorker)
	require.Equal(t, int64(7), repo.releaseFence)
	require.Equal(t, "legal_hold", repo.releaseCode)

	repo.executeErr = repository.ErrPrivateMemoryClaimLost
	repo.releaseErr = nil
	require.False(t, svc.processOne(ctx))
	require.Equal(t, 1, repo.releases)
	require.NotEmpty(t, logger.warnings)

	runtime.err = errors.New("config unavailable")
	svc.runAutomaticRetention(ctx, time.Now())
	runtime.err = nil
	runtime.config.RetentionDays = 0
	svc.runAutomaticRetention(ctx, time.Now())
	runtime.config.RetentionDays = 14
	repo.runtimeErr = repository.ErrPrivateMemoryManifest
	now := time.Date(2026, 8, 18, 12, 34, 0, 0, time.UTC)
	svc.runAutomaticRetention(ctx, now)
	require.Equal(t, domain.PrivateMemoryActorRetention, repo.retentionInput.ActorClass)
	require.Equal(t, 14, repo.retentionInput.RetentionDays)
	require.Equal(t, now, repo.retentionInput.Now)

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	repo.claim = nil
	repo.claimErr = nil
	svc.runWorker(canceled)
	svc.runRetentionScheduler(canceled)
	svc.Start(canceled)
	(*PrivateMemoryService)(nil).Start(ctx)
	NewPrivateMemoryService(PrivateMemoryServiceConfig{}).Start(ctx)

	for err, expected := range map[error]string{
		repository.ErrPrivateMemoryLegalHold: "legal_hold",
		repository.ErrPrivateMemoryManifest:  "manifest_mismatch",
		repository.ErrPrivateMemoryClaimLost: "claim_lost",
		context.Canceled:                     "canceled",
		context.DeadlineExceeded:             "timeout",
		errors.New("database unavailable"):   "database_operation",
	} {
		require.Equal(t, expected, privateMemoryServiceErrorCode(err))
	}
	require.Empty(t, stringValue(nil))
	require.Equal(t, string(kind), stringValue(&kind))
	require.NotEqual(t, privateMemoryServiceHash("ab", "c"), privateMemoryServiceHash("a", "bc"))
}

const sha256HexLength = 64

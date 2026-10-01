package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	privacycontract "github.com/markhuangai/dense-mem/internal/privacy/contract"
)

type activityLogger struct {
	mu       sync.Mutex
	warnings []string
}

func (*activityLogger) Info(string, ...observability.LogAttr)         {}
func (*activityLogger) Error(string, error, ...observability.LogAttr) {}
func (l *activityLogger) Warn(message string, _ ...observability.LogAttr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warnings = append(l.warnings, message)
}
func (*activityLogger) Debug(string, ...observability.LogAttr)                    {}
func (l *activityLogger) With(...observability.LogAttr) observability.LogProvider { return l }

type privateMemoryRepositoryStub struct {
	privacycontract.PrivateMemoryRepository

	prepareErr error
	operation  *domain.PrivateMemoryErasureOperation
	requestErr error

	profileRequest       privacycontract.PrivateMemoryErasureRequest
	credentialRequest    privacycontract.PrivateMemoryErasureRequest
	disableRequest       privacycontract.PrivateMemoryErasureRequest
	disableAudit         *privacycontract.PrivateMemoryCredentialRevocationAudit
	disableRequestHashes map[string]string
	controlSpaceID       uuid.UUID
	controlScopeHash     string
	controlRequestHash   string
	controlReason        string

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
	retentionInput    privacycontract.PrivateMemoryRetentionRequest
	retentionRun      *domain.PrivateMemoryRetentionRun
	retentionRuns     []domain.PrivateMemoryRetentionRun
	runtimeErr        error

	claim              *domain.PrivateMemoryErasureOperation
	claimErr           error
	claimStarted       chan struct{}
	claimBlock         <-chan struct{}
	claimIgnoreContext bool
	execute            *domain.PrivateMemoryErasureOperation
	executeErr         error
	releaseErr         error
	releases           int
	releaseID          uuid.UUID
	releaseWorker      string
	releaseFence       int64
	releaseCode        string
}

func (r *privateMemoryRepositoryStub) Prepare(context.Context) error {
	return r.prepareErr
}

func (r *privateMemoryRepositoryStub) RequestProfileErasure(_ context.Context, input privacycontract.PrivateMemoryErasureRequest) (*domain.PrivateMemoryErasureOperation, bool, error) {
	r.profileRequest = input
	return r.operation, true, r.requestErr
}

func (r *privateMemoryRepositoryStub) RequestCredentialErasure(_ context.Context, input privacycontract.PrivateMemoryErasureRequest) (*domain.PrivateMemoryErasureOperation, bool, error) {
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

func (r *privateMemoryRepositoryStub) DisableSSOCredential(_ context.Context, input privacycontract.PrivateMemoryErasureRequest) (*domain.PrivateMemoryErasureOperation, bool, error) {
	r.disableRequest = input
	r.disableAudit = input.CredentialRevocationAudit
	if r.disableRequestHashes != nil {
		if previous, exists := r.disableRequestHashes[input.IdempotencyScopeHash]; exists {
			if previous != input.RequestHash {
				return nil, false, privacycontract.ErrPrivateMemoryIdempotency
			}
			return r.operation, false, r.requestErr
		}
		r.disableRequestHashes[input.IdempotencyScopeHash] = input.RequestHash
	}
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

func (r *privateMemoryRepositoryStub) RunRetention(_ context.Context, input privacycontract.PrivateMemoryRetentionRequest) (*domain.PrivateMemoryRetentionRun, bool, error) {
	r.retentionInput = input
	return r.retentionRun, true, r.runtimeErr
}

func (r *privateMemoryRepositoryStub) ListRetentionRuns(context.Context, int, int) ([]domain.PrivateMemoryRetentionRun, error) {
	return r.retentionRuns, r.requestErr
}

func (r *privateMemoryRepositoryStub) ClaimNext(ctx context.Context, _ string, _ time.Duration) (*domain.PrivateMemoryErasureOperation, error) {
	if r.claimStarted != nil {
		close(r.claimStarted)
		r.claimStarted = nil
	}
	if r.claimBlock != nil {
		if r.claimIgnoreContext {
			<-r.claimBlock
		} else {
			select {
			case <-r.claimBlock:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
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
	config  domain.PrivateMemoryRuntimeConfig
	err     error
	started chan struct{}
	block   <-chan struct{}
}

func (s *privateMemoryRuntimeConfigStub) PrivateMemoryRuntimeConfig(ctx context.Context) (domain.PrivateMemoryRuntimeConfig, error) {
	if s.started != nil {
		close(s.started)
		s.started = nil
	}
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return domain.PrivateMemoryRuntimeConfig{}, ctx.Err()
		}
	}
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

	actorProfileID := identityID.String()
	actorCredentialID := credentialID.String()
	result, err = svc.DeleteSSOCredential(ctx, teamID, identityID, credentialID, command, PrivateMemoryAuditContext{
		ActorProfileID: &actorProfileID, ActorCredentialID: &actorCredentialID,
		ActorRole: "member", ClientIP: "198.51.100.10", CorrelationID: "corr-delete",
	})
	require.NoError(t, err)
	require.Same(t, operation, result)
	require.Equal(t, identityID, repo.disableRequest.OwnerID)
	require.Equal(t, credentialID, repo.disableRequest.CredentialID)
	require.Equal(t, "credential_deleted", repo.disableRequest.ReasonCode)
	require.NotNil(t, repo.disableAudit)
	require.Equal(t, actorProfileID, *repo.disableAudit.ActorProfileID)
	require.Equal(t, actorCredentialID, *repo.disableAudit.ActorCredentialID)
	require.Equal(t, "member", repo.disableAudit.ActorRole)
	require.Equal(t, "198.51.100.10", repo.disableAudit.ClientIP)
	require.Equal(t, "corr-delete", repo.disableAudit.CorrelationID)
	require.Equal(t, teamID.String(), invalidator.teamID)
	require.Equal(t, credentialID.String(), invalidator.credentialID)
	require.NotEmpty(t, logger.warnings)
	require.Zero(t, audit.calls)
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

func TestPrivateMemoryServiceCredentialDeletionReasonBindsRequestHash(t *testing.T) {
	ctx := context.Background()
	teamID := uuid.New()
	identityID := uuid.New()
	credentialID := uuid.New()
	repo := &privateMemoryRepositoryStub{
		operation:            &domain.PrivateMemoryErasureOperation{ID: uuid.New()},
		disableRequestHashes: make(map[string]string),
	}
	svc := NewPrivateMemoryService(PrivateMemoryServiceConfig{
		Repository:   repo,
		AuditService: &privateMemoryAuditStub{},
	})
	command := PrivateMemoryCommand{
		AcknowledgeIrreversible: true,
		IdempotencyKey:          "same-delete-key",
		ReasonCode:              "credential_deleted",
	}
	_, err := svc.DeleteSSOCredential(ctx, teamID, identityID, credentialID, command, PrivateMemoryAuditContext{})
	require.NoError(t, err)
	firstHash := repo.disableRequest.RequestHash

	command.ReasonCode = "privacy_request"
	_, err = svc.DeleteSSOCredential(ctx, teamID, identityID, credentialID, command, PrivateMemoryAuditContext{})
	require.ErrorIs(t, err, privacycontract.ErrPrivateMemoryIdempotency)
	require.Equal(t, repo.disableRequest.IdempotencyScopeHash, privacycontract.Hash("owner_sso_credential_delete", teamID.String(), identityID.String(), credentialID.String(), command.IdempotencyKey))
	require.NotEqual(t, firstHash, repo.disableRequest.RequestHash)
}

func TestPrivateMemoryServiceIdempotencyScopesBindTargets(t *testing.T) {
	ctx := context.Background()
	teamID := uuid.New()
	identityID := uuid.New()
	credentialOne := uuid.New()
	credentialTwo := uuid.New()
	spaceOne := uuid.New()
	spaceTwo := uuid.New()
	repo := &privateMemoryRepositoryStub{
		operation:            &domain.PrivateMemoryErasureOperation{ID: uuid.New()},
		disableRequestHashes: make(map[string]string),
	}
	svc := NewPrivateMemoryService(PrivateMemoryServiceConfig{
		Repository: repo, AuditService: &privateMemoryAuditStub{},
	})
	command := PrivateMemoryCommand{AcknowledgeIrreversible: true, IdempotencyKey: "same-target-key", ReasonCode: "credential_deleted"}
	_, err := svc.DeleteSSOCredential(ctx, teamID, identityID, credentialOne, command, PrivateMemoryAuditContext{})
	require.NoError(t, err)
	firstScope := repo.disableRequest.IdempotencyScopeHash
	_, err = svc.DeleteSSOCredential(ctx, teamID, identityID, credentialTwo, command, PrivateMemoryAuditContext{})
	require.NoError(t, err)
	require.NotEqual(t, firstScope, repo.disableRequest.IdempotencyScopeHash)

	command.ReasonCode = "privacy_request"
	_, err = svc.RequestControlErasure(ctx, spaceOne, command)
	require.NoError(t, err)
	firstControlScope := repo.controlScopeHash
	_, err = svc.RequestControlErasure(ctx, spaceTwo, command)
	require.NoError(t, err)
	require.NotEqual(t, firstControlScope, repo.controlScopeHash)
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
	require.ErrorIs(t, err, ErrPrivateMemoryRuntimeConfigUnavailable)
	runtime.err = errors.New("config unavailable")
	svc.runtimeConfig = runtime
	_, err = svc.RunRetention(ctx, command, domain.PrivateMemoryActorControl)
	require.ErrorIs(t, err, ErrPrivateMemoryRuntimeConfigUnavailable)
	require.NotContains(t, err.Error(), runtime.err.Error())
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

	repo.executeErr = privacycontract.ErrPrivateMemoryLegalHold
	repo.releaseErr = errors.New("release failed")
	require.False(t, svc.processOne(ctx))
	require.Equal(t, 1, repo.releases)
	require.Equal(t, claimed.ID, repo.releaseID)
	require.Equal(t, "worker-a", repo.releaseWorker)
	require.Equal(t, int64(7), repo.releaseFence)
	require.Equal(t, "legal_hold", repo.releaseCode)

	repo.executeErr = privacycontract.ErrPrivateMemoryClaimLost
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
	repo.runtimeErr = privacycontract.ErrPrivateMemoryManifest
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
		privacycontract.ErrPrivateMemoryLegalHold: "legal_hold",
		privacycontract.ErrPrivateMemoryManifest:  "manifest_mismatch",
		privacycontract.ErrPrivateMemoryClaimLost: "claim_lost",
		context.Canceled:                   "canceled",
		context.DeadlineExceeded:           "timeout",
		errors.New("database unavailable"): "database_operation",
	} {
		require.Equal(t, expected, privateMemoryServiceErrorCode(err))
	}
	require.Empty(t, stringValue(nil))
	require.Equal(t, string(kind), stringValue(&kind))
	require.NotEqual(t, privacycontract.Hash("ab", "c"), privacycontract.Hash("a", "bc"))
}

const sha256HexLength = 64

func TestPrivateMemoryWorkersStartOnceAndShutdownTogether(t *testing.T) {
	repo := &privateMemoryRepositoryStub{}
	runtime := &privateMemoryRuntimeConfigStub{config: domain.PrivateMemoryRuntimeConfig{RetentionDays: 1}}
	service := NewPrivateMemoryService(PrivateMemoryServiceConfig{Repository: repo, RuntimeConfig: runtime})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := service.Start(ctx)
	second := service.Start(ctx)
	if first != second {
		t.Fatal("starting private-memory workers twice created a second lifecycle")
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	if err := service.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("private-memory workers were not joined")
	}
}

func TestPrivateMemoryErasureWorkerJoinsDelayedClaim(t *testing.T) {
	claimStarted := make(chan struct{})
	claimBlock := make(chan struct{})
	repo := &privateMemoryRepositoryStub{claimStarted: claimStarted, claimBlock: claimBlock}
	service := NewPrivateMemoryService(PrivateMemoryServiceConfig{Repository: repo, WorkerPoll: time.Hour})
	done := service.Start(context.Background())
	select {
	case <-claimStarted:
	case <-time.After(time.Second):
		t.Fatal("private-memory erasure worker did not begin delayed claim")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("private-memory erasure worker was not joined")
	}
}

func TestPrivateMemoryShutdownTimeoutRetainsLifecycleForRetry(t *testing.T) {
	claimStarted := make(chan struct{})
	claimBlock := make(chan struct{})
	repo := &privateMemoryRepositoryStub{claimStarted: claimStarted, claimBlock: claimBlock, claimIgnoreContext: true}
	service := NewPrivateMemoryService(PrivateMemoryServiceConfig{Repository: repo, WorkerPoll: time.Hour})
	done := service.Start(context.Background())
	select {
	case <-claimStarted:
	case <-time.After(time.Second):
		t.Fatal("private-memory erasure worker did not begin claim")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	require.ErrorIs(t, service.Shutdown(shutdownCtx), context.DeadlineExceeded)
	cancel()
	if retryDone := service.Start(context.Background()); retryDone != done {
		t.Fatal("starting during a timed-out shutdown created a second lifecycle")
	}
	close(claimBlock)
	joinCtx, joinCancel := context.WithTimeout(context.Background(), time.Second)
	defer joinCancel()
	require.NoError(t, service.Shutdown(joinCtx))
}

func TestPrivateMemoryRetentionWorkerJoinsDelayedConfig(t *testing.T) {
	runtimeStarted := make(chan struct{})
	runtimeBlock := make(chan struct{})
	runtime := &privateMemoryRuntimeConfigStub{
		config:  domain.PrivateMemoryRuntimeConfig{RetentionDays: 1},
		started: runtimeStarted,
		block:   runtimeBlock,
	}
	service := NewPrivateMemoryService(PrivateMemoryServiceConfig{Repository: &privateMemoryRepositoryStub{}, RuntimeConfig: runtime})
	done := service.Start(context.Background())
	select {
	case <-runtimeStarted:
	case <-time.After(time.Second):
		t.Fatal("private-memory retention worker did not begin delayed config read")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("private-memory retention worker was not joined")
	}
}

func TestPrivateMemoryServiceOperationHashesStayStable(t *testing.T) {
	ctx := context.Background()
	teamID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	identityID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	credentialID := uuid.MustParse("33333333-3333-4333-8333-333333333333")
	spaceID := uuid.MustParse("44444444-4444-4444-8444-444444444444")
	repo := &privateMemoryRepositoryStub{operation: &domain.PrivateMemoryErasureOperation{ID: uuid.New()}}
	svc := NewPrivateMemoryService(PrivateMemoryServiceConfig{
		Repository: repo, AuditService: &privateMemoryAuditStub{},
		RuntimeConfig: &privateMemoryRuntimeConfigStub{config: domain.PrivateMemoryRuntimeConfig{RetentionDays: 30}},
	})
	command := PrivateMemoryCommand{IdempotencyKey: " key-π ", AcknowledgeIrreversible: true, ReasonCode: "privacy_request"}
	check := func(scope, request string, wantScope, wantRequest string) {
		t.Helper()
		require.Equal(t, wantScope, scope)
		require.Equal(t, wantRequest, request)
	}

	_, err := svc.RequestSSOProfileErasure(ctx, teamID, identityID, command)
	require.NoError(t, err)
	check(repo.profileRequest.IdempotencyScopeHash, repo.profileRequest.RequestHash,
		"c0593c78b2032e5ec5c4ee95f16c46d66e71bebe1e6acd25fa5779cd9f4b36ee",
		"2e2e2939b54326e74ade6b2908352457e0195ec81ce74cb494bc489c376e216c")

	_, err = svc.RequestCredentialErasure(ctx, teamID, credentialID, command)
	require.NoError(t, err)
	check(repo.credentialRequest.IdempotencyScopeHash, repo.credentialRequest.RequestHash,
		"1838810a5b34246b58e077adbcd8ec0f8be6bac8700def36314fa5bbc44a6ba3",
		"b193dd174284e48a3c33eff2921c001541e79338f2c59dca3319927a946abf91")

	_, err = svc.DeleteSSOCredential(ctx, teamID, identityID, credentialID, command, PrivateMemoryAuditContext{})
	require.NoError(t, err)
	check(repo.disableRequest.IdempotencyScopeHash, repo.disableRequest.RequestHash,
		"6d38fc347fe84dbd215ae446d83cc47e3b8bb8b47fdb622ac12c363621668c0b",
		"8b6524b638d859f57626c954f6cdb63cd6d52bfb4d92f4281cdb7765927acb8e")

	_, err = svc.RequestControlErasure(ctx, spaceID, command)
	require.NoError(t, err)
	check(repo.controlScopeHash, repo.controlRequestHash,
		"f744c34c19d763bcda2d8c9b6a257bb87c1a1bbad718f1a9323e6907924b3b72",
		"0afd5c44936734e784f7f28c59a9a7b6cca0de0d30b412ad6e448819440dc8a6")

	_, err = svc.RunRetention(ctx, command, domain.PrivateMemoryActorControl)
	require.NoError(t, err)
	check(repo.retentionInput.IdempotencyScopeHash, repo.retentionInput.RequestHash,
		"0119aec053889c8e7024c4709ecf9e55d0ce1200380ef040bd3ecace1bd0fcca",
		"c271dc390e90ecff6010970ef75e60080fd2202f44f9db96f5276557220913cc")

	svc.runAutomaticRetention(ctx, time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC))
	check(repo.retentionInput.IdempotencyScopeHash, repo.retentionInput.RequestHash,
		"06bc9159154b247ce3d2cee9041b847dbea4343bd3023b09140a456bcb83bde9",
		"502ac992ea7dee6afd5cddbd1760779ca5e7c58a1d633465126e144c6fec24c3")
}

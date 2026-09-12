package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	privacycontract "github.com/markhuangai/dense-mem/internal/privacy/contract"
)

var (
	ErrPrivateMemoryAcknowledgementRequired  = errors.New("irreversible private-memory erasure must be acknowledged")
	ErrPrivateMemoryIdempotencyKeyRequired   = errors.New("idempotency key is required")
	ErrPrivateMemoryInvalidReason            = errors.New("invalid private-memory reason code")
	ErrPrivateMemoryAuditUnavailable         = errors.New("private-memory audit service is unavailable")
	ErrPrivateMemoryRuntimeConfigUnavailable = errors.New("private-memory runtime configuration is unavailable")
)

const (
	defaultPrivateMemoryWorkerPoll       = time.Second
	defaultPrivateMemoryWorkerLease      = 5 * time.Minute
	defaultPrivateMemoryRetentionPoll    = time.Hour
	maximumPrivateMemoryIdempotencyRunes = 200
)

var privateMemoryReasonPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

type PrivateMemoryRuntimeConfigProvider interface {
	PrivateMemoryRuntimeConfig(ctx context.Context) (domain.PrivateMemoryRuntimeConfig, error)
}

type CredentialSessionInvalidator interface {
	InvalidateCredentialSessions(context.Context, string, string) error
}

// AuditService is a presence port. Credential retirement owns its durable
// audit transaction and only requires an audit-capable application to be set.
type AuditService interface{}

type PrivateMemoryCommand struct {
	IdempotencyKey          string
	AcknowledgeIrreversible bool
	ReasonCode              string
}

type PrivateMemoryAuditContext struct {
	ActorProfileID    *string
	ActorCredentialID *string
	ActorRole         string
	ClientIP          string
	CorrelationID     string
}

type PrivateMemoryServiceConfig struct {
	Repository         privacycontract.PrivateMemoryRepository
	RuntimeConfig      PrivateMemoryRuntimeConfigProvider
	SessionInvalidator CredentialSessionInvalidator
	AuditService       AuditService
	Logger             observability.LogProvider
	WorkerID           string
	WorkerPoll         time.Duration
	WorkerLease        time.Duration
	RetentionPoll      time.Duration
}

type PrivateMemoryService struct {
	repository         privacycontract.PrivateMemoryRepository
	runtimeConfig      PrivateMemoryRuntimeConfigProvider
	sessionInvalidator CredentialSessionInvalidator
	auditService       AuditService
	logger             observability.LogProvider
	workerID           string
	workerPoll         time.Duration
	workerLease        time.Duration
	retentionPoll      time.Duration
	now                func() time.Time

	lifecycleMu sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
}

func NewPrivateMemoryService(config PrivateMemoryServiceConfig) *PrivateMemoryService {
	workerID := strings.TrimSpace(config.WorkerID)
	if workerID == "" {
		hostname, _ := os.Hostname()
		workerID = fmt.Sprintf("private-erasure-%s-%d", hostname, os.Getpid())
	}
	workerPoll := config.WorkerPoll
	if workerPoll <= 0 {
		workerPoll = defaultPrivateMemoryWorkerPoll
	}
	workerLease := config.WorkerLease
	if workerLease <= 0 {
		workerLease = defaultPrivateMemoryWorkerLease
	}
	retentionPoll := config.RetentionPoll
	if retentionPoll <= 0 {
		retentionPoll = defaultPrivateMemoryRetentionPoll
	}
	return &PrivateMemoryService{
		repository: config.Repository, runtimeConfig: config.RuntimeConfig,
		sessionInvalidator: config.SessionInvalidator, auditService: config.AuditService, logger: config.Logger,
		workerID: workerID, workerPoll: workerPoll, workerLease: workerLease,
		retentionPoll: retentionPoll, now: func() time.Time { return time.Now().UTC() },
	}
}

func (s *PrivateMemoryService) Prepare(ctx context.Context) error {
	if s == nil || s.repository == nil {
		return fmt.Errorf("private memory service is unavailable")
	}
	return s.repository.Prepare(ctx)
}

func (s *PrivateMemoryService) RequestSSOProfileErasure(ctx context.Context, teamID, identityID uuid.UUID, command PrivateMemoryCommand) (*domain.PrivateMemoryErasureOperation, error) {
	key, reason, err := validatePrivateMemoryCommand(command, "owner_request")
	if err != nil {
		return nil, err
	}
	operation, _, err := s.repository.RequestProfileErasure(ctx, privacycontract.PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: identityID,
		IdempotencyScopeHash: privateMemoryServiceHash("owner_sso", teamID.String(), identityID.String(), key),
		RequestHash:          privateMemoryServiceHash(string(domain.PrivateMemoryEraseProfilePrivate), teamID.String(), identityID.String(), "acknowledged"),
		ReasonCode:           reason,
	})
	return operation, err
}

func (s *PrivateMemoryService) RequestCredentialErasure(ctx context.Context, teamID, credentialID uuid.UUID, command PrivateMemoryCommand) (*domain.PrivateMemoryErasureOperation, error) {
	key, reason, err := validatePrivateMemoryCommand(command, "owner_request")
	if err != nil {
		return nil, err
	}
	operation, _, err := s.repository.RequestCredentialErasure(ctx, privacycontract.PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: credentialID, CredentialID: credentialID,
		IdempotencyScopeHash: privateMemoryServiceHash("owner_credential", teamID.String(), credentialID.String(), key),
		RequestHash:          privateMemoryServiceHash(string(domain.PrivateMemoryEraseCredentialPrivate), teamID.String(), credentialID.String(), "acknowledged"),
		ReasonCode:           reason,
	})
	return operation, err
}

func (s *PrivateMemoryService) DeleteSSOCredential(ctx context.Context, teamID, identityID, credentialID uuid.UUID, command PrivateMemoryCommand, auditContext PrivateMemoryAuditContext) (*domain.PrivateMemoryErasureOperation, error) {
	key, reason, err := validatePrivateMemoryCommand(command, "credential_deleted")
	if err != nil {
		return nil, err
	}
	if s.auditService == nil {
		return nil, ErrPrivateMemoryAuditUnavailable
	}
	operation, _, err := s.repository.DisableSSOCredential(ctx, privacycontract.PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: identityID, CredentialID: credentialID,
		IdempotencyScopeHash: privateMemoryServiceHash("owner_sso_credential_delete", teamID.String(), identityID.String(), credentialID.String(), key),
		RequestHash:          privateMemoryServiceHash(string(domain.PrivateMemoryRetireCredential), teamID.String(), identityID.String(), credentialID.String(), reason, "acknowledged"),
		ReasonCode:           reason,
		CredentialRevocationAudit: &privacycontract.PrivateMemoryCredentialRevocationAudit{
			ActorProfileID:    auditContext.ActorProfileID,
			ActorCredentialID: auditContext.ActorCredentialID,
			ActorRole:         auditContext.ActorRole,
			ClientIP:          auditContext.ClientIP,
			CorrelationID:     auditContext.CorrelationID,
		},
	})
	if err != nil {
		return nil, err
	}
	if s.sessionInvalidator != nil {
		if err := s.sessionInvalidator.InvalidateCredentialSessions(ctx, teamID.String(), credentialID.String()); err != nil && s.logger != nil {
			s.logger.Warn("private memory credential session invalidation failed",
				observability.String("error_code", "coordination_cleanup_failed"),
				observability.String("team_id", teamID.String()),
				observability.String("key_id", credentialID.String()),
			)
		}
	}
	return operation, nil
}

func (s *PrivateMemoryService) RequestControlErasure(ctx context.Context, spaceID uuid.UUID, command PrivateMemoryCommand) (*domain.PrivateMemoryErasureOperation, error) {
	key, reason, err := validatePrivateMemoryCommand(command, "control_request")
	if err != nil {
		return nil, err
	}
	operation, _, err := s.repository.RequestControlErasure(
		ctx,
		spaceID,
		privateMemoryServiceHash("control_erasure", spaceID.String(), key),
		privateMemoryServiceHash("control_erasure", spaceID.String(), reason, "acknowledged"),
		reason,
	)
	return operation, err
}

func (s *PrivateMemoryService) GetOwnerOperation(ctx context.Context, teamID, operationID uuid.UUID, identityID, credentialID *uuid.UUID) (*domain.PrivateMemoryErasureOperation, error) {
	return s.repository.GetOwnerOperation(ctx, teamID, operationID, identityID, credentialID)
}

func (s *PrivateMemoryService) GetOperation(ctx context.Context, operationID uuid.UUID) (*domain.PrivateMemoryErasureOperation, error) {
	return s.repository.GetOperation(ctx, operationID)
}

func (s *PrivateMemoryService) ListOperations(ctx context.Context, limit, offset int) ([]domain.PrivateMemoryErasureOperation, error) {
	return s.repository.ListOperations(ctx, limit, offset)
}

func (s *PrivateMemoryService) ListSpaces(ctx context.Context, limit, offset int) ([]domain.PrivateMemorySpaceMetadata, error) {
	return s.repository.ListSpaces(ctx, limit, offset)
}

func (s *PrivateMemoryService) PlaceLegalHold(ctx context.Context, spaceID uuid.UUID, reasonCode string) (*domain.PrivateMemoryLegalHold, bool, error) {
	reasonCode = strings.TrimSpace(reasonCode)
	if !privateMemoryReasonPattern.MatchString(reasonCode) {
		return nil, false, ErrPrivateMemoryInvalidReason
	}
	return s.repository.PlaceLegalHold(ctx, spaceID, reasonCode)
}

func (s *PrivateMemoryService) ReleaseLegalHold(ctx context.Context, spaceID uuid.UUID) (*domain.PrivateMemoryLegalHold, bool, error) {
	return s.repository.ReleaseLegalHold(ctx, spaceID)
}

func (s *PrivateMemoryService) RunRetention(ctx context.Context, command PrivateMemoryCommand, actorClass domain.PrivateMemoryActorClass) (*domain.PrivateMemoryRetentionRun, error) {
	key, _, err := validatePrivateMemoryCommand(command, "retention_request")
	if err != nil {
		return nil, err
	}
	if s.runtimeConfig == nil {
		return nil, ErrPrivateMemoryRuntimeConfigUnavailable
	}
	runtime, err := s.runtimeConfig.PrivateMemoryRuntimeConfig(ctx)
	if err != nil {
		return nil, ErrPrivateMemoryRuntimeConfigUnavailable
	}
	run, _, err := s.repository.RunRetention(ctx, privacycontract.PrivateMemoryRetentionRequest{
		ActorClass: actorClass, RetentionDays: runtime.RetentionDays,
		IdempotencyScopeHash: privateMemoryServiceHash("retention", string(actorClass), key),
		RequestHash:          privateMemoryServiceHash("retention", fmt.Sprint(runtime.RetentionDays), "acknowledged"),
		Now:                  s.now().UTC(),
	})
	return run, err
}

func (s *PrivateMemoryService) ListRetentionRuns(ctx context.Context, limit, offset int) ([]domain.PrivateMemoryRetentionRun, error) {
	return s.repository.ListRetentionRuns(ctx, limit, offset)
}

// Start begins the private-memory workers and returns a channel that closes
// after both workers have stopped. The service owns the worker loops; callers
// only control their lifetime and wait for completion.
func (s *PrivateMemoryService) Start(ctx context.Context) <-chan struct{} {
	if s == nil || s.repository == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.lifecycleMu.Lock()
	if s.done != nil {
		done := s.done
		s.lifecycleMu.Unlock()
		return done
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.cancel = cancel
	s.done = done
	s.lifecycleMu.Unlock()

	go func() {
		var workers sync.WaitGroup
		workers.Add(1)
		go func() {
			defer workers.Done()
			s.runWorker(runCtx)
		}()
		if s.runtimeConfig != nil {
			workers.Add(1)
			go func() {
				defer workers.Done()
				s.runRetentionScheduler(runCtx)
			}()
		}
		workers.Wait()
		close(done)
	}()
	return done
}

// Shutdown cancels the private-memory workers and waits for both loops to
// finish before returning.
func (s *PrivateMemoryService) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.lifecycleMu.Lock()
	cancel := s.cancel
	done := s.done
	s.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		s.lifecycleMu.Lock()
		if s.done == done {
			s.cancel = nil
			s.done = nil
		}
		s.lifecycleMu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *PrivateMemoryService) runWorker(ctx context.Context) {
	ticker := time.NewTicker(s.workerPoll)
	defer ticker.Stop()
	for {
		worked := s.processOne(ctx)
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *PrivateMemoryService) processOne(ctx context.Context) bool {
	operation, err := s.repository.ClaimNext(ctx, s.workerID, s.workerLease)
	if err != nil {
		if ctx.Err() == nil && s.logger != nil {
			s.logger.Warn("private memory erasure claim failed", observability.String("error_code", privateMemoryServiceErrorCode(err)))
		}
		return false
	}
	if operation == nil {
		return false
	}
	completed, err := s.repository.ExecuteClaim(ctx, operation.ID, operation.WorkerID, operation.Fence)
	if err == nil {
		if s.logger != nil {
			s.logger.Info("private memory erasure completed",
				observability.String("operation_id", completed.ID.String()),
				observability.String("space_kind", stringValue(completed.SpaceKind)),
			)
		}
		return true
	}
	code := privateMemoryServiceErrorCode(err)
	if !errors.Is(err, privacycontract.ErrPrivateMemoryClaimLost) && ctx.Err() == nil {
		if releaseErr := s.repository.ReleaseClaim(ctx, operation.ID, operation.WorkerID, operation.Fence, code); releaseErr != nil && s.logger != nil {
			s.logger.Warn("private memory erasure claim release failed", observability.String("error_code", privateMemoryServiceErrorCode(releaseErr)))
		}
	}
	if ctx.Err() == nil && s.logger != nil {
		s.logger.Warn("private memory erasure failed",
			observability.String("operation_id", operation.ID.String()),
			observability.String("error_code", code),
		)
	}
	return false
}

func (s *PrivateMemoryService) runRetentionScheduler(ctx context.Context) {
	s.runAutomaticRetention(ctx, s.now().UTC())
	ticker := time.NewTicker(s.retentionPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.runAutomaticRetention(ctx, now.UTC())
		}
	}
}

func (s *PrivateMemoryService) runAutomaticRetention(ctx context.Context, now time.Time) {
	runtime, err := s.runtimeConfig.PrivateMemoryRuntimeConfig(ctx)
	if err != nil {
		if ctx.Err() == nil && s.logger != nil {
			s.logger.Warn("private memory retention config unavailable", observability.String("error_code", "configuration_unavailable"))
		}
		return
	}
	if runtime.RetentionDays <= 0 {
		return
	}
	window := now.UTC().Format("2006-01-02T15")
	_, _, err = s.repository.RunRetention(ctx, privacycontract.PrivateMemoryRetentionRequest{
		ActorClass: domain.PrivateMemoryActorRetention, RetentionDays: runtime.RetentionDays, Now: now.UTC(),
		IdempotencyScopeHash: privateMemoryServiceHash("automatic_retention", window),
		RequestHash:          privateMemoryServiceHash("automatic_retention", window, fmt.Sprint(runtime.RetentionDays)),
	})
	if err != nil && ctx.Err() == nil && s.logger != nil {
		s.logger.Warn("private memory retention failed", observability.String("error_code", privateMemoryServiceErrorCode(err)))
	}
}

func validatePrivateMemoryCommand(command PrivateMemoryCommand, defaultReason string) (string, string, error) {
	if !command.AcknowledgeIrreversible {
		return "", "", ErrPrivateMemoryAcknowledgementRequired
	}
	key := strings.TrimSpace(command.IdempotencyKey)
	if key == "" || len([]rune(key)) > maximumPrivateMemoryIdempotencyRunes {
		return "", "", ErrPrivateMemoryIdempotencyKeyRequired
	}
	reason := strings.TrimSpace(command.ReasonCode)
	if reason == "" {
		reason = defaultReason
	}
	if !privateMemoryReasonPattern.MatchString(reason) {
		return "", "", ErrPrivateMemoryInvalidReason
	}
	return key, reason, nil
}

func privateMemoryServiceHash(parts ...string) string {
	digest := sha256.New()
	for _, part := range parts {
		_, _ = digest.Write([]byte(fmt.Sprintf("%d:", len(part))))
		_, _ = digest.Write([]byte(part))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func privateMemoryServiceErrorCode(err error) string {
	switch {
	case errors.Is(err, privacycontract.ErrPrivateMemoryLegalHold):
		return "legal_hold"
	case errors.Is(err, privacycontract.ErrPrivateMemoryManifest):
		return "manifest_mismatch"
	case errors.Is(err, privacycontract.ErrPrivateMemoryClaimLost):
		return "claim_lost"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "database_operation"
	}
}

func stringValue(value *domain.MemorySpaceKind) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

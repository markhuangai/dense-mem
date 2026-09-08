package repository

import (
	"context"
	"errors"
	"log/slog"
	"time"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

var (
	ErrRememberReplay                    = knowledgecontract.ErrRememberReplay
	ErrRememberAttemptNotFound           = knowledgecontract.ErrRememberAttemptNotFound
	ErrRememberFailureRetentionDegraded  = knowledgecontract.ErrRememberFailureRetentionDegraded
	ErrRememberAttemptDiagnosticNotFound = knowledgepostgres.ErrRememberAttemptDiagnosticNotFound
	ErrRememberFailureArtifactNotFound   = knowledgepostgres.ErrRememberFailureArtifactNotFound
)

type RememberAttemptRecordInput = knowledgecontract.RememberAttemptRecordInput
type RememberAttempt = knowledgecontract.RememberAttempt
type RememberAttemptLookupInput = knowledgecontract.RememberAttemptLookupInput
type RememberAttemptLookup = knowledgecontract.RememberAttemptLookup
type RememberFailureArtifactInput = knowledgecontract.RememberFailureArtifactInput
type RememberFailureRecordInput = knowledgecontract.RememberFailureRecordInput
type RememberAttemptDiagnosticFilter = knowledgecontract.RememberAttemptDiagnosticFilter
type RememberAttemptDiagnosticRecord = knowledgecontract.RememberAttemptDiagnosticRecord
type RememberAttemptDiagnosticEvent = knowledgecontract.RememberAttemptDiagnosticEvent
type RememberFailureArtifactDescriptor = knowledgecontract.RememberFailureArtifactDescriptor
type RememberFailureArtifact = knowledgecontract.RememberFailureArtifact
type RememberAttemptDiagnosticRecordPage = knowledgecontract.RememberAttemptDiagnosticRecordPage

type RememberAttemptDiagnosticsRepository interface {
	ListRememberAttemptDiagnostics(context.Context, RememberAttemptDiagnosticFilter) (*RememberAttemptDiagnosticRecordPage, error)
	GetRememberAttemptDiagnostic(context.Context, string, string) (*RememberAttemptDiagnosticRecord, error)
	GetRememberFailureArtifact(context.Context, string, string, string) (*RememberFailureArtifact, error)
	PurgeExpiredRememberFailureArtifacts(context.Context, int) (int, error)
}

var _ RememberAttemptDiagnosticsRepository = (*LedgerRepositoryImpl)(nil)

const (
	maxRememberFailureArtifactBytes       = 256 * 1024
	maxRememberFailureArtifactRetention   = 7 * 24 * time.Hour
	rememberFailureArtifactPurgeBatchSize = 100
)

func (r *LedgerRepositoryImpl) LoadRememberAttempt(ctx context.Context, input RememberAttemptLookupInput) (*RememberAttempt, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, errors.New("ledger: knowledge write owner is required")
	}
	return owner.LoadRememberAttempt(ctx, input)
}

func (r *LedgerRepositoryImpl) RecordRememberAttempt(ctx context.Context, input RememberAttemptRecordInput) error {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return errors.New("ledger: knowledge write owner is required")
	}
	return owner.RecordRememberAttempt(ctx, input)
}

func (r *LedgerRepositoryImpl) RecordRememberFailure(ctx context.Context, input RememberFailureRecordInput) error {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return errors.New("ledger: knowledge write owner is required")
	}
	return owner.RecordRememberFailure(ctx, input)
}

func (r *LedgerRepositoryImpl) ListRememberAttemptDiagnostics(ctx context.Context, filter RememberAttemptDiagnosticFilter) (*RememberAttemptDiagnosticRecordPage, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, errors.New("ledger: knowledge write owner is required")
	}
	return owner.ListRememberAttemptDiagnostics(ctx, filter)
}

func (r *LedgerRepositoryImpl) GetRememberAttemptDiagnostic(ctx context.Context, teamID, attemptID string) (*RememberAttemptDiagnosticRecord, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, errors.New("ledger: knowledge write owner is required")
	}
	return owner.GetRememberAttemptDiagnostic(ctx, teamID, attemptID)
}

func (r *LedgerRepositoryImpl) GetRememberFailureArtifact(ctx context.Context, teamID, attemptID, artifactID string) (*RememberFailureArtifact, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, errors.New("ledger: knowledge write owner is required")
	}
	return owner.GetRememberFailureArtifact(ctx, teamID, attemptID, artifactID)
}

func (r *LedgerRepositoryImpl) PurgeExpiredRememberFailureArtifacts(ctx context.Context, batchSize int) (int, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return 0, errors.New("ledger: knowledge write owner is required")
	}
	return owner.PurgeExpiredRememberFailureArtifacts(ctx, batchSize)
}

func (r *LedgerRepositoryImpl) StartRememberFailureArtifactPurger(ctx context.Context, interval time.Duration, logger *slog.Logger) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return
	}
	owner.StartRememberFailureArtifactPurger(ctx, interval, logger)
}

func normalizeRememberAttemptRecord(input RememberAttemptRecordInput) RememberAttemptRecordInput {
	return knowledgepostgres.NormalizeRememberAttemptRecord(input)
}

func validateRememberAttemptRecord(input RememberAttemptRecordInput) error {
	return knowledgepostgres.ValidateRememberAttemptRecord(input)
}

func rememberAttemptPhase(input RememberAttemptRecordInput) string {
	return knowledgepostgres.RememberAttemptPhase(input)
}

func rememberAttemptEventKind(input RememberAttemptRecordInput) string {
	return knowledgepostgres.RememberAttemptEventKind(input)
}

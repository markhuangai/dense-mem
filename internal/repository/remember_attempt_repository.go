package repository

import (
	"context"
	"errors"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

var (
	ErrRememberReplay                    = knowledgecontract.ErrRememberReplay
	ErrRememberAttemptNotFound           = knowledgecontract.ErrRememberAttemptNotFound
	ErrRememberFailureRetentionDegraded  = knowledgecontract.ErrRememberFailureRetentionDegraded
	ErrRememberAttemptDiagnosticNotFound = knowledgepostgres.ErrRememberAttemptDiagnosticNotFound
)

type RememberAttemptRecordInput = knowledgecontract.RememberAttemptRecordInput
type RememberAttempt = knowledgecontract.RememberAttempt
type RememberAttemptLookupInput = knowledgecontract.RememberAttemptLookupInput
type RememberAttemptLookup = knowledgecontract.RememberAttemptLookup
type RememberFailureRecordInput = knowledgecontract.RememberFailureRecordInput
type RememberAttemptDiagnosticInput = knowledgecontract.RememberAttemptDiagnosticInput
type RememberAttemptDiagnosticFilter = knowledgecontract.RememberAttemptDiagnosticFilter
type RememberAttemptDiagnosticRecord = knowledgecontract.RememberAttemptDiagnosticRecord
type RememberAttemptDiagnosticEvent = knowledgecontract.RememberAttemptDiagnosticEvent
type RememberAttemptDiagnosticRecordItem = knowledgecontract.RememberAttemptDiagnosticRecordItem
type RememberAttemptDiagnosticRecordPage = knowledgecontract.RememberAttemptDiagnosticRecordPage

type RememberAttemptDiagnosticsRepository interface {
	ListRememberAttemptDiagnostics(context.Context, RememberAttemptDiagnosticFilter) (*RememberAttemptDiagnosticRecordPage, error)
	GetRememberAttemptDiagnostic(context.Context, string, string) (*RememberAttemptDiagnosticRecord, error)
}

var _ RememberAttemptDiagnosticsRepository = (*LedgerRepositoryImpl)(nil)

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

func (r *LedgerRepositoryImpl) PurgeExpiredRememberAttemptDiagnostics(ctx context.Context, batchSize int) (int, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return 0, errors.New("ledger: knowledge write owner is required")
	}
	return owner.PurgeExpiredRememberAttemptDiagnostics(ctx, batchSize)
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

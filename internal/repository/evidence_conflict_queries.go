package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"

	conflictcontract "github.com/markhuangai/dense-mem/internal/conflict/contract"
	conflictpostgres "github.com/markhuangai/dense-mem/internal/conflict/postgres"
)

func (r *LedgerRepositoryImpl) ListEvidenceConflicts(ctx context.Context, input EvidenceConflictListInput) (*EvidenceConflictListResult, error) {
	return r.ConflictStore().ListEvidenceConflicts(ctx, input)
}

func (r *LedgerRepositoryImpl) GetEvidenceConflict(ctx context.Context, input EvidenceConflictGetInput) (*EvidenceConflictGetResult, error) {
	return r.ConflictStore().GetEvidenceConflict(ctx, input)
}

func (r *LedgerRepositoryImpl) ResolveEvidenceConflict(ctx context.Context, input EvidenceConflictResolutionInput) (*EvidenceConflictCaseRecord, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, errors.New("ledger: knowledge write owner is required")
	}
	return owner.ResolveEvidenceConflict(ctx, input)
}

func loadEvidenceConflictPositions(ctx context.Context, tx *gorm.DB, teamID, conflictID string) ([]EvidenceConflictPositionRecord, error) {
	return conflictpostgres.LoadEvidenceConflictPositions(ctx, tx, teamID, conflictID)
}

func loadEvidenceConflictEvents(ctx context.Context, tx *gorm.DB, input EvidenceConflictGetInput, _ string) ([]EvidenceConflictEventRecord, *EvidenceConflictEventCursor, error) {
	return conflictpostgres.LoadEvidenceConflictEvents(ctx, tx, input)
}

var _ conflictcontract.EvidenceConflictRepository = (*LedgerRepositoryImpl)(nil)

package repository

import (
	"context"
	"database/sql"
	"errors"

	"gorm.io/gorm"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

var (
	ErrIdempotencyConflict        = knowledgecontract.ErrIdempotencyConflict
	ErrSourceRevisionConflict     = knowledgecontract.ErrSourceRevisionConflict
	ErrEvidenceLifecycleNotFound  = knowledgecontract.ErrEvidenceLifecycleNotFound
	ErrEvidenceLifecycleConflict  = knowledgecontract.ErrEvidenceLifecycleConflict
	ErrEvidenceLifecycleIDInvalid = knowledgecontract.ErrEvidenceLifecycleIDInvalid
	ErrTeamInactive               = knowledgecontract.ErrTeamInactive
)

// LedgerRepository contains only durable, non-workflow operations. Semantic
// claiming and status mutation are intentionally not part of the runtime API.
type LedgerRepository interface {
	AdvanceSourceRevision(context.Context, AdvanceSourceRevisionInput) (*SourceRevisionResult, error)
	AppendSecurityEvent(context.Context, SecurityEventInput) (string, error)
}

// CreateIngestInput is the low-level evidence input used by conflict-derived
// evidence and synchronous commit helpers. It never creates placement state.
type CreateIngestInput = knowledgecontract.CreateIngestInput
type EvidenceInput = knowledgecontract.EvidenceInput
type SecurityEventDraft = knowledgecontract.SecurityEventDraft
type SecurityEventInput = knowledgecontract.SecurityEventInput
type SecuritySignalInput = knowledgecontract.SecuritySignalInput
type EvidenceIngestResult = knowledgecontract.EvidenceIngestResult
type EvidenceFragment = knowledgecontract.EvidenceFragment

type rLSHelper = postgres.RLSHelper

type LedgerRepositoryImpl struct {
	db                     *gorm.DB
	rls                    rLSHelper
	conflictReviewTTLDays  int
	conflictReviewTimezone string
	knowledgeOwner         *knowledgepostgres.Store
}

var _ LedgerRepository = (*LedgerRepositoryImpl)(nil)

func (r *LedgerRepositoryImpl) AppendSecurityEvent(ctx context.Context, input SecurityEventInput) (string, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return "", errors.New("ledger: knowledge write owner is required")
	}
	return owner.AppendSecurityEvent(ctx, input)
}

func (r *LedgerRepositoryImpl) withTeamProfileTx(ctx context.Context, teamID, profileID string, fn func(*gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("ledger: database is required")
	}
	if r.rls == nil {
		return errors.New("ledger: rls helper is required")
	}
	return r.rls.WithTeamProfileTx(ctx, r.db, teamID, profileID, func(tx *gorm.DB) error {
		if err := ensureActiveTeamForMutation(ctx, tx, teamID); err != nil {
			return err
		}
		return fn(tx)
	})
}

func (r *LedgerRepositoryImpl) withTeamTx(ctx context.Context, teamID string, fn func(*gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("ledger: database is required")
	}
	if r.rls == nil {
		return errors.New("ledger: rls helper is required")
	}
	return r.rls.WithTeamTx(ctx, r.db, teamID, func(tx *gorm.DB) error {
		if err := ensureActiveTeamForMutation(ctx, tx, teamID); err != nil {
			return err
		}
		return fn(tx)
	})
}

func (r *LedgerRepositoryImpl) withSystemTx(ctx context.Context, fn func(*gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("ledger: database is required")
	}
	if r.rls == nil {
		return errors.New("ledger: rls helper is required")
	}
	return r.rls.WithSystemTx(ctx, r.db, fn)
}

func ensureActiveTeamForMutation(ctx context.Context, tx *gorm.DB, teamID string) error {
	row := tx.WithContext(ctx).Raw(`
		SELECT id::text
		FROM teams
		WHERE id = ?::uuid
		  AND status = 'active'
		  AND deleted_at IS NULL
		FOR SHARE
	`, teamID).Row()
	var id string
	if err := row.Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTeamInactive
		}
		return err
	}
	return nil
}

func insertKnowledgeIngest(ctx context.Context, tx *gorm.DB, input CreateIngestInput) (string, bool, error) {
	return knowledgepostgres.InsertKnowledgeIngestTx(ctx, knowledgepostgres.LegacyTransaction(tx), input)
}

func insertEvidenceFragment(ctx context.Context, tx *gorm.DB, input CreateIngestInput, ingestID string, index int, item EvidenceInput, source *SourceRevisionResult) (EvidenceFragment, error) {
	return knowledgepostgres.InsertEvidenceFragmentTx(ctx, knowledgepostgres.LegacyTransaction(tx), input, ingestID, index, item, source)
}

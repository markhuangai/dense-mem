package repository

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

type AdvanceSourceRevisionInput = knowledgecontract.AdvanceSourceRevisionInput
type SourceRevisionResult = knowledgecontract.SourceRevisionResult

func (r *LedgerRepositoryImpl) AdvanceSourceRevision(ctx context.Context, input AdvanceSourceRevisionInput) (*SourceRevisionResult, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, errors.New("ledger: knowledge write owner is required")
	}
	return owner.AdvanceSourceRevision(ctx, input)
}

func normalizeAdvanceSourceRevisionInput(input AdvanceSourceRevisionInput) AdvanceSourceRevisionInput {
	return knowledgepostgres.NormalizeAdvanceSourceRevisionInput(input)
}

func validateAdvanceSourceRevisionInput(input AdvanceSourceRevisionInput) error {
	return knowledgepostgres.ValidateAdvanceSourceRevisionInput(input)
}

// advanceSourceRevisionInTx is a compatibility helper used by legacy database
// fixtures. The source revision SQL remains in the knowledge PostgreSQL owner.
func advanceSourceRevisionInTx(ctx context.Context, tx *gorm.DB, input AdvanceSourceRevisionInput, cache map[string]SourceRevisionResult) (*SourceRevisionResult, error) {
	return knowledgepostgres.AdvanceSourceRevisionTx(ctx, knowledgepostgres.LegacyTransaction(tx), input, cache)
}

func sourceKindForEvidence(sourceType string) string {
	switch strings.TrimSpace(sourceType) {
	case "document":
		return "document"
	case "manual":
		return "manual"
	case "observation":
		return "integration"
	default:
		return "conversation"
	}
}

// withSystemModeInTx is a shared RLS transition used by legacy adapters that
// are outside the knowledge write-owner migration.
func withSystemModeInTx(ctx context.Context, tx *gorm.DB, teamID, profileID string, fn func(systemTx *gorm.DB) error) error {
	if err := tx.WithContext(ctx).Exec("SELECT set_config('app.current_team_id', '', true)").Error; err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Exec("SELECT set_config('app.current_profile_id', '', true)").Error; err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Exec("SELECT set_config('app.tx_mode', 'system', true)").Error; err != nil {
		return err
	}
	fnErr := fn(tx)
	resetErr := resetProfileModeInTx(ctx, tx, teamID, profileID)
	if fnErr != nil {
		return fnErr
	}
	return resetErr
}

func resetProfileModeInTx(ctx context.Context, tx *gorm.DB, teamID, profileID string) error {
	if err := tx.WithContext(ctx).Exec("SELECT set_config('app.current_team_id', ?, true)", teamID).Error; err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Exec("SELECT set_config('app.current_profile_id', ?, true)", profileID).Error; err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec("SELECT set_config('app.tx_mode', 'profile', true)").Error
}

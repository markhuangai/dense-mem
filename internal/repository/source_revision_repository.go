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

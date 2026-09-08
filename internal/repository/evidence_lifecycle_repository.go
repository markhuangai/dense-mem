package repository

import (
	"context"
	"errors"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

type RetractEvidenceInput = knowledgecontract.RetractEvidenceInput
type EvidenceLifecycleResult = knowledgecontract.EvidenceLifecycleResult

func (r *LedgerRepositoryImpl) RetractEvidence(ctx context.Context, input RetractEvidenceInput) (*EvidenceLifecycleResult, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, errors.New("ledger: knowledge write owner is required")
	}
	return owner.RetractEvidence(ctx, input)
}

package memoryservice

import (
	"context"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	lifecycleapp "github.com/markhuangai/dense-mem/internal/lifecycle"
)

// Lifecycle names remain single-hop aliases while callers migrate to the
// capability-owned application package.
type LifecycleService = lifecycleapp.LifecycleService
type LifecycleDependencies = lifecycleapp.LifecycleDependencies
type LifecycleSemanticRepository = knowledgecontract.LifecyclePort
type LifecycleCorrectionExecutor = lifecycleapp.LifecycleCorrectionExecutor
type LifecycleEvidenceRepository = knowledgecontract.LifecyclePort
type CorrectRelationshipRequest = lifecycleapp.CorrectRelationshipRequest
type CorrectRelationshipReceipt = lifecycleapp.CorrectRelationshipReceipt
type RetractEvidenceRequest = lifecycleapp.RetractEvidenceRequest
type RetractEvidenceResult = lifecycleapp.RetractEvidenceResult

var (
	ErrLifecycleAuthContext          = lifecycleapp.ErrLifecycleAuthContext
	ErrLifecyclePersistence          = lifecycleapp.ErrLifecyclePersistence
	ErrLifecycleEmbeddingUnavailable = lifecycleapp.ErrLifecycleEmbeddingUnavailable
	ErrLifecycleEmbeddingInvalid     = lifecycleapp.ErrLifecycleEmbeddingInvalid
	ErrLifecycleEmbeddingTimeout     = lifecycleapp.ErrLifecycleEmbeddingTimeout
)

const CorrectionConfirmationInvalidReason = lifecycleapp.CorrectionConfirmationInvalidReason

type lifecycleService struct {
	delegate lifecycleapp.LifecycleService
}

func (s *lifecycleService) CorrectRelationship(ctx context.Context, req CorrectRelationshipRequest) (*CorrectRelationshipReceipt, error) {
	return s.delegate.CorrectRelationship(ctx, req)
}

func (s *lifecycleService) RetractEvidence(ctx context.Context, req RetractEvidenceRequest) (*RetractEvidenceResult, error) {
	return s.delegate.RetractEvidence(ctx, req)
}

func NewLifecycleService(deps LifecycleDependencies) LifecycleService {
	return &lifecycleService{delegate: lifecycleapp.NewLifecycleService(deps)}
}

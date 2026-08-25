package memoryservice

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/markhuangai/dense-mem/internal/repository"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

func (s *submissionAssessmentPlacementWorkerService) commitSubmissionAssessment(
	ctx context.Context,
	input repository.CommitSubmissionAssessmentInput,
) (*repository.CommitSubmissionAssessmentResult, error) {
	if s.inlineEmbedder == nil {
		return s.assessments.CommitSubmissionAssessment(ctx, input)
	}
	inlineCommitter, ok := s.assessments.(repository.InlineSubmissionAssessmentCommitter)
	if !ok {
		return nil, errors.New("submission assessment worker: inline semantic committer is unavailable")
	}
	plan, err := inlineCommitter.PlanSubmissionAssessmentEmbeddings(ctx, input)
	if err == nil && plan == nil {
		err = errors.New("submission assessment worker: nil inline embedding plan")
	}
	if err == nil {
		var embedded []repository.SearchDocumentEmbedding
		embedded, err = s.inlineEmbedder(ctx, plan.Documents)
		if err == nil {
			results := make([]repository.InlineEmbeddingResult, 0, len(embedded))
			for _, embedding := range embedded {
				results = append(results, repository.InlineEmbeddingResult{
					DocumentHash: embedding.DocumentHash,
					Embedding:    append([]float32(nil), embedding.Embedding...),
				})
			}
			commitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			committed, commitErr := inlineCommitter.CommitSubmissionAssessmentWithEmbeddings(commitCtx, input, results)
			if errors.Is(commitErr, repository.ErrInlineEmbeddingPlanTooLarge) {
				commitErr = fmt.Errorf("%w: synchronous embedding plan exceeds 256 documents", rememberapp.ErrRememberInputBudgetExceeded)
			} else if errors.Is(commitErr, repository.ErrInlineEmbeddingPlanMismatch) {
				commitErr = fmt.Errorf("%w: rendered search state changed before commit", rememberapp.ErrRememberCommitConflict)
			}
			return committed, commitErr
		}
	}
	if errors.Is(err, repository.ErrInlineEmbeddingPlanTooLarge) {
		err = fmt.Errorf("%w: synchronous embedding plan exceeds 256 documents", rememberapp.ErrRememberInputBudgetExceeded)
	} else if errors.Is(err, repository.ErrInlineEmbeddingPlanMismatch) {
		err = fmt.Errorf("%w: rendered search state changed before commit", rememberapp.ErrRememberCommitConflict)
	}
	return nil, err
}

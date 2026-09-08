package repository

import (
	"time"

	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

func conflictNextReviewAt(now, due time.Time) time.Time {
	return knowledgepostgres.ConflictNextReviewAt(now, due)
}

func normalizeConflictReviewRunInput(input ConflictReviewRunInput) (ConflictReviewRunInput, error) {
	return knowledgepostgres.NormalizeConflictReviewRunInput(input)
}

func validateClaimConflictDerivedEvidenceTasksInput(input ClaimConflictDerivedEvidenceTasksInput) error {
	return knowledgepostgres.ValidateClaimConflictDerivedEvidenceTasksInput(input)
}

package repository

import (
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

// Submission-assessment commit validation is owned by the PostgreSQL write
// owner. These aliases preserve the legacy repository test and fixture names
// until the #382 facade cleanup.
func normalizeRememberCommitScope(scope RememberCommitScope) RememberCommitScope {
	return RememberCommitScope(knowledgepostgres.NormalizeRememberCommitScope(knowledgecontract.RememberCommitScope(scope)))
}

func normalizeCommitSubmissionAssessmentInput(input CommitSubmissionAssessmentInput) CommitSubmissionAssessmentInput {
	return fromKnowledgeCommitSubmissionAssessmentInput(knowledgepostgres.NormalizeCommitSubmissionAssessmentInput(toKnowledgeCommitSubmissionAssessmentInput(input)))
}

func validateCommitSubmissionAssessmentInput(input CommitSubmissionAssessmentInput) error {
	return knowledgepostgres.ValidateCommitSubmissionAssessmentInput(toKnowledgeCommitSubmissionAssessmentInput(input))
}

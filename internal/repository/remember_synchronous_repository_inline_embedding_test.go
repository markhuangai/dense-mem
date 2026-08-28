package repository

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func configureSynchronousRememberEntityReuse(t *testing.T, repo *LedgerRepositoryImpl, input *SynchronousRememberCommitInput, entity *EntityRecord) {
	t.Helper()
	require.NotNil(t, input)
	require.NotNil(t, entity)
	originalBuildCommit := input.BuildCommit
	input.BuildCommit = func(created *CreateIngestResult, scope SubmissionAssessmentRunScope) (PersistSubmissionAssessmentInput, CommitSubmissionAssessmentInput, error) {
		persist, commit, err := originalBuildCommit(created, scope)
		if err != nil {
			return PersistSubmissionAssessmentInput{}, CommitSubmissionAssessmentInput{}, err
		}
		for index := range commit.EntityResolutions {
			if commit.EntityResolutions[index].Resolution.MentionRef != "orion" {
				continue
			}
			commit.EntityResolutions[index].Resolution.Action = string(domain.EntityResolutionReuse)
			commit.EntityResolutions[index].Resolution.EntityID = entity.EntityID
			commit.EntityResolutions[index].Resolution.ExactEntityID = entity.EntityID
			commit.EntityResolutions[index].Resolution.EntityKind = entity.EntityKind
			commit.EntityResolutions[index].Resolution.CanonicalName = entity.CanonicalName
		}
		return persist, commit, nil
	}
	prepareSynchronousRememberInline(t, repo, input.CreateIngest, input)
}

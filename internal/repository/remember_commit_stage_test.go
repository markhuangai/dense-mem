package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

func TestRememberCommitFailureStageExtractsOwnerError(t *testing.T) {
	teamID, ownerID, ingestID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	fragmentID, assessmentID := uuid.NewString(), uuid.NewString()
	input := knowledgepostgres.SynchronousRememberCommitInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingestID,
		IdempotencyKey: "owner-stage", RequestHash: "sha256:owner-stage",
		AssessmentID: assessmentID, AssessmentJSON: []byte(`{}`),
		Evidence: []knowledgepostgres.EvidenceInput{{FragmentID: fragmentID}},
		EvidenceSecurityResults: []knowledgepostgres.EvidenceSecurityResult{{
			FragmentID: fragmentID, EvidenceIndex: 0, Decision: "pass", Safe: true,
		}},
		Commit: knowledgepostgres.CommitSubmissionAssessmentInput{
			RememberCommitScope: knowledgepostgres.RememberCommitScope{
				TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingestID,
			},
			AssessmentID: assessmentID,
			Items:        []knowledgepostgres.SubmissionAssessmentItemInput{{FragmentID: fragmentID}},
		},
	}

	_, err := knowledgepostgres.NewStore(nil, nil, knowledgepostgres.ConflictRuntimeConfig{}).
		CommitRememberWithEmbeddings(context.Background(), input, nil)
	require.Error(t, err)
	require.Equal(t, "transaction_setup", RememberCommitFailureStage(err))
}

func TestIsRememberStaleInputErrorClassifiesAdapterBoundaries(t *testing.T) {
	for _, stale := range []error{
		knowledgepostgres.ErrConflictContextStale,
		knowledgepostgres.ErrRememberExactReferenceStale,
		knowledgepostgres.ErrCorrectionTargetStale,
	} {
		require.True(t, IsRememberStaleInputError(stale), stale)
		require.True(t, IsRememberStaleInputError(errors.Join(errors.New("wrapped"), stale)), stale)
	}
	require.True(t, IsRememberStaleInputError(knowledgecontract.ErrSourceRevisionConflict))
	require.False(t, IsRememberStaleInputError(errors.New("fresh input")))
}

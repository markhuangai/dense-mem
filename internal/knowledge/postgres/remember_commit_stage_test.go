package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
)

func TestRememberCommitFailureStageExtractsOwnerError(t *testing.T) {
	teamID, ownerID, ingestID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	fragmentID, assessmentID := uuid.NewString(), uuid.NewString()
	input := SynchronousRememberCommitInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingestID,
		IdempotencyKey: "owner-stage", RequestHash: "sha256:owner-stage",
		AssessmentID: assessmentID, AssessmentJSON: []byte(`{}`),
		Evidence: []EvidenceInput{{FragmentID: fragmentID}},
		EvidenceSecurityResults: []EvidenceSecurityResult{{
			FragmentID: fragmentID, EvidenceIndex: 0, Decision: "pass", Safe: true,
		}},
		Commit: CommitSubmissionAssessmentInput{
			RememberCommitScope: RememberCommitScope{
				TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingestID,
			},
			AssessmentID: assessmentID,
			Items:        []SubmissionAssessmentItemInput{{FragmentID: fragmentID}},
		},
	}

	_, err := NewStore(nil, nil, ConflictRuntimeConfig{}).
		CommitRememberWithEmbeddings(context.Background(), input, nil)
	require.Error(t, err)
	require.Equal(t, "transaction_setup", RememberCommitFailureStage(err))
}

func TestIsRememberStaleInputErrorClassifiesAdapterBoundaries(t *testing.T) {
	for _, stale := range []error{
		ErrConflictContextStale,
		ErrRememberExactReferenceStale,
		ErrCorrectionTargetStale,
	} {
		require.True(t, rememberapp.IsRememberStaleInputError(stale), stale)
		require.True(t, rememberapp.IsRememberStaleInputError(errors.Join(errors.New("wrapped"), stale)), stale)
	}
	require.True(t, rememberapp.IsRememberStaleInputError(knowledgecontract.ErrSourceRevisionConflict))
	require.False(t, rememberapp.IsRememberStaleInputError(errors.New("fresh input")))
}

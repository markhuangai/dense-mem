package serverapp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

func TestRememberStageResultPreservesRelationshipResults(t *testing.T) {
	result := rememberStageResult(&repository.CreateIngestResult{
		RelationshipResults: []repository.SubmissionRelationshipResult{{
			RelationshipRef: "submitted-ref",
			Disposition:     "stored",
			Splits: []repository.SubmissionRelationshipSplitInput{{
				SplitIndex: 0, RelationshipID: "relationship-1", RelationshipVersion: 2, Status: "active",
			}},
		}},
	})

	require.Len(t, result.RelationshipResults, 1)
	require.Equal(t, "submitted-ref", result.RelationshipResults[0].RelationshipRef)
	require.Equal(t, "relationship-1", result.RelationshipResults[0].Splits[0].RelationshipID)
}

func TestRememberServiceCompatPreservesEmptyStatusArrays(t *testing.T) {
	compat := newRememberServiceCompat(rememberServiceCompatStub{})

	result, err := compat.GetSubmissionStatus(context.Background(), memoryservice.GetSubmissionStatusRequest{SubmissionID: "submission"})

	require.NoError(t, err)
	require.NotNil(t, result.Evidence)
	require.NotNil(t, result.Errors)
	require.NotNil(t, result.Degradations)
	require.Len(t, result.RelationshipResults, 1)
	require.Equal(t, "relationship-1", result.RelationshipResults[0].Splits[0].RelationshipID)
	require.Empty(t, result.Evidence)
	require.Empty(t, result.Errors)
	require.Empty(t, result.Degradations)
}

type rememberServiceCompatStub struct{}

func (rememberServiceCompatStub) Remember(context.Context, rememberapp.RememberRequest) (*rememberapp.RememberResult, error) {
	return nil, nil
}

func (rememberServiceCompatStub) GetSubmissionStatus(context.Context, rememberapp.GetSubmissionStatusRequest) (*rememberapp.SubmissionStatusResult, error) {
	return &rememberapp.SubmissionStatusResult{
		Evidence:     []rememberapp.SubmissionEvidenceStatus{},
		Errors:       []rememberapp.SubmissionStatusError{},
		Degradations: []rememberapp.SubmissionStatusDegradation{},
		RelationshipResults: []rememberapp.SubmissionRelationshipResult{{
			RelationshipRef: "submitted-ref",
			Disposition:     "stored",
			Splits: []rememberapp.SubmissionRelationshipSplit{{
				SplitIndex: 0, RelationshipID: "relationship-1", RelationshipVersion: 1, Status: "active",
			}},
		}},
	}, nil
}

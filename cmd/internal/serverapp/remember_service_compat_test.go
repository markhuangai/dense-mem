package serverapp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

func TestRememberServiceCompatPreservesEmptyStatusArrays(t *testing.T) {
	compat := newRememberServiceCompat(rememberServiceCompatStub{})

	result, err := compat.GetSubmissionStatus(context.Background(), memoryservice.GetSubmissionStatusRequest{SubmissionID: "submission"})

	require.NoError(t, err)
	require.NotNil(t, result.Evidence)
	require.NotNil(t, result.Errors)
	require.Empty(t, result.Evidence)
	require.Empty(t, result.Errors)
}

type rememberServiceCompatStub struct{}

func (rememberServiceCompatStub) Remember(context.Context, rememberapp.RememberRequest) (*rememberapp.RememberResult, error) {
	return nil, nil
}

func (rememberServiceCompatStub) GetSubmissionStatus(context.Context, rememberapp.GetSubmissionStatusRequest) (*rememberapp.SubmissionStatusResult, error) {
	return &rememberapp.SubmissionStatusResult{
		Evidence: []rememberapp.SubmissionEvidenceStatus{},
		Errors:   []rememberapp.SubmissionStatusError{},
	}, nil
}

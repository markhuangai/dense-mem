package serverapp

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/embedding"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

func TestRememberFailureCodeMapsEmbeddingProviderResponseInvalid(t *testing.T) {
	failure := &rememberEmbeddingProviderFailure{cause: &embedding.ProviderError{
		FailureCode:  "provider_response_invalid",
		FailureClass: "provider_action_required",
	}}

	require.Equal(t, rememberapp.SubmissionErrorEmbeddingResponseInvalid, rememberFailureCode("embedding", failure))
}

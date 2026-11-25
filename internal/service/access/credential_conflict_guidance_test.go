package access

import (
	accesscontract "github.com/markhuangai/dense-mem/internal/access/contract"
	"testing"

	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/stretchr/testify/require"
)

func TestCredentialNameConflictIncludesCorrectionGuidance(t *testing.T) {
	err := credentialNameConflict(accesscontract.ErrCredentialNameConflict, "name")
	var apiErr *httperr.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "credential_name_conflict", apiErr.ReasonCode)
	require.Equal(t, "correct_and_resubmit", apiErr.NextAction)
	require.False(t, apiErr.Retryable)
}

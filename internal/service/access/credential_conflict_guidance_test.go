package access

import (
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/stretchr/testify/require"
)

func TestCredentialNameConflictIncludesCorrectionGuidance(t *testing.T) {
	err := credentialNameConflict(&pgconn.PgError{Code: "23505"}, "name")
	var apiErr *httperr.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "credential_name_conflict", apiErr.ReasonCode)
	require.Equal(t, "correct_and_resubmit", apiErr.NextAction)
	require.False(t, apiErr.Retryable)
}

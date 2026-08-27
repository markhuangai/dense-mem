package remember

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRememberValidationErrorMessageFallbacks(t *testing.T) {
	var nilError *RememberValidationError
	require.Equal(t, "remember validation failed", nilError.Error())
	require.Equal(t, "remember validation failed", (&RememberValidationError{}).Error())
	require.Equal(t, "bounded message", (&RememberValidationError{
		Issues: []RememberValidationIssue{{Message: "bounded message"}},
	}).Error())
}

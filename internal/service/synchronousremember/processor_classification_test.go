package synchronousremember

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/repository"
	remember "github.com/markhuangai/dense-mem/internal/service/remember"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
)

func TestSynchronousFailureCodeClassifiesSearchContractMismatch(t *testing.T) {
	for _, test := range []struct {
		name  string
		phase string
		cause error
		want  remember.TerminalErrorCode
	}{
		{name: "search contract during planning", phase: "embedding", cause: repository.ErrSearchContractMismatch, want: remember.TerminalErrorConfigurationInvalid},
		{name: "search contract during commit", phase: "commit", cause: repository.ErrSearchContractMismatch, want: remember.TerminalErrorConfigurationInvalid},
		{name: "embedding provider configuration", phase: "embedding", cause: semanticwrite.ErrProviderConfiguration, want: remember.TerminalErrorConfigurationInvalid},
		{name: "inactive reused entity", phase: "embedding", cause: repository.ErrRememberExactReferenceStale, want: remember.TerminalErrorStaleInput},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, synchronousFailureCode(test.phase, test.cause))
		})
	}
}

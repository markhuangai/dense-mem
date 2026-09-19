package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

func TestRememberInvocationDiagnosticValidationSeparatesClassificationAndBounds(t *testing.T) {
	base := knowledgecontract.RememberInvocationDiagnosticInput{
		TeamID:         "11111111-1111-4111-8111-111111111111",
		OwnerProfileID: "22222222-2222-4222-8222-222222222222",
		InvocationID:   "33333333-3333-4333-8333-333333333333",
		Classification: "replay", Outcome: "replayed",
		CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, validateRememberInvocationDiagnostic(base))

	conflict := base
	conflict.Classification = "conflict"
	conflict.Outcome = "conflict"
	require.NoError(t, validateRememberInvocationDiagnostic(conflict))
	negativeDuration := base
	negativeDuration.Duration = -time.Millisecond
	require.ErrorContains(t, validateRememberInvocationDiagnostic(negativeDuration), "duration cannot be negative")

	tooLarge := base
	tooLarge.RequestBody = []byte(strings.Repeat("x", 16<<20+1))
	require.ErrorContains(t, validateRememberInvocationDiagnostic(tooLarge), "body exceeds")

	badExchange := base
	badExchange.ProviderExchanges = []knowledgecontract.RememberAttemptDiagnosticInput{{
		RequestBody: []byte(strings.Repeat("x", 16<<20+1)),
	}}
	require.ErrorContains(t, validateRememberInvocationDiagnostic(badExchange), "provider exchange")
}

func TestRememberInvocationExchangeEncodingRetainsAdmittedBodies(t *testing.T) {
	exchanges, err := rememberInvocationExchanges([]knowledgecontract.RememberAttemptDiagnosticInput{{
		SequenceNo: 1, Kind: "provider_exchange", Component: "assessor",
		RequestBody:  []byte(`{"messages":[{"content":"admitted text"}]}`),
		ResponseBody: []byte(`{"error":"database unavailable"}`), CaptureState: "captured",
	}})
	require.NoError(t, err)
	require.Len(t, exchanges, 1)
	require.Contains(t, exchanges[0].RequestBody, "admitted text")
	require.Contains(t, exchanges[0].ResponseBody, "database unavailable")
}

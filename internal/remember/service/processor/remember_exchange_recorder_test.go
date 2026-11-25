package processor

import (
	"strings"
	"testing"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/stretchr/testify/require"
)

func TestBoundRememberDiagnosticItemsStopsAfterAggregateBudget(t *testing.T) {
	body := []byte(strings.Repeat("x", modelprovider.MaxProviderDiagnosticBodyBytes))
	items := make([]knowledgecontract.RememberAttemptDiagnosticInput, 5)
	for index := range items {
		items[index] = knowledgecontract.RememberAttemptDiagnosticInput{
			RequestBody:  body,
			CaptureState: "captured",
		}
	}

	boundRememberDiagnosticItems(items)
	require.Len(t, items[0].RequestBody, modelprovider.MaxProviderDiagnosticBodyBytes)
	require.Empty(t, items[4].RequestBody)
	require.Equal(t, "truncated", items[4].CaptureState)
}

package e2eapp

import (
	"testing"

	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
	"github.com/stretchr/testify/require"
)

func TestRememberInputHasFaultUsesFixtureMarker(t *testing.T) {
	input := rememberapp.RememberProcessRequest{Evidence: []rememberapp.EvidenceInput{{Content: "safe [fixture-fault:search-generation-rotation]"}}}
	require.True(t, rememberInputHasFault(input, synchronousSearchGenerationFault))
	require.False(t, rememberInputHasFault(input, synchronousSupersessionFault))
	require.False(t, rememberInputHasFault(rememberapp.RememberProcessRequest{Evidence: []rememberapp.EvidenceInput{{Content: "safe"}}}, synchronousSearchGenerationFault))
}

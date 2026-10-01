package contract

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestHashPreservesLengthPrefixedBytes(t *testing.T) {
	cases := []struct {
		name  string
		parts []string
		want  string
	}{
		{"no parts", nil, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"empty part", []string{""}, "ba768b331fd86cec803be04e56ab2b3d4c0e98ef4ee4fcd4e72ad7cce61a1d1f"},
		{"unicode byte length", []string{"é"}, "2f23c71856587c2a6ffdb64f0e11e9940b35da318947308f6a02ee01898e6107"},
		{"ab then c", []string{"ab", "c"}, "430fb1b4ac43316eca81fab27a1930ab8eff8fef6a1dc7903dce44bbc2790dc5"},
		{"a then bc", []string{"a", "bc"}, "5310a58788781ab25d5ad7c3f85035824b4eb7bdfa394e0ac2186271472b5492"},
		{"credential delete scope", []string{"team-credential-delete", "11111111-1111-4111-8111-111111111111", "33333333-3333-4333-8333-333333333333"}, "663757424438ef700e5e2176a9e3b417fcf6f26962151c7b00ed783b4d25d1de"},
		{"credential delete request", []string{"retire-credential", "11111111-1111-4111-8111-111111111111", "33333333-3333-4333-8333-333333333333"}, "1d3cbd360a8c3b7b1516d21f3392f6799d3cacf16dc43ef2723e1f046d23009a"},
		{"retention scope", []string{"retention-operation", "55555555-5555-4555-8555-555555555555", "66666666-6666-4666-8666-666666666666"}, "90e1ead7bb2ac6bafe7242292a7754ef9853d38754d57cfaba77703ae3c4edb9"},
		{"retention request", []string{"retention_purge", "66666666-6666-4666-8666-666666666666", "1"}, "8f04c431236d290ec51bd00c8ca574f2b1182629cf2fddd543969c32a6496bec"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, Hash(tc.parts...)) })
	}
}

func TestDecideRetryPreservesAttemptBoundaryAndDelay(t *testing.T) {
	require.Equal(t, 5, MaximumAttempts)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		attempt int
		delay   time.Duration
	}{
		{1, time.Second}, {2, 2 * time.Second}, {3, 4 * time.Second}, {4, 8 * time.Second},
	} {
		decision := DecideRetry(tc.attempt, now)
		require.Equal(t, domain.PrivateMemoryErasureQueued, decision.Status)
		require.NotNil(t, decision.NextAttemptAt)
		require.Equal(t, now.Add(tc.delay), *decision.NextAttemptAt)
		require.Equal(t, tc.delay, RetryDelay(tc.attempt))
	}
	for _, attempt := range []int{5, 6} {
		decision := DecideRetry(attempt, now)
		require.Equal(t, domain.PrivateMemoryErasureFailed, decision.Status)
		require.Nil(t, decision.NextAttemptAt)
	}
	require.Equal(t, time.Minute, RetryDelay(7))
}

package conflictread

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeConflictUUIDListDropsInvalidAndDuplicateValues(t *testing.T) {
	first := "00000000-0000-0000-0000-000000000001"
	second := "00000000-0000-0000-0000-000000000002"
	require.Equal(t, []string{first, second}, normalizeConflictUUIDList([]string{" ", first, first, "not-a-uuid", second}))
}

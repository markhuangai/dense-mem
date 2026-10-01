package recall

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecallCommunityLimitsPreserveDisablingAndPositiveNestedBounds(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		input                      *int
		communities, relationships int
	}{
		{"omitted", nil, 3, 5}, {"zero", intPointer(0), 0, 1}, {"negative", intPointer(-1), 0, 1},
		{"one", intPointer(1), 1, 1}, {"maximum", intPointer(20), 10, 20}, {"excessive", intPointer(100), 10, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			normalized := normalizeRecallRequest(RecallRequest{CommunityLimit: tc.input, CommunityRelationshipLimit: tc.input})
			require.Equal(t, tc.communities, *normalized.CommunityLimit)
			require.Equal(t, tc.relationships, *normalized.CommunityRelationshipLimit)
		})
	}
}

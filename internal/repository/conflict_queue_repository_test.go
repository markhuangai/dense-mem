package repository

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestConflictQueueItemFromRecordReportsProjectionTruncation(t *testing.T) {
	item := conflictQueueItemFromRecord(conflictQueueCaseRow{
		Record: RelationshipConflictCaseRecord{
			ConflictID:   "conflict-1",
			Status:       "open",
			Question:     strings.Repeat("q", 513),
			PredicateKey: strings.Repeat("p", 257),
			Positions: []RelationshipConflictPositionRecord{{
				PositionID:         "position-1",
				PositionKey:        "position",
				PositionCount:      domain.ConflictQueueMaxPositions + 1,
				PositionsTruncated: true,
			}},
		},
	}, time.Now().UTC())

	require.True(t, item.QuestionTruncated)
	require.True(t, item.PredicateKeyTruncated)
	require.True(t, item.PositionsTruncated)
	require.Len(t, []rune(item.Question), 512)
	require.Len(t, []rune(item.PredicateKey), 256)
}

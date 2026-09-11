package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"gorm.io/gorm"
)

func TestConflictAdapterRejectsInvalidInputsBeforeOpeningDatabase(t *testing.T) {
	store := NewStore(nil, nil, nil)
	ctx := context.Background()

	_, err := store.ListEvidenceConflicts(ctx, EvidenceConflictListInput{TeamID: "not-a-uuid", Limit: 1})
	require.Error(t, err)
	_, err = store.ListEvidenceConflicts(ctx, EvidenceConflictListInput{TeamID: "00000000-0000-0000-0000-000000000001", Status: "invalid", Limit: 1})
	require.ErrorIs(t, err, ErrEvidenceConflictInvalidCommand)
	_, err = store.ListEvidenceConflicts(ctx, EvidenceConflictListInput{TeamID: "00000000-0000-0000-0000-000000000001", Limit: EvidenceConflictMaxLimit + 1})
	require.ErrorIs(t, err, ErrEvidenceConflictInvalidCommand)
	_, err = store.ListEvidenceConflicts(ctx, EvidenceConflictListInput{
		TeamID: "00000000-0000-0000-0000-000000000001", Status: "open", Limit: 1,
		Cursor: &EvidenceConflictCursor{Version: 1, TeamID: "different", StatusFilter: "open"},
	})
	require.Error(t, err)

	_, err = store.GetEvidenceConflict(ctx, EvidenceConflictGetInput{TeamID: "not-a-uuid", ConflictID: "not-a-uuid", EventLimit: 1})
	require.Error(t, err)
	_, err = store.GetEvidenceConflict(ctx, EvidenceConflictGetInput{TeamID: "00000000-0000-0000-0000-000000000001", ConflictID: "00000000-0000-0000-0000-000000000002", EventLimit: EvidenceConflictMaxEventLimit + 1})
	require.ErrorIs(t, err, ErrEvidenceConflictInvalidCommand)

	_, err = store.ListConflictQueue(ctx, domain.ConflictQueueQuery{TeamID: "not-a-uuid", Limit: 1})
	require.Error(t, err)
	_, err = store.ListConflictQueue(ctx, domain.ConflictQueueQuery{TeamID: "00000000-0000-0000-0000-000000000001", Status: "resolved", Limit: 1})
	require.Error(t, err)
	_, err = store.ListConflictQueue(ctx, domain.ConflictQueueQuery{TeamID: "00000000-0000-0000-0000-000000000001", Limit: 0})
	require.Error(t, err)
	_, err = store.ListConflictQueue(ctx, domain.ConflictQueueQuery{TeamID: "00000000-0000-0000-0000-000000000001", Limit: 1})
	require.Error(t, err)
	_, err = store.ListConflictQueue(ctx, domain.ConflictQueueQuery{
		TeamID: "00000000-0000-0000-0000-000000000001", Limit: 1,
		Cursor: &domain.ConflictQueueCursor{Version: 0},
	})
	require.Error(t, err)

	_, _, err = loadEvidenceConflictEvents(ctx, nil, EvidenceConflictGetInput{
		TeamID: "00000000-0000-0000-0000-000000000001", ConflictID: "00000000-0000-0000-0000-000000000002", EventLimit: 1,
		EventCursor: &EvidenceConflictEventCursor{Version: 0},
	}, "")
	require.Error(t, err)
	_, err = store.ResolveEvidenceConflict(ctx, EvidenceConflictResolutionInput{})
	require.Error(t, err)
}

func TestNormalizeRecallUUIDListDropsInvalidAndDuplicateValues(t *testing.T) {
	first := "00000000-0000-0000-0000-000000000001"
	second := "00000000-0000-0000-0000-000000000002"
	require.Equal(t, []string{first, second}, normalizeRecallUUIDList([]string{" ", first, first, "not-a-uuid", second}))
}

func TestConflictAdapterValidInputsStillRequireInfrastructure(t *testing.T) {
	teamID := "00000000-0000-0000-0000-000000000001"
	conflictID := "00000000-0000-0000-0000-000000000002"
	store := NewStore(&gorm.DB{}, nil, nil)
	ctx := context.Background()

	for _, status := range []string{"open", "resolved", "dismissed"} {
		_, err := store.ListEvidenceConflicts(ctx, EvidenceConflictListInput{TeamID: teamID, Status: status})
		require.Error(t, err)
	}
	_, err := store.GetEvidenceConflict(ctx, EvidenceConflictGetInput{TeamID: teamID, ConflictID: conflictID})
	require.Error(t, err)

	cursor := &domain.ConflictQueueCursor{
		Version: 1, TeamID: teamID, Status: "open", StatusFilter: "open",
		NextReviewAt: time.Now().UTC(), ConflictID: conflictID,
	}
	_, err = store.ListConflictQueue(ctx, domain.ConflictQueueQuery{TeamID: teamID, Status: "open", Limit: 1, Cursor: cursor})
	require.Error(t, err)
}

//go:build integration

package postgres

import (
	"sync"
	"testing"

	"github.com/google/uuid"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
)

func TestOntologyManagementCompetingSavesKeepLosingEdits(t *testing.T) {
	f := newOntologyFixture(t)
	service := managementService(f)
	ctx := f.actor(0, "manager")
	input := managementInput(0, topic.ManagementEdit{Action: "define", Kind: topic.Topic, Definition: &topic.Definition{Key: "save-race", Label: "Save race"}})
	draft, err := service.SaveDraft(ctx, "", input)
	require.NoError(t, err)
	var group sync.WaitGroup
	errors := make([]error, 2)
	for i := range 2 {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			next := input
			next.OperationKey = uuid.NewString()
			next.ExpectedVersion = draft.Version
			next.Reason = uuid.NewString()
			_, errors[index] = service.SaveDraft(ctx, draft.ID, next)
		}(i)
	}
	group.Wait()
	if errors[0] == nil {
		require.ErrorIs(t, errors[1], topic.ErrConflict)
	} else {
		require.ErrorIs(t, errors[0], topic.ErrConflict)
		require.NoError(t, errors[1])
	}
	stored, err := service.Draft(ctx, draft.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, stored.Version)
	require.Equal(t, "open", stored.State)
}

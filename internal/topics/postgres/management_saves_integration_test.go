//go:build integration

package postgres

import (
	"errors"
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
	save := func(edits ...topic.ManagementEdit) (topic.PublicationResult, error) {
		return service.SaveEdits(ctx, topic.SaveEditsInput{OperationKey: uuid.NewString(), Reason: "Validate concurrent manager edits", Edits: edits})
	}
	define := func(key string) topic.ManagementEdit {
		return topic.ManagementEdit{Action: "define", Kind: topic.Topic, Definition: &topic.Definition{Key: key, Label: key}}
	}
	race := func(edits ...topic.ManagementEdit) {
		t.Helper()
		var group sync.WaitGroup
		outcomes := make(chan error, len(edits))
		for _, edit := range edits {
			group.Go(func() { _, err := save(edit); outcomes <- err })
		}
		group.Wait()
		close(outcomes)
		success, rejected := 0, 0
		for err := range outcomes {
			switch {
			case err == nil:
				success++
			case errors.Is(err, topic.ErrConflict), errors.Is(err, topic.ErrInvalid):
				rejected++
			default:
				t.Fatalf("unexpected concurrent edit result: %v", err)
			}
		}
		require.Equal(t, 1, success)
		require.Equal(t, 1, rejected)
	}
	first, second := define("crossed-first"), define("crossed-second")
	a, err := save(first)
	require.NoError(t, err)
	b, err := save(second)
	require.NoError(t, err)
	first.RecordID, first.ExpectedVersion, first.Definition.ParentID = a.Records[0].ID, a.Records[0].Version, b.Records[0].ID
	second.RecordID, second.ExpectedVersion, second.Definition.ParentID = b.Records[0].ID, b.Records[0].Version, a.Records[0].ID
	race(first, second)
	firstHead, err := service.Record(ctx, first.RecordID, 0)
	require.NoError(t, err)
	secondHead, err := service.Record(ctx, second.RecordID, 0)
	require.NoError(t, err)
	require.True(t, (firstHead.Definition.ParentID == second.RecordID) != (secondHead.Definition.ParentID == first.RecordID))
	race(define("simultaneous-name"), define("simultaneous-name"))
}

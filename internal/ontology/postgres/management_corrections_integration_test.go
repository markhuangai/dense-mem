//go:build integration

package postgres

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	application "github.com/markhuangai/dense-mem/internal/ontology"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func TestOntologyManagementPinUnpinRollbackAndHistory(t *testing.T) {
	f := newOntologyFixture(t)
	service := managementService(f)
	ctx := f.actor(0, "manager")
	topic := testTopic("storage")
	seed, err := f.store.PublishAutomatic(context.Background(), f.team, testPublication("seed", 0, ontology.Change{Record: topic}))
	require.NoError(t, err)
	before := f.canonicalSnapshot(t)
	definition := *topic.Definition
	definition.Label = "Durable storage"
	definition.Description = "Manager-maintained label"
	definition.Aliases = []string{"database platform"}
	draft, err := service.SaveDraft(ctx, "", managementInput(seed.Revision, ontology.ManagementEdit{Action: "define", RecordID: topic.ID, ExpectedVersion: 1, Kind: ontology.Topic, Definition: &definition}))
	require.NoError(t, err)
	preview, err := service.Preview(ctx, draft.ID, draft.Version)
	require.NoError(t, err)
	require.Equal(t, 2, preview.ChangedRecords)
	pub, err := service.CommandDraft(ctx, draft.ID, "publish", managementCommand(draft))
	require.NoError(t, err)
	topic.Definition.Label = "Automatic overwrite"
	_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("pinned", pub.Publication.Revision, ontology.Change{ExpectedVersion: 2, Record: topic}))
	require.ErrorIs(t, err, ontology.ErrOverride)
	page, err := service.Records(ctx, ontology.OverrideKind, "", 200)
	require.NoError(t, err)
	require.Len(t, page.Records, 1)
	pin := page.Records[0]
	unpin, err := service.SaveDraft(ctx, "", managementInput(page.Revision, ontology.ManagementEdit{Action: "remove_override", RecordID: pin.ID, ExpectedVersion: pin.Version}))
	require.NoError(t, err)
	unpinned, err := service.CommandDraft(ctx, unpin.ID, "publish", managementCommand(unpin))
	require.NoError(t, err)
	rolled, err := service.Rollback(ctx, unpinned.Publication.ID, ontology.DraftCommand{OperationKey: "restore-pin", ExpectedRevision: unpinned.Publication.Revision, Reason: "Restore protection"})
	require.NoError(t, err)
	replayed, err := service.Rollback(ctx, unpinned.Publication.ID, ontology.DraftCommand{OperationKey: "restore-pin", ExpectedRevision: unpinned.Publication.Revision, Reason: "Restore protection"})
	require.NoError(t, err)
	require.True(t, replayed.Existing)
	require.Equal(t, rolled.ID, replayed.ID)
	_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("pinned-again", rolled.Revision, ontology.Change{ExpectedVersion: 2, Record: topic}))
	require.ErrorIs(t, err, ontology.ErrOverride)
	history, err := service.Publication(ctx, rolled.ID)
	require.NoError(t, err)
	require.Equal(t, unpinned.Publication.ID, history.RollbackOf)
	require.Len(t, history.RecordsView, 1)
	page1, err := service.Publications(ctx, 0, 1)
	require.NoError(t, err)
	require.Len(t, page1.Publications, 1)
	require.NotZero(t, page1.NextRevision)
	page2, err := service.Publications(ctx, page1.NextRevision, 200)
	require.NoError(t, err)
	require.Len(t, page2.Publications, 3)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyManagementPreviewsAreBoundedAndPreserveOtherGroups(t *testing.T) {
	f := newOrganizationFixture(t)
	service := managementService(f)
	ctx := f.actor(0, "manager")
	handles := []ontology.SourceHandle{}
	for range 12 {
		handles = append(handles, f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil))
	}
	other := []ontology.SourceHandle{f.organizationEvidence(t, 1, "Unrelated uses Redis.", nil), f.organizationEvidence(t, 1, "Unrelated uses Redis.", nil)}
	organizer, calls := organizationFixtureService(t, f, func(assessment.Item, assessment.Item) bool { return true }, nil)
	_, err := organizer.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "group-target", Sources: handles})
	require.NoError(t, err)
	_, err = organizer.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "group-other", Sources: other})
	require.NoError(t, err)
	otherPage, err := f.store.ListRecords(ctx, f.team, ontology.EvidenceGroup, "", 200)
	require.NoError(t, err)
	var otherID string
	for _, record := range otherPage.Records {
		for _, member := range record.Group.Members {
			if member.ID == other[0].ID {
				otherID = record.ID
			}
		}
	}
	require.NotEmpty(t, otherID)
	before := f.canonicalSnapshot(t)
	priorCalls := calls.Load()
	draft, err := service.SaveDraft(ctx, "", managementInput(otherPage.Revision, ontology.ManagementEdit{Action: "keep_separate", Members: handles}))
	require.NoError(t, err)
	preview, err := service.Preview(ctx, draft.ID, draft.Version)
	require.NoError(t, err)
	require.EqualValues(t, 12, preview.AffectedSources)
	require.Len(t, preview.Examples, 10)
	require.True(t, preview.ExamplesTruncated)
	require.True(t, preview.Complete)
	_, err = service.CommandDraft(ctx, draft.ID, "publish", managementCommand(draft))
	require.NoError(t, err)
	require.Equal(t, priorCalls, calls.Load())
	view, err := service.Record(ctx, otherID, 0)
	require.NoError(t, err)
	require.True(t, view.Current)
	status, err := service.Status(ctx)
	require.NoError(t, err)
	require.Positive(t, status.PendingMarkers)
	require.False(t, status.CoverageComplete)
	topics, err := service.Records(ctx, ontology.Topic, "", 200)
	require.NoError(t, err)
	require.Len(t, topics.Records, 1)
	definition := *topics.Records[0].Definition
	definition.Label = "Shared storage topic"
	definitionDraft, err := service.SaveDraft(ctx, "", managementInput(topics.Revision, ontology.ManagementEdit{Action: "define", RecordID: topics.Records[0].ID, ExpectedVersion: topics.Records[0].Version, Kind: ontology.Topic, Definition: &definition}))
	require.NoError(t, err)
	definitionPreview, err := service.Preview(ctx, definitionDraft.ID, definitionDraft.Version)
	require.NoError(t, err)
	require.EqualValues(t, 14, definitionPreview.AffectedSources)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyManagementRejectsCyclesAndExplicitReplacementCollisions(t *testing.T) {
	f := newOrganizationFixture(t)
	service := managementService(f)
	ctx := f.actor(0, "manager")
	a, b := testTopic("parent"), testTopic("child")
	b.Definition.ParentID = a.ID
	_, err := f.store.PublishAutomatic(context.Background(), f.team, testPublication("seed", 0, ontology.Change{Record: a}, ontology.Change{Record: b}))
	require.NoError(t, err)
	a.Definition.ParentID = b.ID
	_, err = service.SaveDraft(ctx, "", managementInput(1, ontology.ManagementEdit{Action: "define", RecordID: a.ID, ExpectedVersion: 1, Kind: ontology.Topic, Definition: a.Definition}))
	require.ErrorIs(t, err, ontology.ErrInvalid)
	_, err = service.SaveDraft(ctx, "", managementInput(1, ontology.ManagementEdit{Action: "define", Kind: ontology.Topic, Definition: &ontology.Definition{Key: "another", Label: "parent"}}))
	require.ErrorIs(t, err, ontology.ErrInvalid)
	handle := f.organizationEvidence(t, 1, "Atlas uses PostgreSQL.", nil)
	draft, err := service.SaveDraft(ctx, "", managementInput(1, ontology.ManagementEdit{Action: "classify", Source: &handle, DefinitionID: b.ID}))
	require.NoError(t, err)
	pub, err := service.CommandDraft(ctx, draft.ID, "publish", managementCommand(draft))
	require.NoError(t, err)
	_, err = service.SaveDraft(ctx, "", managementInput(pub.Publication.Revision, ontology.ManagementEdit{Action: "classify", Source: &handle, DefinitionID: b.ID}))
	require.ErrorIs(t, err, ontology.ErrInvalid)
	page, err := service.Records(ctx, ontology.OverrideKind, "", 200)
	require.NoError(t, err)
	require.Len(t, page.Records, 1)
	replacement, err := service.SaveDraft(ctx, "", managementInput(page.Revision, ontology.ManagementEdit{Action: "classify", RecordID: page.Records[0].ID, ExpectedVersion: page.Records[0].Version, Source: &handle, DefinitionID: b.ID}))
	require.NoError(t, err)
	_, err = service.CommandDraft(ctx, replacement.ID, "publish", managementCommand(replacement))
	require.NoError(t, err)
	_, err = service.Rollback(ctx, pub.Publication.ID, ontology.DraftCommand{OperationKey: uuid.NewString(), ExpectedRevision: page.Revision + 1, Reason: "Cannot overwrite later correction"})
	require.ErrorIs(t, err, ontology.ErrConflict)
}

func TestOntologyManagementCorrectionWhileMaintenanceInFlight(t *testing.T) {
	f := newOrganizationFixture(t)
	manager := managementService(f)
	handles := []ontology.SourceHandle{f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil), f.organizationEvidence(t, 1, "Atlas uses PostgreSQL for data.", nil)}
	config := maintenanceSettings(t, f)
	var once sync.Once
	var correction ontology.ManagementDraft
	var publicationError error
	worker, calls := maintenanceServiceFixture(t, f, config, func(_ assessment.Request, _ *assessment.Response) {
		once.Do(func() {
			page, err := manager.Records(f.actor(0, "manager"), "", "", 1)
			if err != nil {
				publicationError = err
				return
			}
			draft, err := manager.SaveDraft(f.actor(0, "manager"), "", managementInput(page.Revision, ontology.ManagementEdit{Action: "keep_separate", Members: handles}))
			if err != nil {
				publicationError = err
				return
			}
			correction, publicationError = manager.CommandDraft(f.actor(0, "manager"), draft.ID, "publish", managementCommand(draft))
		})
	})
	for range 20 {
		_, runErr := worker.RunTurn(context.Background())
		if runErr != nil {
			var failure *application.OrganizationError
			require.ErrorAs(t, runErr, &failure)
			require.Contains(t, []string{"commit_conflict", "stale_input"}, failure.Code)
		}
		if calls() > 0 {
			break
		}
	}
	require.Positive(t, calls())
	require.NoError(t, publicationError)
	require.Equal(t, "published", correction.State)
	page, err := manager.Records(f.actor(0, "manager"), ontology.OverrideKind, "", 200)
	require.NoError(t, err)
	require.Len(t, page.Records, 1)
	require.Equal(t, ontology.KeepSeparate, page.Records[0].Override.Action)
	for _, record := range page.Records {
		require.False(t, record.Retired)
	}
	read, err := f.store.ReadOrganization(context.Background(), f.team, handles)
	require.NoError(t, err)
	for _, record := range read.Records {
		if record.Current && record.Group != nil {
			require.NotEqual(t, len(handles), len(record.Group.Members))
		}
	}
}

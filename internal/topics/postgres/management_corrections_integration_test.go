//go:build integration

package postgres

import (
	"context"
	"sync"
	"testing"

	"github.com/markhuangai/dense-mem/internal/topics/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/topics/contract"
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
	require.Equal(t, 1, preview.ChangedRecords)
	pub, err := service.CommandDraft(ctx, draft.ID, "publish", managementCommand(draft))
	require.NoError(t, err)
	page, err := service.Records(ctx, ontology.OverrideKind, "", 200)
	require.NoError(t, err)
	require.Empty(t, page.Records)
	rolled, err := service.Rollback(ctx, pub.Publication.ID, ontology.DraftCommand{OperationKey: "restore-definition", ExpectedRevision: pub.Publication.Revision, Reason: "Restore prior label"})
	require.NoError(t, err)
	replayed, err := service.Rollback(ctx, pub.Publication.ID, ontology.DraftCommand{OperationKey: "restore-definition", ExpectedRevision: pub.Publication.Revision, Reason: "Restore prior label"})
	require.NoError(t, err)
	require.True(t, replayed.Existing)
	require.Equal(t, rolled.ID, replayed.ID)
	history, err := service.Publication(ctx, rolled.ID)
	require.NoError(t, err)
	require.Equal(t, pub.Publication.ID, history.RollbackOf)
	require.Len(t, history.RecordsView, 1)
	require.Equal(t, topic.Definition.Label, history.RecordsView[0].Definition.Label)
	page1, err := service.Publications(ctx, 0, 1)
	require.NoError(t, err)
	require.Len(t, page1.Publications, 1)
	require.NotZero(t, page1.NextRevision)
	page2, err := service.Publications(ctx, page1.NextRevision, 200)
	require.NoError(t, err)
	require.Len(t, page2.Publications, 2)
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
	_, err = service.SaveDraft(ctx, "", managementInput(otherPage.Revision, ontology.ManagementEdit{Action: "keep_separate", Members: handles}))
	require.ErrorIs(t, err, ontology.ErrInvalid)
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
	require.Len(t, definitionPreview.Examples, 10)
	require.True(t, definitionPreview.ExamplesTruncated)
	require.True(t, definitionPreview.Complete)
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
	_, err = service.SaveDraft(ctx, "", managementInput(1, ontology.ManagementEdit{Action: "classify", Source: &handle, DefinitionID: b.ID}))
	require.ErrorIs(t, err, ontology.ErrInvalid)
	page, err := service.Records(ctx, ontology.OverrideKind, "", 200)
	require.NoError(t, err)
	require.Empty(t, page.Records)

}

func TestOntologyManagementCorrectionWhileMaintenanceInFlight(t *testing.T) {
	f := newOrganizationFixture(t)
	manager := managementService(f)
	handles := []ontology.SourceHandle{f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil), f.organizationEvidence(t, 1, "Atlas uses PostgreSQL for data.", nil)}
	config := maintenanceSettings(t, f)
	var once sync.Once
	var publicationError error
	worker, calls := maintenanceServiceFixture(t, f, config, func(_ assessment.Request, _ *assessment.Response) {
		once.Do(func() {
			page, err := manager.Records(f.actor(0, "manager"), "", "", 1)
			if err != nil {
				publicationError = err
				return
			}
			_, publicationError = manager.SaveDraft(f.actor(0, "manager"), "", managementInput(page.Revision, ontology.ManagementEdit{Action: "keep_separate", Members: handles}))
		})
	})
	for range 20 {
		_, runErr := worker.RunTurn(context.Background())
		require.NoError(t, runErr)
		if calls() > 0 {
			break
		}
	}
	require.Positive(t, calls())
	require.ErrorIs(t, publicationError, ontology.ErrInvalid)
	page, err := manager.Records(f.actor(0, "manager"), ontology.OverrideKind, "", 200)
	require.NoError(t, err)
	require.Empty(t, page.Records)
	drafts, err := manager.Drafts(f.actor(0, "manager"), "", 20)
	require.NoError(t, err)
	require.Empty(t, drafts.Drafts)
}

//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func semanticHierarchyRecord(key string, kind ontology.Kind, parent string, relation ontology.ParentRelation) ontology.Record {
	record := testTopic(key)
	record.Kind = kind
	record.Definition.ParentID, record.Definition.ParentRelation = parent, relation
	if kind == ontology.EntityClass {
		record.Definition.BaseEntityKind = "person"
	}
	return record
}

func TestOntologySemanticHierarchyPublication(t *testing.T) {
	f := newOntologyFixture(t)
	before := f.canonicalSnapshot(t)
	revision := int64(0)
	for _, kind := range []ontology.Kind{ontology.EntityClass, ontology.PredicateConcept} {
		relation := ontology.SubclassOf
		if kind == ontology.PredicateConcept {
			relation = ontology.SubpropertyOf
		}
		parent := semanticHierarchyRecord(string(kind)+"-parent", kind, "", "")
		child := semanticHierarchyRecord(string(kind)+"-child", kind, parent.ID, "")
		initial, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(uuid.NewString(), revision, ontology.Change{Record: parent}, ontology.Change{Record: child}))
		require.NoError(t, err)
		legacy, err := f.store.GetRecord(f.actor(1, "member"), f.team, child.ID, 0)
		require.NoError(t, err)
		require.True(t, legacy.Current)
		require.Empty(t, legacy.Definition.ParentRelation)
		legacy.Definition.ParentRelation = relation
		input := testPublication(uuid.NewString(), initial.Revision, ontology.Change{ExpectedVersion: 1, Record: legacy.Record})
		published, err := f.store.PublishManager(f.actor(0, "manager"), f.team, input)
		require.NoError(t, err)
		replay, err := f.store.PublishManager(f.actor(0, "manager"), f.team, input)
		require.NoError(t, err)
		require.True(t, replay.Existing)
		require.Equal(t, published.ID, replay.ID)
		for version := int64(1); version <= 2; version++ {
			view, err := f.store.GetRecord(f.actor(1, "member"), f.team, child.ID, version)
			require.NoError(t, err)
			if version == 1 {
				require.Empty(t, view.Definition.ParentRelation)
			} else {
				require.Equal(t, relation, view.Definition.ParentRelation)
				require.NotEqual(t, legacy.Fingerprint, view.Fingerprint)
				require.True(t, view.Current)
			}
		}
		var legacyHasRelation bool
		require.NoError(t, f.admin.Raw(`SELECT jsonb_exists(body->'definition','parent_relation') FROM ontology_record_revisions WHERE team_id=?::uuid AND record_id=?::uuid AND version=1`, f.team, child.ID).Row().Scan(&legacyHasRelation))
		require.False(t, legacyHasRelation)
		revision = published.Revision
		for _, invalid := range []ontology.Record{
			semanticHierarchyRecord(uuid.NewString(), ontology.Topic, parent.ID, relation),
			semanticHierarchyRecord(uuid.NewString(), kind, "", relation),
			semanticHierarchyRecord(uuid.NewString(), kind, parent.ID, "unknown_relation"),
		} {
			valid := testTopic("must-not-commit-" + uuid.NewString())
			_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(uuid.NewString(), revision, ontology.Change{Record: valid}, ontology.Change{Record: invalid}))
			require.ErrorIs(t, err, ontology.ErrInvalid)
			_, err = f.store.GetRecord(context.Background(), f.team, valid.ID, 0)
			require.ErrorIs(t, err, ontology.ErrNotFound)
		}
	}
	parentID := ""
	var changes []ontology.Change
	for depth := 0; depth <= ontology.MaxParentDepth; depth++ {
		relation := ontology.SubclassOf
		if parentID == "" {
			relation = ""
		}
		record := semanticHierarchyRecord(fmt.Sprintf("depth-%d", depth), ontology.EntityClass, parentID, relation)
		parentID = record.ID
		changes = append(changes, ontology.Change{Record: record})
		if len(changes) == 16 || depth == ontology.MaxParentDepth {
			published, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(uuid.NewString(), revision, changes...))
			require.NoError(t, err)
			revision, changes = published.Revision, nil
		}
	}
	for _, record := range []ontology.Record{
		semanticHierarchyRecord("too-deep", ontology.EntityClass, parentID, ontology.SubclassOf),
		semanticHierarchyRecord("cycle", ontology.EntityClass, "", ontology.SubclassOf),
	} {
		if record.Definition.ParentID == "" {
			record.Definition.ParentID = record.ID
		}
		_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(uuid.NewString(), revision, ontology.Change{Record: record}))
		require.ErrorIs(t, err, ontology.ErrInvalid)
	}
	page, err := f.store.ListRecords(context.Background(), f.team, "", "", 200)
	require.NoError(t, err)
	require.Equal(t, revision, page.Revision)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologySemanticHierarchyDependencies(t *testing.T) {
	f := newOntologyFixture(t)
	entity, err := f.knowledge.CreateEntity(context.Background(), knowledge.CreateEntityInput{TeamID: f.team, OwnerProfileID: f.owners[1], EntityKind: "person", CanonicalName: "Ada"})
	require.NoError(t, err)
	handle := ontology.SourceHandle{Kind: ontology.EntitySource, ID: entity.EntityID, Version: int64(entity.Version)}
	before := f.canonicalSnapshot(t)
	parent := semanticHierarchyRecord("parent", ontology.EntityClass, "", "")
	child := semanticHierarchyRecord("child", ontology.EntityClass, parent.ID, "")
	unrelated := semanticHierarchyRecord("unrelated", ontology.EntityClass, "", "")
	assignment := ontology.Record{ID: uuid.NewString(), Kind: ontology.AssignmentKind, Assignment: &ontology.Assignment{Source: handle, DefinitionID: child.ID}, Sources: []ontology.SourceDependency{f.source(t, handle)}}
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("initial", 0,
		ontology.Change{Record: parent}, ontology.Change{Record: child}, ontology.Change{Record: unrelated}, ontology.Change{Record: assignment}))
	require.NoError(t, err)
	original, err := f.store.GetRecord(context.Background(), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.True(t, original.Current)
	child.Definition.ParentRelation = ontology.SubclassOf
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("declare", 1, ontology.Change{ExpectedVersion: 1, Record: child}))
	require.NoError(t, err)
	stale, err := f.store.GetRecord(context.Background(), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.False(t, stale.Current)
	assignment = stale.Record
	assignment.Dependencies, assignment.Fingerprint = nil, ""
	_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("refresh-assignment", 2, ontology.Change{ExpectedVersion: 1, Record: assignment}))
	require.NoError(t, err)
	refreshed, err := f.store.GetRecord(context.Background(), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.True(t, refreshed.Current)
	require.Contains(t, refreshed.Dependencies, ontology.RevisionRef{ID: child.ID, Version: 2})
	require.Contains(t, refreshed.Dependencies, ontology.RevisionRef{ID: parent.ID, Version: 1})
	unrelated.Definition.Description = "Unrelated vocabulary change"
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("unrelated", 3, ontology.Change{ExpectedVersion: 1, Record: unrelated}))
	require.NoError(t, err)
	current, err := f.store.GetRecord(f.actor(1, "member"), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.True(t, current.Current)
	require.Equal(t, refreshed.Fingerprint, current.Fingerprint)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologySemanticHierarchyRollbackAndPins(t *testing.T) {
	f := newOntologyFixture(t)
	service, ctx := managementService(f), f.actor(0, "manager")
	before := f.canonicalSnapshot(t)
	revision := int64(0)
	for _, kind := range []ontology.Kind{ontology.EntityClass, ontology.PredicateConcept} {
		relation := ontology.SubclassOf
		if kind == ontology.PredicateConcept {
			relation = ontology.SubpropertyOf
		}
		parent := semanticHierarchyRecord(string(kind)+"-parent", kind, "", "")
		published, err := f.store.PublishManager(ctx, f.team, testPublication(uuid.NewString(), revision, ontology.Change{Record: parent}))
		require.NoError(t, err)
		child := semanticHierarchyRecord(string(kind)+"-child", kind, parent.ID, relation)
		draft, err := service.SaveDraft(ctx, "", managementInput(published.Revision, ontology.ManagementEdit{Action: "define", Kind: kind, Definition: child.Definition}))
		require.NoError(t, err)
		first, err := service.CommandDraft(ctx, draft.ID, "publish", managementCommand(draft))
		require.NoError(t, err)
		var childView ontology.RecordView
		for _, ref := range first.Publication.Records {
			view, err := f.store.GetRecord(ctx, f.team, ref.ID, 0)
			require.NoError(t, err)
			if view.Definition != nil {
				childView = view
			}
		}
		require.NotNil(t, childView.Definition)
		childView.Definition.Label = "Revised " + string(kind)
		edit := ontology.ManagementEdit{Action: "define", RecordID: childView.ID, ExpectedVersion: childView.Version, Kind: kind, Definition: childView.Definition}
		draft, err = service.SaveDraft(ctx, "", managementInput(first.Publication.Revision, edit))
		require.NoError(t, err)
		changed, err := service.CommandDraft(ctx, draft.ID, "publish", managementCommand(draft))
		require.NoError(t, err)
		rollbackInput := ontology.DraftCommand{OperationKey: uuid.NewString(), ExpectedRevision: changed.Publication.Revision, Reason: "Restore accepted definition"}
		rolled, err := service.Rollback(ctx, changed.Publication.ID, rollbackInput)
		require.NoError(t, err)
		replayed, err := service.Rollback(ctx, changed.Publication.ID, rollbackInput)
		require.NoError(t, err)
		require.True(t, replayed.Existing)
		require.Equal(t, rolled.ID, replayed.ID)
		current, err := f.store.GetRecord(f.actor(1, "member"), f.team, childView.ID, 0)
		require.NoError(t, err)
		require.Equal(t, relation, current.Definition.ParentRelation)
		require.Equal(t, parent.ID, current.Definition.ParentID)
		require.Equal(t, child.Definition.Label, current.Definition.Label)
		for version := int64(1); version <= 3; version++ {
			view, err := f.store.GetRecord(ctx, f.team, current.ID, version)
			require.NoError(t, err)
			require.Equal(t, relation, view.Definition.ParentRelation)
		}
		_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication(uuid.NewString(), rolled.Revision, ontology.Change{ExpectedVersion: current.Version, Record: current.Record}))
		require.ErrorIs(t, err, ontology.ErrOverride)
		cleared := *current.Definition
		cleared.ParentID = ""
		_, err = service.SaveDraft(ctx, "", managementInput(rolled.Revision, ontology.ManagementEdit{Action: "define", RecordID: current.ID, ExpectedVersion: current.Version, Kind: kind, Definition: &cleared}))
		require.ErrorIs(t, err, ontology.ErrInvalid)
		revision = rolled.Revision
	}
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologySemanticHierarchyRacesAndIsolation(t *testing.T) {
	f := newOntologyFixture(t)
	managementOwnerC(t, f)
	service := managementService(f)
	parent := semanticHierarchyRecord("shared-parent", ontology.EntityClass, "", "")
	_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("parent", 0, ontology.Change{Record: parent}))
	require.NoError(t, err)
	drafts := make([]ontology.ManagementDraft, 2)
	for owner := range drafts {
		child := semanticHierarchyRecord(fmt.Sprintf("owner-%d", owner), ontology.EntityClass, parent.ID, ontology.SubclassOf)
		drafts[owner], err = service.SaveDraft(f.actor(owner, "manager"), "", managementInput(1, ontology.ManagementEdit{Action: "define", Kind: child.Kind, Definition: child.Definition}))
		require.NoError(t, err)
	}
	for _, owner := range []int{1, 2} {
		_, err := service.Draft(f.actor(owner, "manager"), drafts[0].ID)
		require.ErrorIs(t, err, ontology.ErrNotFound)
	}
	results := make([]error, 2)
	start := make(chan struct{})
	var group sync.WaitGroup
	for owner := range drafts {
		group.Add(1)
		go func(owner int) {
			defer group.Done()
			<-start
			_, results[owner] = service.CommandDraft(f.actor(owner, "manager"), drafts[owner].ID, "publish", managementCommand(drafts[owner]))
		}(owner)
	}
	close(start)
	group.Wait()
	if results[0] == nil {
		require.ErrorIs(t, results[1], ontology.ErrConflict)
	} else {
		require.ErrorIs(t, results[0], ontology.ErrConflict)
		require.NoError(t, results[1])
	}
	page, err := service.Records(f.actor(2, "manager"), ontology.EntityClass, "", 200)
	require.NoError(t, err)
	require.Len(t, page.Records, 2)
	foreign := maintenanceOtherTeam(t, f)
	_, err = service.Draft(foreign.actor(0, "manager"), drafts[0].ID)
	require.ErrorIs(t, err, ontology.ErrNotFound)
	_, err = f.store.GetRecord(foreign.actor(0, "manager"), foreign.team, parent.ID, 0)
	require.ErrorIs(t, err, ontology.ErrNotFound)
	private := f.credentials[2]
	privateContext := requestctx.WithAllowedSpaces(context.Background(), []domain.MemorySpaceAccess{{ID: private.MemorySpaceID, Kind: domain.MemorySpacePrivate, Generation: private.MemorySpaceGeneration}})
	privateEvidence, err := f.knowledge.CreateIngestForTest(privateContext, knowledge.CreateIngestInput{TeamID: f.team, OwnerProfileID: private.ID.String(), SpaceID: private.MemorySpaceID.String(), SpaceGeneration: private.MemorySpaceGeneration, Evidence: []knowledge.EvidenceInput{{Content: "Private hierarchy canary"}}})
	require.NoError(t, err)
	foreignEvidence := foreign.evidence(t, 0, "Foreign hierarchy canary")
	shared := f.evidence(t, 0, "Historical hierarchy source")
	oldSource := f.source(t, ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: shared.Evidence[0].FragmentID, Version: 1})
	before := f.canonicalSnapshot(t)
	for _, id := range []string{privateEvidence.Evidence[0].FragmentID, foreignEvidence.Evidence[0].FragmentID} {
		record := semanticHierarchyRecord(uuid.NewString(), ontology.EntityClass, parent.ID, ontology.SubclassOf)
		handle := ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: id, Version: 1}
		_, readErr := f.store.ReadSources(context.Background(), f.team, []ontology.SourceHandle{handle})
		require.ErrorIs(t, readErr, ontology.ErrSourceStale)
		record.Sources = []ontology.SourceDependency{{SourceHandle: handle, Fingerprint: testHash("unavailable")}}
		_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(uuid.NewString(), 2, ontology.Change{Record: record}))
		require.ErrorIs(t, err, ontology.ErrSourceStale)
	}
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE memory_spaces SET generation=generation+1 WHERE team_id=?::uuid AND id=?::uuid`, f.team, f.space).Error
	}))
	newParent := semanticHierarchyRecord("new-generation-parent", ontology.EntityClass, "", "")
	child := semanticHierarchyRecord("old-source", ontology.EntityClass, newParent.ID, ontology.SubclassOf)
	child.Sources = []ontology.SourceDependency{oldSource}
	_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("old-generation", 0, ontology.Change{Record: newParent}, ontology.Change{Record: child}))
	require.True(t, errors.Is(err, ontology.ErrSourceStale), "%v", err)
	history, err := f.store.History(context.Background(), f.team, 0, 20)
	require.NoError(t, err)
	require.Empty(t, history)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

//go:build integration

package postgres

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyManagementPredicateLookupBeyondFirstPage(t *testing.T) {
	f := newOntologyFixture(t)
	ctx := f.actor(0, "manager")
	manager := managementService(f)
	subject := maintenanceEntity(t, f, "Atlas")
	object := maintenanceEntity(t, f, "PostgreSQL")
	text := "Atlas has independently supported storage classifications."
	evidence := f.evidence(t, 0, text)
	var last ontology.SourceHandle
	for index := range 51 {
		predicate, err := f.knowledge.EnsureSemanticReviewPredicateCandidate(context.Background(), knowledge.EnsureSemanticPredicateCandidateInput{TeamID: f.team, OwnerProfileID: f.owners[0], Predicate: fmt.Sprintf("ordered_storage_%03d", index), RelationshipKind: "state", SubjectKind: "project", ObjectKind: "project"})
		require.NoError(t, err)
		decision, err := f.knowledge.ApplyRelationshipDecision(context.Background(), knowledge.ApplyRelationshipDecisionInput{TeamID: f.team, OwnerProfileID: f.owners[0], IngestID: evidence.IngestID, SubjectEntityID: subject.ID, ObjectEntityID: object.ID, PredicateKey: predicate.PredicateKey, PredicateVersion: predicate.Version, Polarity: "+", Support: &knowledge.EvidenceSupportInput{FragmentID: evidence.Evidence[0].FragmentID, SourceGroupKey: uuid.NewString(), SpanEnd: len(text), Authority: "primary"}})
		require.NoError(t, err)
		require.NotNil(t, decision.Relationship)
		last = ontology.SourceHandle{Kind: ontology.PredicateSource, ID: predicate.PredicateKey, Version: int64(predicate.Version)}
	}
	before := f.canonicalSnapshot(t)
	first, err := manager.Sources(ctx, ontology.PredicateSource, "", "", 50)
	require.NoError(t, err)
	require.Len(t, first.Sources, 50)
	require.NotEmpty(t, first.NextCursor)
	for _, source := range first.Sources {
		require.NotEqual(t, last.ID, source.ID)
	}
	found, err := manager.Sources(ctx, ontology.PredicateSource, "", last.ID, 50)
	require.NoError(t, err)
	require.Len(t, found.Sources, 1)
	require.Equal(t, last, found.Sources[0].SourceHandle)
	require.Empty(t, found.NextCursor)
	filtered, err := manager.Sources(ctx, ontology.PredicateSource, "", "ordered_storage", 50)
	require.NoError(t, err)
	require.Equal(t, first, filtered)
	foreign := maintenanceOtherTeam(t, f)
	denied, err := manager.Sources(foreign.actor(0, "manager"), ontology.PredicateSource, "", last.ID, 50)
	require.NoError(t, err)
	require.Empty(t, denied.Sources)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyManagementRetiredGroupSplitsIntoDistinctRecords(t *testing.T) {
	f := newOrganizationFixture(t)
	handles := []ontology.SourceHandle{}
	for index := range 6 {
		handles = append(handles, f.organizationEvidence(t, 0, fmt.Sprintf("Atlas uses PostgreSQL. Occurrence %d.", index), nil))
	}
	slices.SortFunc(handles, func(a, b ontology.SourceHandle) int { return strings.Compare(a.ID, b.ID) })
	before := f.canonicalSnapshot(t)
	grouped, _ := organizationFixtureService(t, f, func(assessment.Item, assessment.Item) bool { return true }, nil)
	_, err := grouped.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "later-anchored-group", Sources: handles[4:]})
	require.NoError(t, err)
	original, err := f.store.ListRecords(context.Background(), f.team, ontology.EvidenceGroup, "", 20)
	require.NoError(t, err)
	require.Len(t, original.Records, 1)
	expanded, err := grouped.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "expanded-group", Sources: handles})
	require.NoError(t, err)
	manager := managementService(f)
	ctx := f.actor(0, "manager")
	draft, err := manager.SaveDraft(ctx, "", managementInput(expanded.Publication.Revision, ontology.ManagementEdit{Action: "keep_separate", Members: handles}))
	require.NoError(t, err)
	separated, err := manager.CommandDraft(ctx, draft.ID, "publish", managementCommand(draft))
	require.NoError(t, err)
	distinct, err := grouped.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "retired-group", Sources: handles})
	require.NoError(t, err)
	retired, err := f.store.GetRecord(context.Background(), f.team, original.Records[0].ID, 0)
	require.NoError(t, err)
	require.True(t, retired.Retired)
	draft, err = manager.SaveDraft(ctx, "", managementInput(distinct.Publication.Revision, ontology.ManagementEdit{Action: "remove_override", RecordID: separated.Publication.Records[0].ID, ExpectedVersion: separated.Publication.Records[0].Version}))
	require.NoError(t, err)
	_, err = manager.CommandDraft(ctx, draft.ID, "publish", managementCommand(draft))
	require.NoError(t, err)
	snapshots, err := f.store.ReadSources(context.Background(), f.team, handles)
	require.NoError(t, err)
	partition := map[string]int{}
	for index, snapshot := range snapshots {
		partition[ontology.SourceDisplayText(snapshot)] = index / 2
	}
	split, _ := organizationFixtureService(t, f, func(a, b assessment.Item) bool { return partition[a.Text] == partition[b.Text] }, nil)
	_, err = split.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "split-retired-group", Sources: handles})
	require.NoError(t, err)
	current, err := f.store.ListRecords(context.Background(), f.team, ontology.EvidenceGroup, "", 20)
	require.NoError(t, err)
	require.Len(t, current.Records, 3)
	seen := map[string]bool{}
	for _, group := range current.Records {
		require.True(t, group.Current)
		require.Len(t, group.Group.Members, 2)
		for _, member := range group.Group.Members {
			require.False(t, seen[member.ID])
			seen[member.ID] = true
		}
	}
	require.Len(t, seen, len(handles))
	historical, err := f.store.GetRecord(context.Background(), f.team, retired.ID, retired.Version)
	require.NoError(t, err)
	require.Equal(t, retired.Record, historical.Record)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyManagementEditAndUnpinTogether(t *testing.T) {
	f := newOntologyFixture(t)
	manager := managementService(f)
	ctx := f.actor(0, "manager")
	definition := &ontology.Definition{Key: "editable", Label: "Editable"}
	draft, err := manager.SaveDraft(ctx, "", managementInput(0, ontology.ManagementEdit{Action: "define", Kind: ontology.Topic, Definition: definition}))
	require.NoError(t, err)
	pub, err := manager.CommandDraft(ctx, draft.ID, "publish", managementCommand(draft))
	require.NoError(t, err)
	page, err := manager.Records(ctx, ontology.OverrideKind, "", 200)
	require.NoError(t, err)
	pin := page.Records[0]
	definition.Label = "Unpinned edit"
	draft, err = manager.SaveDraft(ctx, "", managementInput(pub.Publication.Revision, ontology.ManagementEdit{Action: "define", RecordID: pin.Override.TargetID, ExpectedVersion: 1, Kind: ontology.Topic, Definition: definition}, ontology.ManagementEdit{Action: "remove_override", RecordID: pin.ID, ExpectedVersion: pin.Version}))
	require.NoError(t, err)
	pub, err = manager.CommandDraft(ctx, draft.ID, "publish", managementCommand(draft))
	require.NoError(t, err)
	page, err = manager.Records(ctx, ontology.OverrideKind, "", 200)
	require.NoError(t, err)
	for _, record := range page.Records {
		require.True(t, record.Retired)
	}
	definition.Label = "Automatic edit after explicit unpin"
	_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("unpin-works", pub.Publication.Revision, ontology.Change{ExpectedVersion: 2, Record: ontology.Record{ID: pin.Override.TargetID, Kind: ontology.Topic, Definition: definition}}))
	require.NoError(t, err)
}

func TestOntologyManagementReplacesAdvancedSourceAndRemovesWithdrawnCorrection(t *testing.T) {
	f := newOrganizationFixture(t)
	manager := managementService(f)
	ctx := f.actor(0, "manager")
	source := maintenanceRelationship(t, f)
	topic := testTopic("support-version")
	_, err := f.store.PublishAutomatic(context.Background(), f.team, testPublication("topic", 0, ontology.Change{Record: topic}))
	require.NoError(t, err)
	draft, err := manager.SaveDraft(ctx, "", managementInput(1, ontology.ManagementEdit{Action: "classify", Source: &source, DefinitionID: topic.ID}))
	require.NoError(t, err)
	pub, err := manager.CommandDraft(ctx, draft.ID, "publish", managementCommand(draft))
	require.NoError(t, err)
	var subject, object, predicate string
	var predicateVersion int
	require.NoError(t, f.admin.Raw(`SELECT subject_entity_id::text,object_entity_id::text,predicate_key,predicate_version FROM relationship_records WHERE team_id=?::uuid AND relationship_id=?::uuid`, f.team, source.ID).Row().Scan(&subject, &object, &predicate, &predicateVersion))
	evidence := f.evidence(t, 0, "Another source confirms Atlas uses PostgreSQL.")
	decision, err := f.knowledge.ApplyRelationshipDecision(context.Background(), knowledge.ApplyRelationshipDecisionInput{TeamID: f.team, OwnerProfileID: f.owners[0], IngestID: evidence.IngestID, SubjectEntityID: subject, ObjectEntityID: object, PredicateKey: predicate, PredicateVersion: predicateVersion, Polarity: "+", Support: &knowledge.EvidenceSupportInput{FragmentID: evidence.Evidence[0].FragmentID, SourceGroupKey: uuid.NewString(), SpanEnd: len("Another source confirms Atlas uses PostgreSQL."), Authority: "primary"}})
	require.NoError(t, err)
	require.Equal(t, source.ID, decision.Relationship.RelationshipID)
	require.Greater(t, int64(decision.Relationship.Version), source.Version)
	page, err := manager.Records(ctx, ontology.OverrideKind, "", 200)
	require.NoError(t, err)
	override := page.Records[0]
	source.Version = int64(decision.Relationship.Version)
	draft, err = manager.SaveDraft(ctx, "", managementInput(pub.Publication.Revision, ontology.ManagementEdit{Action: "classify", RecordID: override.ID, ExpectedVersion: override.Version, Source: &source, DefinitionID: topic.ID}))
	require.NoError(t, err)
	_, err = manager.CommandDraft(ctx, draft.ID, "publish", managementCommand(draft))
	require.NoError(t, err)
	withdrawn := f.evidence(t, 0, "Withdraw this classification source.")
	handle := ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: withdrawn.Evidence[0].FragmentID, Version: 1}
	page, err = manager.Records(ctx, "", "", 1)
	require.NoError(t, err)
	draft, err = manager.SaveDraft(ctx, "", managementInput(page.Revision, ontology.ManagementEdit{Action: "classify", Source: &handle, DefinitionID: topic.ID}))
	require.NoError(t, err)
	pub, err = manager.CommandDraft(ctx, draft.ID, "publish", managementCommand(draft))
	require.NoError(t, err)
	_, err = f.knowledge.RetractEvidence(ctx, knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{handle.ID}, IdempotencyKey: uuid.NewString(), RequestHash: testHash("withdraw-correction-source"), Reason: "Withdraw source"})
	require.NoError(t, err)
	page, err = manager.Records(ctx, ontology.OverrideKind, "", 200)
	require.NoError(t, err)
	for _, record := range page.Records {
		if record.Override.Members[0].ID == handle.ID {
			override = record
		}
	}
	draft, err = manager.SaveDraft(ctx, "", managementInput(pub.Publication.Revision, ontology.ManagementEdit{Action: "remove_override", RecordID: override.ID, ExpectedVersion: override.Version}))
	require.NoError(t, err)
	pub, err = manager.CommandDraft(ctx, draft.ID, "publish", managementCommand(draft))
	require.NoError(t, err)
	_, err = manager.Rollback(ctx, pub.Publication.ID, ontology.DraftCommand{OperationKey: uuid.NewString(), ExpectedRevision: pub.Publication.Revision, Reason: "Unavailable sources cannot be restored"})
	require.ErrorIs(t, err, ontology.ErrSourceStale)
}

func TestOntologyManagementExcludesManagersOwnPrivateSpaces(t *testing.T) {
	f := newOntologyFixture(t)
	manager := managementService(f)
	ctx := f.actor(0, "manager")
	topic := testTopic("private-denial")
	_, err := f.store.PublishAutomatic(context.Background(), f.team, testPublication("topic", 0, ontology.Change{Record: topic}))
	require.NoError(t, err)
	for _, kind := range []domain.MemorySpaceKind{domain.MemorySpaceProfilePrivate, domain.MemorySpaceCredentialPrivate} {
		var id string
		var generation int64
		column := "owner_profile_id"
		if kind == domain.MemorySpaceCredentialPrivate {
			column = "owner_credential_id"
		}
		require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
			return tx.Raw(`INSERT INTO memory_spaces(team_id,kind,`+column+`) VALUES(?::uuid,?,?::uuid) RETURNING id::text,generation`, f.team, string(kind), f.owners[0]).Row().Scan(&id, &generation)
		}))
		space := domain.MemorySpaceAccess{ID: uuid.MustParse(id), Kind: kind, Generation: generation}
		privateContext := requestctx.WithAllowedSpaces(context.Background(), []domain.MemorySpaceAccess{space})
		ingest, err := f.knowledge.CreateIngestForTest(privateContext, knowledge.CreateIngestInput{TeamID: f.team, OwnerProfileID: f.owners[0], SpaceID: id, SpaceGeneration: generation, Evidence: []knowledge.EvidenceInput{{Content: "Manager's own private canary"}}})
		require.NoError(t, err)
		actor, _ := requestctx.ActorFromContext(ctx)
		actor.AllowedSpaces = append(actor.AllowedSpaces, space)
		authorized := requestctx.WithActor(context.Background(), actor)
		handle := ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: ingest.Evidence[0].FragmentID, Version: 1}
		page, err := manager.Sources(authorized, ontology.EvidenceSource, "", handle.ID, 50)
		require.NoError(t, err)
		require.Empty(t, page.Sources)
		_, err = manager.SaveDraft(authorized, "", managementInput(1, ontology.ManagementEdit{Action: "classify", Source: &handle, DefinitionID: topic.ID}))
		require.ErrorIs(t, err, ontology.ErrSourceStale)
		drafts, err := manager.Drafts(authorized, "", 50)
		require.NoError(t, err)
		require.Empty(t, drafts.Drafts)
	}
}

func TestOntologyManagementStatusCountsOnlyAuthenticatedTeam(t *testing.T) {
	f := newOrganizationFixture(t)
	other := maintenanceOtherTeam(t, f)
	manager := managementService(f)
	for _, team := range []*ontologyFixture{f, other} {
		statuses := []string{"organized", "pending", "ambiguous", "failed", "budget_deferred"}
		if team == other {
			statuses = []string{"failed", "failed", "failed", "failed", "failed"}
		}
		for _, status := range statuses {
			source := team.evidence(t, 0, "Status "+status+uuid.NewString())
			require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
				return tx.Exec(`INSERT INTO ontology_maintenance_sources(team_id,shared_space_id,space_generation,source_kind,source_id,source_version,eligible,status) VALUES(?::uuid,?::uuid,?,'evidence',?,1,true,?)`, team.team, team.space, team.generation, source.Evidence[0].FragmentID, status).Error
			}))
		}
		require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
			if err := tx.Exec(`INSERT INTO ontology_maintenance_teams(team_id,shared_space_id,space_generation,discovery_kind) VALUES(?::uuid,?::uuid,?,4) ON CONFLICT(team_id,shared_space_id,space_generation) DO UPDATE SET discovery_kind=4`, team.team, team.space, team.generation).Error; err != nil {
				return err
			}
			return tx.Exec(`DELETE FROM ontology_maintenance_markers WHERE team_id=?::uuid`, team.team).Error
		}))
	}
	status, err := manager.Status(f.actor(0, "manager"))
	require.NoError(t, err)
	require.Equal(t, ontology.MaintenanceCounts{Eligible: 5, Organized: 1, Pending: 1, Ambiguous: 1, Failed: 1, BudgetDeferred: 1}, status.Counts)
	require.True(t, status.DiscoveryComplete)
	require.False(t, status.CoverageComplete)
	foreign, err := manager.Status(other.actor(0, "manager"))
	require.NoError(t, err)
	require.EqualValues(t, 5, foreign.Counts.Failed)
	require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_sources SET status='organized' WHERE team_id=?::uuid`, f.team).Error)
	status, err = manager.Status(f.actor(0, "manager"))
	require.NoError(t, err)
	require.Equal(t, ontology.MaintenanceCounts{Eligible: 5, Organized: 5}, status.Counts)
	require.True(t, status.CoverageComplete)
	draft, err := manager.SaveDraft(f.actor(0, "manager"), "", managementInput(status.Revision, ontology.ManagementEdit{Action: "define", Kind: ontology.Topic, Definition: &ontology.Definition{Key: "coverage-update", Label: "Coverage update"}}))
	require.NoError(t, err)
	_, err = manager.CommandDraft(f.actor(0, "manager"), draft.ID, "publish", managementCommand(draft))
	require.NoError(t, err)
	status, err = manager.Status(f.actor(0, "manager"))
	require.NoError(t, err)
	require.Positive(t, status.PendingMarkers)
	require.False(t, status.CoverageComplete)
	require.Equal(t, ontology.MaintenanceCounts{Eligible: 5, Organized: 5}, status.Counts)
}

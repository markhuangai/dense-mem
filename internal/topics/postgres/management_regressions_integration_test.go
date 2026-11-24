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
	"github.com/markhuangai/dense-mem/internal/requestctx"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/topics/assessment"
	topiccontract "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyManagementEvidenceLookupBeyondFirstPage(t *testing.T) {
	f := newOntologyFixture(t)
	ctx := f.actor(0, "manager")
	manager := managementService(f)
	var handles []topiccontract.SourceHandle
	for index := range 51 {
		handles = append(handles, f.organizationEvidence(t, 0, fmt.Sprintf("ordered_storage_%03d", index), nil))
	}
	slices.SortFunc(handles, func(a, b topiccontract.SourceHandle) int { return strings.Compare(a.ID, b.ID) })
	last := handles[len(handles)-1]
	before := f.canonicalSnapshot(t)
	first, err := manager.Sources(ctx, topiccontract.EvidenceSource, "", "", 50)
	require.NoError(t, err)
	require.Len(t, first.Sources, 50)
	require.NotEmpty(t, first.NextCursor)
	for _, source := range first.Sources {
		require.NotEqual(t, last.ID, source.ID)
	}
	found, err := manager.Sources(ctx, topiccontract.EvidenceSource, "", last.ID, 50)
	require.NoError(t, err)
	require.Len(t, found.Sources, 1)
	require.Equal(t, last, found.Sources[0].SourceHandle)
	require.Empty(t, found.NextCursor)
	filtered, err := manager.Sources(ctx, topiccontract.EvidenceSource, "", "ordered_storage", 50)
	require.NoError(t, err)
	require.Equal(t, first, filtered)
	foreign := maintenanceOtherTeam(t, f)
	denied, err := manager.Sources(foreign.actor(0, "manager"), topiccontract.EvidenceSource, "", last.ID, 50)
	require.NoError(t, err)
	require.Empty(t, denied.Sources)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyManagementRetiredGroupSplitsIntoDistinctRecords(t *testing.T) {
	f := newOrganizationFixture(t)
	handles := []topiccontract.SourceHandle{}
	for index := range 6 {
		handles = append(handles, f.organizationEvidence(t, 0, fmt.Sprintf("Atlas uses PostgreSQL. Occurrence %d.", index), nil))
	}
	slices.SortFunc(handles, func(a, b topiccontract.SourceHandle) int { return strings.Compare(a.ID, b.ID) })
	before := f.canonicalSnapshot(t)
	grouped, _ := organizationFixtureService(t, f, func(assessment.Item, assessment.Item) bool { return true }, nil)
	_, err := grouped.Organize(context.Background(), f.team, topiccontract.OrganizationInput{OperationKey: "later-anchored-group", Sources: handles[4:]})
	require.NoError(t, err)
	original, err := f.store.ListRecords(context.Background(), f.team, topiccontract.EvidenceGroup, "", 20)
	require.NoError(t, err)
	require.Len(t, original.Records, 1)
	expanded, err := grouped.Organize(context.Background(), f.team, topiccontract.OrganizationInput{OperationKey: "expanded-group", Sources: handles})
	require.NoError(t, err)
	group, err := f.store.GetRecord(context.Background(), f.team, original.Records[0].ID, 0)
	require.NoError(t, err)
	retire := group.Record
	retire.Retired = true
	_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("retire-existing", expanded.Publication.Revision, topiccontract.Change{Record: retire, ExpectedVersion: group.Version}))
	require.NoError(t, err)
	retired, err := f.store.GetRecord(context.Background(), f.team, group.ID, 0)
	require.NoError(t, err)
	require.True(t, retired.Retired)
	snapshots, err := f.store.ReadSources(context.Background(), f.team, handles)
	require.NoError(t, err)
	partition := map[string]int{}
	for index, snapshot := range snapshots {
		partition[topiccontract.SourceDisplayText(snapshot)] = index / 2
	}
	split, _ := organizationFixtureService(t, f, func(a, b assessment.Item) bool { return partition[a.Text] == partition[b.Text] }, nil)
	_, err = split.Organize(context.Background(), f.team, topiccontract.OrganizationInput{OperationKey: "split-retired-group", Sources: handles})
	require.NoError(t, err)
	current, err := f.store.ListRecords(context.Background(), f.team, topiccontract.EvidenceGroup, "", 20)
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
	f := newOrganizationFixture(t)
	manager := managementService(f)
	ctx := f.actor(0, "manager")
	topic := testTopic("atomic-topic-edit")
	_, err := f.store.PublishAutomatic(context.Background(), f.team, testPublication("initial", 0, topiccontract.Change{Record: topic}))
	require.NoError(t, err)
	definition := *topic.Definition
	definition.Label = "Edited definition"
	edit := topiccontract.ManagementEdit{Action: "define", RecordID: topic.ID, ExpectedVersion: 1, Kind: topiccontract.Topic, Definition: &definition}
	_, err = manager.SaveDraft(ctx, "", managementInput(1, edit, topiccontract.ManagementEdit{Action: "remove_override", RecordID: uuid.NewString(), ExpectedVersion: 1}))
	require.ErrorIs(t, err, topiccontract.ErrInvalid)
	drafts, err := manager.Drafts(ctx, "", 20)
	require.NoError(t, err)
	require.Empty(t, drafts.Drafts)
	draft, err := manager.SaveDraft(ctx, "", managementInput(1, edit))
	require.NoError(t, err)
	_, err = manager.CommandDraft(ctx, draft.ID, "publish", managementCommand(draft))
	require.NoError(t, err)
	updated, err := manager.Record(ctx, topic.ID, 0)
	require.NoError(t, err)
	require.Equal(t, definition.Label, updated.Definition.Label)
}

func TestOntologyManagementRemovesWithdrawnCorrection(t *testing.T) {
	f := newHistoricalOntologyFixture(t)
	migrator, err := storage.NewMigrator(f.admin)
	require.NoError(t, err)
	require.NoError(t, migrator.RunUpThroughEvidenceOnly(context.Background()))
	topic := testTopic("source-lifecycle")
	handle := f.organizationEvidence(t, 0, "Withdraw this classification source.", nil)
	override := topiccontract.Record{ID: uuid.NewString(), Kind: topiccontract.OverrideKind, Override: &topiccontract.Override{Action: topiccontract.SetClassification, DefinitionID: topic.ID, Members: []topiccontract.SourceHandle{handle}}, Sources: []topiccontract.SourceDependency{f.source(t, handle)}}
	original := seedHistoricalRecords(t, f, "historic-correction", topic, override)
	require.NoError(t, migrator.RunUp(context.Background()))
	manager := managementService(f)
	ctx := f.actor(0, "manager")
	_, err = f.knowledge.RetractEvidence(ctx, knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{handle.ID}, IdempotencyKey: uuid.NewString(), RequestHash: testHash("withdraw-correction-source"), Reason: "Withdraw source"})
	require.NoError(t, err)
	_, err = manager.SaveDraft(ctx, "", managementInput(1, topiccontract.ManagementEdit{Action: "remove_override", RecordID: override.ID, ExpectedVersion: 1}))
	require.ErrorIs(t, err, topiccontract.ErrInvalid)
	saved, err := manager.Record(ctx, override.ID, 1)
	require.NoError(t, err)
	require.Equal(t, original[override.ID], saved.Record)
}

func TestOntologyManagementExcludesManagersOwnPrivateSpaces(t *testing.T) {
	f := newOntologyFixture(t)
	manager := managementService(f)
	ctx := f.actor(0, "manager")
	topic := testTopic("private-denial")
	_, err := f.store.PublishAutomatic(context.Background(), f.team, testPublication("topic", 0, topiccontract.Change{Record: topic}))
	require.NoError(t, err)
	for _, kind := range []domain.MemorySpaceKind{domain.MemorySpacePrivate} {
		var id string
		var generation int64
		require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
			return tx.Raw(`SELECT space.id::text,space.generation FROM memory_spaces space
            JOIN team_memberships member ON member.id=space.owner_membership_id AND member.team_id=space.team_id
            JOIN ownership_aliases alias ON alias.canonical_identity_id=member.actor_identity_id AND alias.team_id=member.team_id
            WHERE space.team_id=?::uuid AND alias.legacy_owner_id=?::uuid AND space.kind='private'`, f.team, f.owners[0]).Row().Scan(&id, &generation)
		}))
		space := domain.MemorySpaceAccess{ID: uuid.MustParse(id), Kind: kind, Generation: generation}
		privateContext := requestctx.WithAllowedSpaces(context.Background(), []domain.MemorySpaceAccess{space})
		ingest, err := f.knowledge.CreateIngestForTest(privateContext, knowledge.CreateIngestInput{TeamID: f.team, OwnerProfileID: f.owners[0], SpaceID: id, SpaceGeneration: generation, Evidence: []knowledge.EvidenceInput{{Content: "Manager's own private canary"}}})
		require.NoError(t, err)
		actor, _ := requestctx.ActorFromContext(ctx)
		actor.AllowedSpaces = append(actor.AllowedSpaces, space)
		authorized := requestctx.WithActor(context.Background(), actor)
		handle := topiccontract.SourceHandle{Kind: topiccontract.EvidenceSource, ID: ingest.Evidence[0].FragmentID, Version: 1}
		page, err := manager.Sources(authorized, topiccontract.EvidenceSource, "", handle.ID, 50)
		require.NoError(t, err)
		require.Empty(t, page.Sources)
		_, err = manager.SaveDraft(authorized, "", managementInput(1, topiccontract.ManagementEdit{Action: "classify", Source: &handle, DefinitionID: topic.ID}))
		require.ErrorIs(t, err, topiccontract.ErrInvalid)
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
		statuses := []string{"organized", "pending", "ambiguous", "failed", "pending"}
		if team == other {
			statuses = []string{"failed", "failed", "failed", "failed", "failed"}
		}
		for _, status := range statuses {
			source := team.evidence(t, 0, "Status "+status+uuid.NewString())
			require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
				return tx.Exec(`INSERT INTO topic_maintenance_sources(team_id,shared_space_id,space_generation,source_kind,source_id,source_version,eligible,status) VALUES(?::uuid,?::uuid,?,'evidence',?,1,true,?)`, team.team, team.space, team.generation, source.Evidence[0].FragmentID, status).Error
			}))
		}
		require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
			if err := tx.Exec(`INSERT INTO topic_maintenance_teams(team_id,shared_space_id,space_generation,discovery_kind) VALUES(?::uuid,?::uuid,?,1) ON CONFLICT(team_id,shared_space_id,space_generation) DO UPDATE SET discovery_kind=1`, team.team, team.space, team.generation).Error; err != nil {
				return err
			}
			return tx.Exec(`DELETE FROM topic_maintenance_markers WHERE team_id=?::uuid`, team.team).Error
		}))
	}
	status, err := manager.Status(f.actor(0, "manager"))
	require.NoError(t, err)
	require.Equal(t, topiccontract.MaintenanceCounts{Eligible: 5, Organized: 1, Pending: 2, Ambiguous: 1, Failed: 1}, status.Counts)
	require.True(t, status.DiscoveryComplete)
	require.False(t, status.CoverageComplete)
	foreign, err := manager.Status(other.actor(0, "manager"))
	require.NoError(t, err)
	require.EqualValues(t, 5, foreign.Counts.Failed)
	require.NoError(t, f.admin.Exec(`UPDATE topic_maintenance_sources SET status='organized' WHERE team_id=?::uuid`, f.team).Error)
	status, err = manager.Status(f.actor(0, "manager"))
	require.NoError(t, err)
	require.Equal(t, topiccontract.MaintenanceCounts{Eligible: 5, Organized: 5}, status.Counts)
	require.True(t, status.CoverageComplete)
	draft, err := manager.SaveDraft(f.actor(0, "manager"), "", managementInput(status.Revision, topiccontract.ManagementEdit{Action: "define", Kind: topiccontract.Topic, Definition: &topiccontract.Definition{Key: "coverage-update", Label: "Coverage update"}}))
	require.NoError(t, err)
	_, err = manager.CommandDraft(f.actor(0, "manager"), draft.ID, "publish", managementCommand(draft))
	require.NoError(t, err)
	status, err = manager.Status(f.actor(0, "manager"))
	require.NoError(t, err)
	require.Positive(t, status.PendingMarkers)
	require.False(t, status.CoverageComplete)
	require.Equal(t, topiccontract.MaintenanceCounts{Eligible: 5, Organized: 5}, status.Counts)
}

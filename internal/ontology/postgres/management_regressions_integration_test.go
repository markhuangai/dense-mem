//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

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

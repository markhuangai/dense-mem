//go:build integration

package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologySharedSourceIsolationAndFreshness(t *testing.T) {
	f := newOntologyFixture(t)
	evidence := f.evidence(t, 1, "Atlas uses PostgreSQL for durable data.")
	handle := ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: evidence.Evidence[0].FragmentID, Version: 1}
	source := f.source(t, handle)
	read, err := f.store.ReadSources(f.actor(0, "manager"), f.team, []ontology.SourceHandle{handle})
	require.NoError(t, err)
	require.Equal(t, f.owners[1], read[0].OwnerID)
	private := &domain.Credential{ID: uuid.New(), TeamID: uuid.MustParse(f.team), Name: "private", KeyHash: "synthetic-private", KeyPrefix: strings.ReplaceAll(uuid.NewString(), "-", "")[:24], KeySuffix: "privat", Scopes: []string{"read", "write"}, MemoryBinding: domain.CredentialBindingCredentialPrivate}
	require.NoError(t, access.NewCredentialRepository(f.admin, f.rls, nil).CreateCredential(context.Background(), private))
	privateContext := requestctx.WithAllowedSpaces(context.Background(), []domain.MemorySpaceAccess{{ID: private.MemorySpaceID, Kind: domain.MemorySpaceCredentialPrivate, Generation: private.MemorySpaceGeneration}})
	privateEvidence, err := f.knowledge.CreateIngestForTest(privateContext, knowledge.CreateIngestInput{TeamID: f.team, OwnerProfileID: private.ID.String(), SpaceID: private.MemorySpaceID.String(), SpaceGeneration: private.MemorySpaceGeneration, Evidence: []knowledge.EvidenceInput{{Content: "Private material must remain excluded."}}})
	require.NoError(t, err)
	_, err = f.store.ReadSources(context.Background(), f.team, []ontology.SourceHandle{{Kind: ontology.EvidenceSource, ID: privateEvidence.Evidence[0].FragmentID, Version: 1}})
	require.ErrorIs(t, err, ontology.ErrSourceStale)
	other := &domain.Team{Name: "other-" + uuid.NewString()}
	require.NoError(t, access.NewTeamRepository(f.admin, f.rls).Create(context.Background(), other))
	thirdCredential := &domain.Credential{ID: uuid.New(), TeamID: other.ID, Name: "actor-c", KeyHash: "synthetic-c", KeyPrefix: strings.ReplaceAll(uuid.NewString(), "-", "")[:24], KeySuffix: "test", Scopes: []string{"read", "write"}}
	require.NoError(t, access.NewCredentialRepository(f.admin, f.rls, nil).CreateCredential(context.Background(), thirdCredential))
	third := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: other.ID, OwnerID: thirdCredential.ID, Role: "manager", Grants: []string{"read", "write"}})
	_, err = f.store.ReadSources(third, f.team, []ontology.SourceHandle{handle})
	require.ErrorIs(t, err, ontology.ErrUnauthorized)
	var count int
	require.NoError(t, f.rls.WithTeamTx(context.Background(), f.app, other.ID.String(), func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM evidence_fragments WHERE team_id=?::uuid`, f.team).Row().Scan(&count)
	}))
	require.Zero(t, count)
	before := f.canonicalSnapshot(t)
	topic := testTopic("databases")
	assignment := ontology.Record{ID: uuid.NewString(), Kind: ontology.AssignmentKind, Assignment: &ontology.Assignment{Source: handle, DefinitionID: topic.ID}, Sources: []ontology.SourceDependency{source}}
	published, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("classify", 0, ontology.Change{Record: topic}, ontology.Change{Record: assignment}))
	require.NoError(t, err)
	require.NoError(t, f.rls.WithTeamTx(context.Background(), f.app, other.ID.String(), func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM ontology_record_revisions WHERE team_id=?::uuid`, f.team).Row().Scan(&count)
	}))
	require.Zero(t, count)
	require.Equal(t, before, f.canonicalSnapshot(t))
	view, err := f.store.GetRecord(f.actor(1, "member"), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.True(t, view.Current)
	unrelated, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("unrelated", published.Revision, ontology.Change{Record: testTopic("unrelated")}))
	require.NoError(t, err)
	view, err = f.store.GetRecord(f.actor(1, "member"), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.True(t, view.Current)
	topic.Definition.Label = "Database systems"
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("revise-definition", unrelated.Revision, ontology.Change{ExpectedVersion: 1, Record: topic}))
	require.NoError(t, err)
	view, err = f.store.GetRecord(f.actor(1, "member"), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.False(t, view.Current)
	require.Equal(t, "dependency_changed", view.StaleReason)
	topic.Definition.Label = "Durable database systems"
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("atomic-refresh", 3, ontology.Change{ExpectedVersion: 2, Record: topic}, ontology.Change{ExpectedVersion: 1, Record: assignment}))
	require.NoError(t, err)
	view, err = f.store.GetRecord(f.actor(1, "member"), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.True(t, view.Current)
	require.Contains(t, view.Dependencies, ontology.RevisionRef{ID: topic.ID, Version: 3})
	_, err = f.knowledge.RetractEvidence(f.actor(0, "manager"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{handle.ID}, Reason: "wrong owner", IdempotencyKey: "wrong-owner", RequestHash: testHash("wrong-owner")})
	require.Error(t, err)
	_, err = f.knowledge.RetractEvidence(f.actor(1, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[1], EvidenceIDs: []string{handle.ID}, Reason: "owner retracts", IdempotencyKey: "retract", RequestHash: testHash("retract")})
	require.NoError(t, err)
	view, err = f.store.GetRecord(f.actor(0, "manager"), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.False(t, view.Current)
	require.Equal(t, "source_changed", view.StaleReason)
	_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("stale-source", 4, ontology.Change{Record: ontology.Record{ID: uuid.NewString(), Kind: ontology.AssignmentKind, Assignment: &ontology.Assignment{Source: handle, DefinitionID: topic.ID}, Sources: []ontology.SourceDependency{source}}}))
	require.ErrorIs(t, err, ontology.ErrSourceStale)
}

func TestOntologyManagerOverridesAndRollback(t *testing.T) {
	f := newOntologyFixture(t)
	seed, err := f.store.SeedDefinitions(context.Background(), f.team, ontology.SeedInput{OperationKey: "seed", Limit: 20})
	require.NoError(t, err)
	entity, err := f.knowledge.CreateEntity(context.Background(), knowledge.CreateEntityInput{TeamID: f.team, OwnerProfileID: f.owners[0], EntityKind: "person", CanonicalName: "Ada"})
	require.NoError(t, err)
	handle := ontology.SourceHandle{Kind: ontology.EntitySource, ID: entity.EntityID, Version: int64(entity.Version)}
	dependency := f.source(t, handle)
	classID := ontology.SeedDefinitionID(f.team, ontology.EntityClass, "person")
	wrongOverride := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.SetClassification, DefinitionID: ontology.SeedDefinitionID(f.team, ontology.EntityClass, "project"), Members: []ontology.SourceHandle{handle}}, Sources: []ontology.SourceDependency{dependency}}
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("wrong-class", seed.Revision, ontology.Change{Record: wrongOverride}))
	require.ErrorIs(t, err, ontology.ErrInvalid)
	history, err := f.store.History(f.actor(0, "manager"), f.team, 0, 20)
	require.NoError(t, err)
	require.Len(t, history, 1)
	override := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.SetClassification, DefinitionID: classID, Members: []ontology.SourceHandle{handle}}, Sources: []ontology.SourceDependency{dependency}}
	pin, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("pin", seed.Revision, ontology.Change{Record: override}))
	require.NoError(t, err)
	other := ontology.Record{ID: uuid.NewString(), Kind: ontology.EntityClass, Definition: &ontology.Definition{Key: "engineer", Label: "Engineer", ParentID: classID, BaseEntityKind: "person"}}
	newClass, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("new-class", pin.Revision, ontology.Change{Record: other}))
	require.NoError(t, err)
	assignment := ontology.Record{ID: uuid.NewString(), Kind: ontology.AssignmentKind, Assignment: &ontology.Assignment{Source: handle, DefinitionID: other.ID}, Sources: []ontology.SourceDependency{dependency}}
	_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("contradict-pin", newClass.Revision, ontology.Change{Record: assignment}))
	require.ErrorIs(t, err, ontology.ErrOverride)
	accepted := assignment
	accepted.Assignment = &ontology.Assignment{Source: handle, DefinitionID: classID}
	result, err := f.store.PublishAutomatic(context.Background(), f.team, testPublication("respect-pin", newClass.Revision, ontology.Change{Record: accepted}))
	require.NoError(t, err)
	storedOverride, err := f.store.GetRecord(f.actor(0, "manager"), f.team, override.ID, 0)
	require.NoError(t, err)
	storedOverride.Record.Retired = true
	unpin, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("unpin", result.Revision, ontology.Change{ExpectedVersion: 1, Record: storedOverride.Record}))
	require.NoError(t, err)
	rollback, err := f.store.Rollback(f.actor(0, "manager"), f.team, unpin.ID, "restore-pin", unpin.Revision, "restore manager classification")
	require.NoError(t, err)
	_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("still-pinned", rollback.Revision, ontology.Change{ExpectedVersion: 1, Record: assignment}))
	require.ErrorIs(t, err, ontology.ErrOverride)
	stored, err := f.store.GetRecord(f.actor(0, "manager"), f.team, override.ID, 0)
	require.NoError(t, err)
	require.Equal(t, int64(3), stored.Version)
	require.False(t, stored.Retired)
}

func TestOntologyRollbackRejectsStaleSources(t *testing.T) {
	f := newOntologyFixture(t)
	evidence := f.evidence(t, 0, "Atlas stores data in PostgreSQL.")
	handle := ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: evidence.Evidence[0].FragmentID, Version: 1}
	first, second := testTopic("storage"), testTopic("database")
	assignment := ontology.Record{ID: uuid.NewString(), Kind: ontology.AssignmentKind, Assignment: &ontology.Assignment{Source: handle, DefinitionID: first.ID}, Sources: []ontology.SourceDependency{f.source(t, handle)}}
	_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("initial", 0, ontology.Change{Record: first}, ontology.Change{Record: second}, ontology.Change{Record: assignment}))
	require.NoError(t, err)
	assignment.Assignment.DefinitionID = second.ID
	changed, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("reclassify", 1, ontology.Change{ExpectedVersion: 1, Record: assignment}))
	require.NoError(t, err)
	_, err = f.knowledge.RetractEvidence(f.actor(0, "manager"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{handle.ID}, Reason: "source is obsolete", IdempotencyKey: "retract", RequestHash: testHash("retract")})
	require.NoError(t, err)
	_, err = f.store.Rollback(f.actor(0, "manager"), f.team, changed.ID, "unsafe-rollback", changed.Revision, "restore classification")
	require.ErrorIs(t, err, ontology.ErrSourceStale)
	history, err := f.store.History(f.actor(0, "manager"), f.team, 0, 20)
	require.NoError(t, err)
	require.Len(t, history, 2)
	view, err := f.store.GetRecord(f.actor(0, "manager"), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.Equal(t, int64(2), view.Version)
	require.False(t, view.Current)
}

func TestOntologyExcludesOldSharedGeneration(t *testing.T) {
	f := newOntologyFixture(t)
	evidence := f.evidence(t, 0, "Old generation source.")
	handle := ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: evidence.Evidence[0].FragmentID, Version: 1}
	dependency := f.source(t, handle)
	seed, err := f.store.SeedDefinitions(context.Background(), f.team, ontology.SeedInput{OperationKey: "seed", Limit: 20})
	require.NoError(t, err)
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE memory_spaces SET generation=generation+1 WHERE team_id=?::uuid AND id=?::uuid`, f.team, f.space).Error
	}))
	_, err = f.store.GetRecord(f.actor(0, "manager"), f.team, seed.Records[0].ID, 0)
	require.ErrorIs(t, err, ontology.ErrUnauthorized)
	_, err = f.store.GetRecord(context.Background(), f.team, seed.Records[0].ID, 0)
	require.ErrorIs(t, err, ontology.ErrNotFound)
	_, err = f.store.ReadSources(context.Background(), f.team, []ontology.SourceHandle{handle})
	require.ErrorIs(t, err, ontology.ErrSourceStale)
	topic := testTopic("new-generation")
	assignment := ontology.Record{ID: uuid.NewString(), Kind: ontology.AssignmentKind, Assignment: &ontology.Assignment{Source: handle, DefinitionID: topic.ID}, Sources: []ontology.SourceDependency{dependency}}
	_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("old-source", 0, ontology.Change{Record: topic}, ontology.Change{Record: assignment}))
	require.ErrorIs(t, err, ontology.ErrSourceStale)
	history, err := f.store.History(context.Background(), f.team, 0, 20)
	require.NoError(t, err)
	require.Empty(t, history)
	require.Error(t, f.rls.WithTeamTx(context.Background(), f.app, f.team, func(tx *gorm.DB) error {
		return tx.Exec(`INSERT INTO ontology_catalog_heads(team_id,shared_space_id,space_generation) VALUES (?::uuid,?::uuid,?)`, f.team, f.space, f.generation).Error
	}))
}

func TestOntologyRelationshipProvenanceAndPredicateSeeds(t *testing.T) {
	f := newOntologyFixture(t)
	ctx := context.Background()
	subject, err := f.knowledge.CreateEntity(ctx, knowledge.CreateEntityInput{TeamID: f.team, OwnerProfileID: f.owners[0], EntityKind: "person", CanonicalName: "Ada"})
	require.NoError(t, err)
	object, err := f.knowledge.CreateEntity(ctx, knowledge.CreateEntityInput{TeamID: f.team, OwnerProfileID: f.owners[0], EntityKind: "project", CanonicalName: "Atlas"})
	require.NoError(t, err)
	predicate, err := f.knowledge.EnsureSemanticReviewPredicateCandidate(ctx, knowledge.EnsureSemanticPredicateCandidateInput{TeamID: f.team, OwnerProfileID: f.owners[0], Predicate: "works_on", RelationshipKind: "state", SubjectKind: "person", ObjectKind: "project"})
	require.NoError(t, err)
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var handles []ontology.SourceHandle
	var evidenceIDs []string
	for index, owner := range []int{0, 1, 0} {
		text := []string{"Ada works on Atlas.", "An independent source confirms Ada works on Atlas.", "Ada does not work on Atlas."}[index]
		ingest := f.evidence(t, owner, text)
		polarity := "+"
		if index == 2 {
			polarity = "-"
		}
		decision, err := f.knowledge.ApplyRelationshipDecision(ctx, knowledge.ApplyRelationshipDecisionInput{
			TeamID: f.team, OwnerProfileID: f.owners[owner], IngestID: ingest.IngestID,
			SubjectEntityID: subject.EntityID, PredicateKey: predicate.PredicateKey, PredicateVersion: predicate.Version,
			ObjectEntityID: object.EntityID, Polarity: polarity, ValidFrom: &from,
			Support: &knowledge.EvidenceSupportInput{FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: uuid.NewString(), SpanEnd: len(text), Authority: "primary"},
		})
		require.NoError(t, err)
		require.NotNil(t, decision.Relationship)
		handles = append(handles, ontology.SourceHandle{Kind: ontology.RelationshipSource, ID: decision.Relationship.RelationshipID, Version: int64(decision.Relationship.Version)})
		evidenceIDs = append(evidenceIDs, ingest.Evidence[0].FragmentID)
	}
	require.NotEqual(t, handles[0].ID, handles[1].ID)
	snapshots, err := f.store.ReadSources(f.actor(1, "member"), f.team, handles)
	require.NoError(t, err)
	require.Equal(t, f.owners[0], snapshots[0].OwnerID)
	require.Equal(t, f.owners[1], snapshots[1].OwnerID)
	require.Equal(t, snapshots[0].MeaningKey, snapshots[1].MeaningKey)
	require.NotEqual(t, snapshots[0].MeaningKey, snapshots[2].MeaningKey)
	var zoned ontology.SourceSnapshot
	require.NoError(t, f.rls.WithTeamTx(ctx, f.app, f.team, func(tx *gorm.DB) error {
		if err := tx.Exec(`SET LOCAL TIME ZONE 'Pacific/Auckland'`).Error; err != nil {
			return err
		}
		var err error
		zoned, err = readSource(tx, scope{TeamID: f.team, SpaceID: f.space, Generation: f.generation}, handles[0])
		return err
	}))
	require.Equal(t, snapshots[0], zoned)
	before := f.canonicalSnapshot(t)
	seed, err := f.store.SeedDefinitions(ctx, f.team, ontology.SeedInput{OperationKey: "seed-predicates", Limit: 20})
	require.NoError(t, err)
	conceptID := ontology.SeedDefinitionID(f.team, ontology.PredicateConcept, predicate.PredicateKey)
	concept, err := f.store.GetRecord(ctx, f.team, conceptID, 0)
	require.NoError(t, err)
	require.True(t, concept.Current)
	group := ontology.Record{ID: uuid.NewString(), Kind: ontology.RelationshipGroup, Group: &ontology.Group{Members: handles[:2]}, Sources: []ontology.SourceDependency{f.source(t, handles[0]), f.source(t, handles[1])}}
	assignment := ontology.Record{ID: uuid.NewString(), Kind: ontology.AssignmentKind, Assignment: &ontology.Assignment{Source: ontology.SourceHandle{Kind: ontology.PredicateSource, ID: predicate.PredicateKey, Version: int64(predicate.Version)}, DefinitionID: conceptID}}
	assignment.Sources = []ontology.SourceDependency{f.source(t, assignment.Assignment.Source)}
	assignment.Dependencies = []ontology.RevisionRef{{ID: group.ID, Version: 1}}
	publication, err := f.store.PublishAutomatic(ctx, f.team, testPublication("equivalent-owners", seed.Revision, ontology.Change{Record: group}, ontology.Change{Record: assignment}))
	require.NoError(t, err)
	view, err := f.store.GetRecord(f.actor(0, "manager"), f.team, group.ID, 0)
	require.NoError(t, err)
	require.True(t, view.Current)
	require.Equal(t, handles[:2], view.Group.Members)
	bad := group
	bad.ID = uuid.NewString()
	bad.Group = &ontology.Group{Members: []ontology.SourceHandle{handles[0], handles[2]}}
	bad.Sources = []ontology.SourceDependency{f.source(t, handles[0]), f.source(t, handles[2])}
	_, err = f.store.PublishAutomatic(ctx, f.team, testPublication("distinct-polarity", publication.Revision, ontology.Change{Record: bad}))
	require.ErrorIs(t, err, ontology.ErrInvalid)
	require.Equal(t, before, f.canonicalSnapshot(t))
	separation := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.KeepSeparate, Members: handles[:2]}, Sources: group.Sources}
	overridden, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("separate-group", publication.Revision, ontology.Change{Record: separation}))
	require.NoError(t, err)
	view, err = f.store.GetRecord(context.Background(), f.team, group.ID, 0)
	require.NoError(t, err)
	require.False(t, view.Current)
	dependent, err := f.store.GetRecord(context.Background(), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.False(t, dependent.Current)
	newAssignment := assignment
	newAssignment.ID = uuid.NewString()
	_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("stale-group-dependency", overridden.Revision, ontology.Change{Record: newAssignment}))
	require.ErrorIs(t, err, ontology.ErrSourceStale)
	history, err := f.store.History(context.Background(), f.team, 0, 20)
	require.NoError(t, err)
	require.Len(t, history, 3)
	require.Equal(t, before, f.canonicalSnapshot(t))
	for index, id := range evidenceIDs {
		owner := 0
		if index == 1 {
			owner = 1
		}
		_, err = f.knowledge.RetractEvidence(f.actor(owner, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[owner], EvidenceIDs: []string{id}, Reason: "withdrawn fixture support", IdempotencyKey: uuid.NewString(), RequestHash: testHash(id)})
		require.NoError(t, err)
	}
	view, err = f.store.GetRecord(ctx, f.team, group.ID, 0)
	require.NoError(t, err)
	require.False(t, view.Current)
	_, err = f.store.ReadSources(ctx, f.team, []ontology.SourceHandle{assignment.Assignment.Source})
	require.ErrorIs(t, err, ontology.ErrSourceStale)
	seedAfter, err := f.store.SeedDefinitions(ctx, f.team, ontology.SeedInput{OperationKey: "seed-withdrawn", ExpectedRevision: overridden.Revision, Limit: 20})
	require.NoError(t, err)
	require.Empty(t, seedAfter.Records)
}

func TestOntologyUnavailableSnapshotRetainsScopedHandleForMaintenance(t *testing.T) {
	f := newOntologyFixture(t)
	handle := ontology.SourceHandle{Kind: ontology.RelationshipSource, ID: uuid.NewString(), Version: 1}
	require.NoError(t, f.rls.WithTeamTx(context.Background(), f.app, f.team, func(tx *gorm.DB) error {
		fence := scope{TeamID: f.team, SpaceID: f.space, Generation: f.generation}
		snapshot, err := readSource(tx, fence, handle)
		require.ErrorIs(t, err, ontology.ErrSourceStale)
		require.False(t, snapshot.Eligible)
		require.Equal(t, handle, snapshot.SourceHandle)
		require.Equal(t, f.team, snapshot.TeamID)
		require.Equal(t, f.space, snapshot.SpaceID)
		require.Equal(t, f.generation, snapshot.Generation)
		return refreshMaintenanceSource(tx, fence, handle, false)
	}))
	var status string
	var eligible bool
	require.NoError(t, f.rls.WithTeamTx(context.Background(), f.app, f.team, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT status,eligible FROM ontology_maintenance_sources WHERE team_id=?::uuid AND source_kind=? AND source_id=?`, f.team, handle.Kind, handle.ID).Row().Scan(&status, &eligible)
	}))
	require.Equal(t, "unavailable", status)
	require.False(t, eligible)
}

func TestOntologyOrganizationPreservesValidLargeSourceContext(t *testing.T) {
	f := newOntologyFixture(t)
	const definitions = 17
	sourceCount := 1 + definitions*(ontology.MaxMembers-1)
	handles := make([]ontology.SourceHandle, 0, sourceCount)
	for offset := 0; offset < sourceCount; offset += 100 {
		items := []knowledge.EvidenceInput{}
		for i := offset; i < min(offset+100, sourceCount); i++ {
			items = append(items, knowledge.EvidenceInput{Content: fmt.Sprintf("Large context source %d.", i)})
		}
		ingest, err := f.knowledge.CreateIngestForTest(context.Background(), knowledge.CreateIngestInput{
			TeamID: f.team, OwnerProfileID: f.owners[0], Evidence: items,
		})
		require.NoError(t, err)
		for _, fragment := range ingest.Evidence {
			handles = append(handles, ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: fragment.FragmentID, Version: 1})
		}
	}
	require.Greater(t, len(handles), ontology.MaxDependencyRecords)
	dependencies := map[ontology.SourceHandle]ontology.SourceDependency{}
	require.NoError(t, f.store.withScope(context.Background(), f.team, true, func(tx *gorm.DB, fence scope) error {
		for offset := 0; offset < len(handles); offset += ontology.MaxDependencyRecords {
			snapshots, err := readSources(tx, fence, handles[offset:min(offset+ontology.MaxDependencyRecords, len(handles))])
			if err != nil {
				return err
			}
			for handle, snapshot := range snapshots {
				fingerprint, err := ontology.SourceFingerprint(snapshot)
				if err != nil {
					return err
				}
				dependencies[handle] = ontology.SourceDependency{SourceHandle: handle, Fingerprint: fingerprint}
			}
		}
		return nil
	}))
	records := make([]ontology.Record, definitions)
	for i := range records {
		records[i] = testTopic(fmt.Sprintf("large-context-%d", i))
		records[i].Sources = []ontology.SourceDependency{dependencies[handles[0]]}
		for _, handle := range handles[1+i*(ontology.MaxMembers-1) : 1+(i+1)*(ontology.MaxMembers-1)] {
			records[i].Sources = append(records[i].Sources, dependencies[handle])
		}
	}
	revision := int64(0)
	for offset := 0; offset < len(records); offset += 4 {
		changes := []ontology.Change{}
		for _, record := range records[offset:min(offset+4, len(records))] {
			changes = append(changes, ontology.Change{Record: record})
		}
		published, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(uuid.NewString(), revision, changes...))
		require.NoError(t, err)
		revision = published.Revision
	}
	contextData, err := f.store.ReadOrganization(context.Background(), f.team, handles[:1])
	require.NoError(t, err)
	require.Len(t, contextData.Records, definitions)
	for _, record := range contextData.Records {
		require.True(t, record.Current)
	}
	last := handles[len(handles)-1]
	_, err = f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{
		TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{last.ID}, Reason: "source withdrawn",
		IdempotencyKey: uuid.NewString(), RequestHash: testHash(last.ID),
	})
	require.NoError(t, err)
	contextData, err = f.store.ReadOrganization(context.Background(), f.team, handles[:1])
	require.NoError(t, err)
	require.Len(t, contextData.Records, definitions)
	for _, record := range contextData.Records {
		require.Equal(t, record.ID != records[len(records)-1].ID, record.Current)
	}
}

func TestOntologyOrganizationPreservesPerRecordDependencyBounds(t *testing.T) {
	f := newOntologyFixture(t)
	ingest := f.evidence(t, 0, "Shared source for parent-backed topics.")
	handle := ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: ingest.Evidence[0].FragmentID, Version: 1}
	dependency := f.source(t, handle)
	children := ontology.MaxDependencyRecords/2 + 1
	revision := int64(0)
	for offset := 0; offset < children; offset += ontology.MaxChanges / 2 {
		changes := []ontology.Change{}
		for i := offset; i < min(offset+ontology.MaxChanges/2, children); i++ {
			parent := testTopic(fmt.Sprintf("context-parent-%d", i))
			child := testTopic(fmt.Sprintf("context-child-%d", i))
			child.Definition.ParentID = parent.ID
			child.Sources = []ontology.SourceDependency{dependency}
			changes = append(changes, ontology.Change{Record: parent}, ontology.Change{Record: child})
		}
		published, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(uuid.NewString(), revision, changes...))
		require.NoError(t, err)
		revision = published.Revision
	}
	contextData, err := f.store.ReadOrganization(context.Background(), f.team, []ontology.SourceHandle{handle})
	require.NoError(t, err)
	require.Len(t, contextData.Records, children)
	for _, view := range contextData.Records {
		require.True(t, view.Current)
		require.NotEmpty(t, view.Definition.ParentID)
	}
	_, err = f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{
		TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{handle.ID}, Reason: "source withdrawn",
		IdempotencyKey: uuid.NewString(), RequestHash: testHash(handle.ID),
	})
	require.NoError(t, err)
	contextData, err = f.store.ReadOrganization(context.Background(), f.team, []ontology.SourceHandle{handle})
	require.NoError(t, err)
	require.Len(t, contextData.Records, children)
	for _, view := range contextData.Records {
		require.False(t, view.Current)
	}
}

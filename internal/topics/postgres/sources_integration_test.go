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
	"github.com/markhuangai/dense-mem/internal/requestctx"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	topiccontract "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologySharedSourceIsolationAndFreshness(t *testing.T) {
	f := newOntologyFixture(t)
	evidence := f.evidence(t, 1, "Atlas uses PostgreSQL for durable data.")
	handle := topiccontract.SourceHandle{Kind: topiccontract.EvidenceSource, ID: evidence.Evidence[0].FragmentID, Version: 1}
	source := f.source(t, handle)
	read, err := f.store.ReadSources(f.actor(0, "manager"), f.team, []topiccontract.SourceHandle{handle})
	require.NoError(t, err)
	require.Equal(t, f.owners[1], read[0].OwnerID)
	private := &domain.Credential{ID: uuid.New(), TeamID: uuid.MustParse(f.team), Name: "private", KeyHash: "synthetic-private", KeyPrefix: strings.ReplaceAll(uuid.NewString(), "-", "")[:24], KeySuffix: "privat", Scopes: []string{"read", "write"}}
	require.NoError(t, access.NewCredentialRepository(f.admin, f.rls, nil).CreateCredential(context.Background(), private))
	privateContext := requestctx.WithAllowedSpaces(context.Background(), []domain.MemorySpaceAccess{{ID: private.MemorySpaceID, Kind: domain.MemorySpacePrivate, Generation: private.MemorySpaceGeneration}})
	privateEvidence, err := f.knowledge.CreateIngestForTest(privateContext, knowledge.CreateIngestInput{TeamID: f.team, OwnerProfileID: private.ID.String(), SpaceID: private.MemorySpaceID.String(), SpaceGeneration: private.MemorySpaceGeneration, Evidence: []knowledge.EvidenceInput{{Content: "Private material must remain excluded."}}})
	require.NoError(t, err)
	_, err = f.store.ReadSources(context.Background(), f.team, []topiccontract.SourceHandle{{Kind: topiccontract.EvidenceSource, ID: privateEvidence.Evidence[0].FragmentID, Version: 1}})
	require.ErrorIs(t, err, topiccontract.ErrSourceStale)
	other := &domain.Team{Name: "other-" + uuid.NewString()}
	require.NoError(t, access.NewTeamRepository(f.admin, f.rls).Create(context.Background(), other))
	thirdCredential := &domain.Credential{ID: uuid.New(), TeamID: other.ID, Name: "actor-c", KeyHash: "synthetic-c", KeyPrefix: strings.ReplaceAll(uuid.NewString(), "-", "")[:24], KeySuffix: "test", Scopes: []string{"read", "write"}}
	require.NoError(t, access.NewCredentialRepository(f.admin, f.rls, nil).CreateCredential(context.Background(), thirdCredential))
	third := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: other.ID, OwnerID: thirdCredential.ID, Role: "manager", Grants: []string{"read", "write"}})
	_, err = f.store.ReadSources(third, f.team, []topiccontract.SourceHandle{handle})
	require.ErrorIs(t, err, topiccontract.ErrUnauthorized)
	var count int
	require.NoError(t, f.rls.WithTeamTx(context.Background(), f.app, other.ID.String(), func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM evidence_fragments WHERE team_id=?::uuid`, f.team).Row().Scan(&count)
	}))
	require.Zero(t, count)
	before := f.canonicalSnapshot(t)
	topic := testTopic("databases")
	assignment := topiccontract.Record{ID: uuid.NewString(), Kind: topiccontract.AssignmentKind, Assignment: &topiccontract.Assignment{Source: handle, DefinitionID: topic.ID}, Sources: []topiccontract.SourceDependency{source}}
	published, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("classify", 0, topiccontract.Change{Record: topic}, topiccontract.Change{Record: assignment}))
	require.NoError(t, err)
	require.NoError(t, f.rls.WithTeamTx(context.Background(), f.app, other.ID.String(), func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM topic_record_revisions WHERE team_id=?::uuid`, f.team).Row().Scan(&count)
	}))
	require.Zero(t, count)
	require.Equal(t, before, f.canonicalSnapshot(t))
	view, err := f.store.GetRecord(f.actor(1, "member"), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.True(t, view.Current)
	unrelated, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("unrelated", published.Revision, topiccontract.Change{Record: testTopic("unrelated")}))
	require.NoError(t, err)
	view, err = f.store.GetRecord(f.actor(1, "member"), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.True(t, view.Current)
	topic.Definition.Label = "Database systems"
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("revise-definition", unrelated.Revision, topiccontract.Change{ExpectedVersion: 1, Record: topic}))
	require.NoError(t, err)
	view, err = f.store.GetRecord(f.actor(1, "member"), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.True(t, view.Current, "an accepted Assignment remains valid while its target is active")
	topic.Definition.Label = "Durable database systems"
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("atomic-refresh", 3, topiccontract.Change{ExpectedVersion: 2, Record: topic}, topiccontract.Change{ExpectedVersion: 1, Record: assignment}))
	require.NoError(t, err)
	view, err = f.store.GetRecord(f.actor(1, "member"), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.True(t, view.Current)
	require.Contains(t, view.Dependencies, topiccontract.RevisionRef{ID: topic.ID, Version: 3})
	_, err = f.knowledge.RetractEvidence(f.actor(0, "manager"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{handle.ID}, Reason: "wrong owner", IdempotencyKey: "wrong-owner", RequestHash: testHash("wrong-owner")})
	require.Error(t, err)
	_, err = f.knowledge.RetractEvidence(f.actor(1, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[1], EvidenceIDs: []string{handle.ID}, Reason: "owner retracts", IdempotencyKey: "retract", RequestHash: testHash("retract")})
	require.NoError(t, err)
	view, err = f.store.GetRecord(f.actor(0, "manager"), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.False(t, view.Current)
	require.Equal(t, "source_changed", view.StaleReason)
	_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("stale-source", 4, topiccontract.Change{Record: topiccontract.Record{ID: uuid.NewString(), Kind: topiccontract.AssignmentKind, Assignment: &topiccontract.Assignment{Source: handle, DefinitionID: topic.ID}, Sources: []topiccontract.SourceDependency{source}}}))
	require.ErrorIs(t, err, topiccontract.ErrSourceStale)
}

func TestOntologyManagerOverridesAndRollback(t *testing.T) {
	f := newHistoricalOntologyFixture(t)
	ctx := context.Background()
	migrator, err := storage.NewMigrator(f.admin)
	require.NoError(t, err)
	require.NoError(t, migrator.RunUpThroughEvidenceOnly(ctx))
	people, engineer := testTopic("people"), testTopic("engineer")
	handle := f.organizationEvidence(t, 0, "Ada works on Atlas.", nil)
	dependencies := []topiccontract.SourceDependency{f.source(t, handle)}
	membership := topiccontract.Record{ID: uuid.NewString(), Kind: topiccontract.AssignmentKind, Assignment: &topiccontract.Assignment{Source: handle, DefinitionID: people.ID}, Sources: dependencies}
	override := topiccontract.Record{ID: uuid.NewString(), Kind: topiccontract.OverrideKind, Override: &topiccontract.Override{Action: topiccontract.SetClassification, DefinitionID: people.ID, Members: []topiccontract.SourceHandle{handle}}, Sources: dependencies}
	seedHistoricalRecords(t, f, "seed", people, engineer, membership)
	original := seedHistoricalRecords(t, f, "historic-classification", override)
	before := f.canonicalSnapshot(t)
	require.NoError(t, migrator.RunUp(ctx))

	manager := f.actor(0, "manager")
	newOverride := override
	newOverride.ID = uuid.NewString()
	_, err = f.store.PublishManager(manager, f.team, testPublication("new-classification", 2, topiccontract.Change{Record: newOverride}))
	require.ErrorIs(t, err, topiccontract.ErrOverride)
	history, err := f.store.History(manager, f.team, 0, 20)
	require.NoError(t, err)
	require.Len(t, history, 2)

	membership.Assignment = &topiccontract.Assignment{Source: handle, DefinitionID: engineer.ID}
	changed, err := f.store.PublishAutomatic(ctx, f.team, testPublication("change-membership", 2, topiccontract.Change{ExpectedVersion: 1, Record: membership}))
	require.NoError(t, err)
	current, err := f.store.GetRecord(manager, f.team, membership.ID, 0)
	require.NoError(t, err)
	require.True(t, current.Current)
	require.Equal(t, engineer.ID, current.Assignment.DefinitionID)
	rolled, err := f.store.Rollback(manager, f.team, changed.ID, "restore-membership", changed.Revision, "restore ordinary topic membership")
	require.NoError(t, err)
	require.Equal(t, changed.Revision+1, rolled.Revision)
	restored, err := f.store.GetRecord(manager, f.team, membership.ID, 0)
	require.NoError(t, err)
	require.True(t, restored.Current)
	require.Equal(t, people.ID, restored.Assignment.DefinitionID)
	retained, err := f.store.GetRecord(manager, f.team, override.ID, 1)
	require.NoError(t, err)
	require.Equal(t, original[override.ID], retained.Record)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyRollbackRejectsStaleSources(t *testing.T) {
	f := newOntologyFixture(t)
	evidence := f.evidence(t, 0, "Atlas stores data in PostgreSQL.")
	handle := topiccontract.SourceHandle{Kind: topiccontract.EvidenceSource, ID: evidence.Evidence[0].FragmentID, Version: 1}
	first, second := testTopic("storage"), testTopic("database")
	assignment := topiccontract.Record{ID: uuid.NewString(), Kind: topiccontract.AssignmentKind, Assignment: &topiccontract.Assignment{Source: handle, DefinitionID: first.ID}, Sources: []topiccontract.SourceDependency{f.source(t, handle)}}
	_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("initial", 0, topiccontract.Change{Record: first}, topiccontract.Change{Record: second}, topiccontract.Change{Record: assignment}))
	require.NoError(t, err)
	assignment.Assignment.DefinitionID = second.ID
	changed, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("reclassify", 1, topiccontract.Change{ExpectedVersion: 1, Record: assignment}))
	require.NoError(t, err)
	_, err = f.knowledge.RetractEvidence(f.actor(0, "manager"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{handle.ID}, Reason: "source is obsolete", IdempotencyKey: "retract", RequestHash: testHash("retract")})
	require.NoError(t, err)
	_, err = f.store.Rollback(f.actor(0, "manager"), f.team, changed.ID, "unsafe-rollback", changed.Revision, "restore classification")
	require.ErrorIs(t, err, topiccontract.ErrSourceStale)
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
	handle := topiccontract.SourceHandle{Kind: topiccontract.EvidenceSource, ID: evidence.Evidence[0].FragmentID, Version: 1}
	dependency := f.source(t, handle)
	seed, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("seed", 0, topiccontract.Change{Record: testTopic("existing-generation")}))
	require.NoError(t, err)
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE memory_spaces SET generation=generation+1 WHERE team_id=?::uuid AND id=?::uuid`, f.team, f.space).Error
	}))
	_, err = f.store.GetRecord(f.actor(0, "manager"), f.team, seed.Records[0].ID, 0)
	require.ErrorIs(t, err, topiccontract.ErrUnauthorized)
	_, err = f.store.GetRecord(context.Background(), f.team, seed.Records[0].ID, 0)
	require.ErrorIs(t, err, topiccontract.ErrNotFound)
	_, err = f.store.ReadSources(context.Background(), f.team, []topiccontract.SourceHandle{handle})
	require.ErrorIs(t, err, topiccontract.ErrSourceStale)
	topic := testTopic("new-generation")
	assignment := topiccontract.Record{ID: uuid.NewString(), Kind: topiccontract.AssignmentKind, Assignment: &topiccontract.Assignment{Source: handle, DefinitionID: topic.ID}, Sources: []topiccontract.SourceDependency{dependency}}
	_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("old-source", 0, topiccontract.Change{Record: topic}, topiccontract.Change{Record: assignment}))
	require.ErrorIs(t, err, topiccontract.ErrSourceStale)
	history, err := f.store.History(context.Background(), f.team, 0, 20)
	require.NoError(t, err)
	require.Empty(t, history)
	require.Error(t, f.rls.WithTeamTx(context.Background(), f.app, f.team, func(tx *gorm.DB) error {
		return tx.Exec(`INSERT INTO topic_catalog_heads(team_id,shared_space_id,space_generation) VALUES (?::uuid,?::uuid,?)`, f.team, f.space, f.generation).Error
	}))
}

func TestOntologyUnavailableSnapshotRetainsScopedHandleForMaintenance(t *testing.T) {
	f := newOntologyFixture(t)
	handle := topiccontract.SourceHandle{Kind: topiccontract.EvidenceSource, ID: uuid.NewString(), Version: 1}
	require.NoError(t, f.rls.WithTeamTx(context.Background(), f.app, f.team, func(tx *gorm.DB) error {
		fence := scope{TeamID: f.team, SpaceID: f.space, Generation: f.generation}
		snapshot, err := readSource(tx, fence, handle)
		require.ErrorIs(t, err, topiccontract.ErrSourceStale)
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
		return tx.Raw(`SELECT status,eligible FROM topic_maintenance_sources WHERE team_id=?::uuid AND source_kind=? AND source_id=?`, f.team, handle.Kind, handle.ID).Row().Scan(&status, &eligible)
	}))
	require.Equal(t, "unavailable", status)
	require.False(t, eligible)
}

func TestOntologyOrganizationPreservesValidLargeSourceContext(t *testing.T) {
	f := newOntologyFixture(t)
	const definitions = 17
	sourceCount := 1 + definitions*(topiccontract.MaxMembers-1)
	handles := make([]topiccontract.SourceHandle, 0, sourceCount)
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
			handles = append(handles, topiccontract.SourceHandle{Kind: topiccontract.EvidenceSource, ID: fragment.FragmentID, Version: 1})
		}
	}
	require.Greater(t, len(handles), topiccontract.MaxDependencyRecords)
	dependencies := map[topiccontract.SourceHandle]topiccontract.SourceDependency{}
	require.NoError(t, f.store.withScope(context.Background(), f.team, true, func(tx *gorm.DB, fence scope) error {
		for offset := 0; offset < len(handles); offset += topiccontract.MaxDependencyRecords {
			snapshots, err := readSources(tx, fence, handles[offset:min(offset+topiccontract.MaxDependencyRecords, len(handles))])
			if err != nil {
				return err
			}
			for handle, snapshot := range snapshots {
				fingerprint, err := topiccontract.SourceFingerprint(snapshot)
				if err != nil {
					return err
				}
				dependencies[handle] = topiccontract.SourceDependency{SourceHandle: handle, Fingerprint: fingerprint}
			}
		}
		return nil
	}))
	records := make([]topiccontract.Record, definitions)
	for i := range records {
		records[i] = testTopic(fmt.Sprintf("large-context-%d", i))
		records[i].Sources = []topiccontract.SourceDependency{dependencies[handles[0]]}
		for _, handle := range handles[1+i*(topiccontract.MaxMembers-1) : 1+(i+1)*(topiccontract.MaxMembers-1)] {
			records[i].Sources = append(records[i].Sources, dependencies[handle])
		}
	}
	revision := int64(0)
	for offset := 0; offset < len(records); offset += 4 {
		changes := []topiccontract.Change{}
		for _, record := range records[offset:min(offset+4, len(records))] {
			changes = append(changes, topiccontract.Change{Record: record})
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
	handle := topiccontract.SourceHandle{Kind: topiccontract.EvidenceSource, ID: ingest.Evidence[0].FragmentID, Version: 1}
	dependency := f.source(t, handle)
	children := topiccontract.MaxDependencyRecords/2 + 1
	revision := int64(0)
	started := time.Now()
	for offset := 0; offset < children; offset += topiccontract.MaxChanges / 2 {
		changes := []topiccontract.Change{}
		for i := offset; i < min(offset+topiccontract.MaxChanges/2, children); i++ {
			parent := testTopic(fmt.Sprintf("context-parent-%d", i))
			child := testTopic(fmt.Sprintf("context-child-%d", i))
			child.Definition.ParentID = parent.ID
			child.Sources = []topiccontract.SourceDependency{dependency}
			changes = append(changes, topiccontract.Change{Record: parent}, topiccontract.Change{Record: child})
		}
		published, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(uuid.NewString(), revision, changes...))
		require.NoError(t, err)
		revision = published.Revision
		t.Logf("published %d of %d parented Topics in %s", min(offset+topiccontract.MaxChanges/2, children), children, time.Since(started).Round(time.Millisecond))
	}
	finished := observeDependencyFixturePhase(t, f.admin, "read current organization")
	contextData, err := f.store.ReadOrganization(context.Background(), f.team, []topiccontract.SourceHandle{handle})
	finished()
	require.NoError(t, err)
	require.Len(t, contextData.Records, children)
	for _, view := range contextData.Records {
		require.True(t, view.Current)
		require.NotEmpty(t, view.Definition.ParentID)
	}
	finished = observeDependencyFixturePhase(t, f.admin, "retract shared Evidence")
	_, err = f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{
		TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{handle.ID}, Reason: "source withdrawn",
		IdempotencyKey: uuid.NewString(), RequestHash: testHash(handle.ID),
	})
	finished()
	require.NoError(t, err)
	finished = observeDependencyFixturePhase(t, f.admin, "read retracted organization")
	contextData, err = f.store.ReadOrganization(context.Background(), f.team, []topiccontract.SourceHandle{handle})
	finished()
	require.NoError(t, err)
	require.Len(t, contextData.Records, children)
	for _, view := range contextData.Records {
		require.False(t, view.Current)
	}
}

func observeDependencyFixturePhase(t *testing.T, db *gorm.DB, phase string) func() {
	t.Helper()
	t.Logf("starting %s", phase)
	started := time.Now()
	sampled := make(chan struct{})
	sample := time.AfterFunc(30*time.Second, func() {
		defer close(sampled)
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		var queries []struct{ State, Wait, AgeSeconds, Statement string }
		err := db.WithContext(ctx).Raw(`SELECT state,COALESCE(wait_event_type||':'||wait_event,'running') AS wait,
			round(EXTRACT(EPOCH FROM clock_timestamp()-query_start),1)::text AS age_seconds,left(query,384) AS statement
			FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()
			AND state IN ('active','idle in transaction','idle in transaction (aborted)') ORDER BY query_start LIMIT 3`).Scan(&queries).Error
		if err != nil {
			t.Logf("PostgreSQL activity during %s was unavailable: %v", phase, err)
			return
		}
		t.Logf("PostgreSQL activity during %s: %v", phase, queries)
	})
	stop := func() {
		if sample.Stop() {
			close(sampled)
		}
		<-sampled
	}
	t.Cleanup(stop)
	return func() {
		stop()
		t.Logf("finished %s in %s", phase, time.Since(started).Round(time.Millisecond))
	}
}

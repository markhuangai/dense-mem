//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestOntologyOrganizationBatchedSources(t *testing.T) {
	f, handles, _ := organizationReadFixture(t, 0)
	other := maintenanceOtherTeam(t, f)
	foreign := other.organizationEvidence(t, 0, "Other team's PostgreSQL evidence.", nil)
	credential := f.credentials[0]
	privateContext := requestctx.WithAllowedSpaces(context.Background(), []domain.MemorySpaceAccess{{ID: credential.MemorySpaceID, Kind: domain.MemorySpacePrivate, Generation: credential.MemorySpaceGeneration}})
	private, err := f.knowledge.CreateIngestForTest(privateContext, knowledge.CreateIngestInput{TeamID: f.team, OwnerProfileID: f.owners[0], SpaceID: credential.MemorySpaceID.String(), SpaceGeneration: credential.MemorySpaceGeneration, Evidence: []knowledge.EvidenceInput{{Content: "Private PostgreSQL evidence."}}})
	require.NoError(t, err)
	stale := handles[0]
	stale.Version++
	input := []ontology.SourceHandle{handles[3], handles[2], handles[0], handles[1], handles[4], handles[3], stale,
		{Kind: ontology.EvidenceSource, ID: uuid.NewString(), Version: 1}, foreign,
		{Kind: ontology.EvidenceSource, ID: private.Evidence[0].FragmentID, Version: 1}}
	check := func(eligible int) {
		t.Helper()
		before := f.canonicalSnapshot(t)
		for _, actor := range []struct {
			team, space, owner string
			generation         int64
		}{
			{f.team, f.space, f.owners[0], f.generation},
			{f.team, f.space, f.owners[1], f.generation},
			{other.team, other.space, other.owners[0], other.generation},
		} {
			require.NoError(t, f.rls.WithTeamProfileTx(context.Background(), f.app, actor.team, actor.owner, func(tx *gorm.DB) error {
				fence := scope{TeamID: actor.team, SpaceID: actor.space, Generation: actor.generation}
				expected := organizationSingletonSources(t, tx, fence, input)
				actual, err := organizationSources(tx, fence, input)
				require.NoError(t, err)
				require.Equal(t, expected, actual)
				count := 0
				for i, snapshot := range actual {
					require.Equal(t, input[i], snapshot.SourceHandle)
					fingerprint, err := ontology.SourceFingerprint(snapshot)
					require.NoError(t, err)
					expectedFingerprint, err := ontology.SourceFingerprint(expected[i])
					require.NoError(t, err)
					require.Equal(t, expectedFingerprint, fingerprint)
					if snapshot.Eligible {
						count++
					} else {
						require.Empty(t, snapshot.OwnerID)
						require.Equal(t, map[string]string{}, snapshot.State)
					}
				}
				if actor.team == f.team {
					require.Equal(t, eligible, count)
					require.Equal(t, f.owners[1], actual[0].OwnerID)
					require.Equal(t, f.owners[0], actual[4].OwnerID)
				} else {
					require.Equal(t, 1, count)
				}
				return nil
			}))
		}
		require.Equal(t, before, f.canonicalSnapshot(t))
	}
	check(6)
	require.NoError(t, f.rls.WithTeamTx(context.Background(), f.app, f.team, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE entity_records SET status='retired',updated_at=now() WHERE team_id=?::uuid AND entity_id=?::uuid`, f.team, handles[0].ID).Error
	}))
	supported, err := f.store.ReadSources(context.Background(), f.team, handles[1:2])
	require.NoError(t, err)
	require.True(t, supported[0].Eligible)
	check(5)
	snapshots, err := f.store.ReadSources(context.Background(), f.team, handles[1:2])
	require.NoError(t, err)
	var supports [][]string
	require.NoError(t, json.Unmarshal([]byte(snapshots[0].State["supports"]), &supports))
	require.NotEmpty(t, supports)
	_, err = f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{supports[0][1]}, Reason: "withdraw relationship support", IdempotencyKey: uuid.NewString(), RequestHash: testHash("withdraw batch support")})
	require.NoError(t, err)
	check(3)
}

func TestOntologyOrganizationBatchedQueryCounts(t *testing.T) {
	f, handles, records := organizationReadFixture(t, 20)
	counter := &organizationReadCounter{Interface: f.app.Logger}
	f.app = f.app.Session(&gorm.Session{Logger: counter})
	f.store = NewStore(f.app, f.rls)
	require.NoError(t, f.store.withScope(context.Background(), f.team, true, func(tx *gorm.DB, fence scope) error {
		counter.queries.Store(0)
		expectedSources := organizationSingletonSources(t, tx, fence, handles)
		individualSources := counter.queries.Load()
		counter.queries.Store(0)
		actualSources, err := organizationSources(tx, fence, handles)
		require.NoError(t, err)
		require.Equal(t, expectedSources, actualSources)
		require.EqualValues(t, 4, counter.queries.Load())
		require.Less(t, counter.queries.Load(), individualSources)
		counter.queries.Store(0)
		var expectedViews []ontology.RecordView
		for _, record := range records {
			view, err := f.store.currentView(tx, fence, record)
			require.NoError(t, err)
			expectedViews = append(expectedViews, view)
		}
		individualViews := counter.queries.Load()
		counter.queries.Store(0)
		actualViews, err := f.store.organizationViews(tx, fence, records)
		require.NoError(t, err)
		require.Equal(t, expectedViews, actualViews)
		require.Less(t, counter.queries.Load(), individualViews)
		return nil
	}))
}

func TestOntologyOrganizationBatchedDependencyBounds(t *testing.T) {
	f := newOrganizationFixture(t)
	records := []ontology.Record{testTopic("bounded-left"), testTopic("bounded-right")}
	publication, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(uuid.NewString(), 0,
		ontology.Change{Record: records[0]}, ontology.Change{Record: records[1]}))
	require.NoError(t, err)
	for i := range records {
		records[i].Version = 1
	}
	addPins := func(target string, count int) {
		t.Helper()
		for offset := 0; offset < count; offset += ontology.MaxChanges {
			var changes []ontology.Change
			for range min(ontology.MaxChanges, count-offset) {
				changes = append(changes, ontology.Change{Record: ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.PinDefinition, TargetID: target}}})
			}
			publication, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(uuid.NewString(), publication.Revision, changes...))
			require.NoError(t, err)
		}
	}
	before := f.canonicalSnapshot(t)
	for _, record := range records {
		addPins(record.ID, ontology.MaxDependencyRecords/2)
	}
	require.NoError(t, f.store.withScope(context.Background(), f.team, true, func(tx *gorm.DB, fence scope) error {
		ids := []string{records[0].ID, records[1].ID}
		heads, err := loadHeads(tx, fence, ids[:1], nil, nil)
		require.NoError(t, err)
		records[0] = heads[records[0].ID]
		heads, err = loadHeads(tx, fence, ids[1:], nil, nil)
		require.NoError(t, err)
		records[1] = heads[records[1].ID]
		_, err = f.store.currentViews(tx, fence, records)
		require.ErrorIs(t, err, ontology.ErrContextBound)
		views, err := f.store.organizationViews(tx, fence, records)
		require.NoError(t, err)
		require.Len(t, views, 2)
		for i, view := range views {
			singleton, err := f.store.currentView(tx, fence, records[i])
			require.NoError(t, err)
			require.Equal(t, singleton, view)
			require.True(t, view.Current)
		}
		return nil
	}))
	addPins(records[0].ID, ontology.MaxDependencyRecords/2)
	require.NoError(t, f.store.withScope(context.Background(), f.team, true, func(tx *gorm.DB, fence scope) error {
		_, err := f.store.currentView(tx, fence, records[0])
		require.ErrorIs(t, err, ontology.ErrContextBound)
		_, err = f.store.organizationViews(tx, fence, records[:1])
		require.ErrorIs(t, err, ontology.ErrContextBound)
		return nil
	}))
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyOrganizationBatchedVocabulary(t *testing.T) {
	f := newOrganizationFixture(t)
	current := seedStaleVocabulary(t, f, 21, 25)
	source := f.organizationEvidence(t, 1, "PostgreSQL relational storage", nil)
	before := f.canonicalSnapshot(t)
	require.NoError(t, f.store.withScope(context.Background(), f.team, true, func(tx *gorm.DB, fence scope) error {
		sources, err := organizationSources(tx, fence, []ontology.SourceHandle{source})
		require.NoError(t, err)
		actual, err := f.store.organizationVocabulary(tx, fence, sources, []string{current[24].ID})
		require.NoError(t, err)
		require.Len(t, actual, ontology.MaxVocabularyCandidates)
		require.Equal(t, current[24].ID, actual[0].ID)
		for i, view := range actual[1:] {
			require.Equal(t, current[i].ID, view.ID)
			require.True(t, view.Current)
		}
		for _, view := range actual {
			singleton, err := f.store.currentView(tx, fence, view.Record)
			require.NoError(t, err)
			require.Equal(t, singleton, view)
		}
		return nil
	}))
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyOrganizationBatchedPublicationFences(t *testing.T) {
	for _, mutation := range []string{"source", "definition", "retirement", "override", "generation"} {
		t.Run(mutation, func(t *testing.T) {
			f, handles, records := organizationReadFixture(t, 20)
			sources := handles[3:13]
			var canonical map[string]string
			service, calls := organizationFixtureService(t, f, nil, func(_ assessment.Request, _ *assessment.Response) {
				if mutation == "source" {
					_, err := f.knowledge.RetractEvidence(f.actor(1, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[1], EvidenceIDs: []string{sources[0].ID}, Reason: "withdraw during batched assessment", IdempotencyKey: uuid.NewString(), RequestHash: testHash("batch source race")})
					require.NoError(t, err)
				} else if mutation == "generation" {
					require.NoError(t, f.admin.Exec(`UPDATE memory_spaces SET generation=generation+1 WHERE id=?::uuid`, f.space).Error)
				} else {
					record := records[0]
					expectedVersion := record.Version
					switch mutation {
					case "definition":
						record.Definition.Label = "Changed during batched assessment"
					case "retirement":
						record.Retired = true
					case "override":
						record = ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.SetClassification, Members: sources[:1], DefinitionID: records[1].ID}, Sources: []ontology.SourceDependency{f.source(t, sources[0])}}
						expectedVersion = 0
					}
					_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(uuid.NewString(), 1, ontology.Change{Record: record, ExpectedVersion: expectedVersion}))
					require.NoError(t, err)
				}
				canonical = f.canonicalSnapshot(t)
			})
			result, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: fmt.Sprintf("batch-race-%s", mutation), Sources: sources})
			require.Error(t, err)
			require.Equal(t, "stale_input", result.FailureCode)
			require.False(t, result.Current)
			require.Nil(t, result.Publication)
			require.EqualValues(t, 1, calls.Load())
			require.Equal(t, canonical, f.canonicalSnapshot(t))
		})
	}
}

//go:build integration

package postgres

import (
	"context"

	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/topics/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/topics/contract"
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
	stale := f.organizationEvidence(t, 0, "Retracted PostgreSQL batch evidence.", nil)
	_, err = f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{
		TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{stale.ID}, Reason: "withdraw batch fixture", IdempotencyKey: uuid.NewString(), RequestHash: testHash("initial batch source retraction")})
	require.NoError(t, err)
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
				invalid := handles[0]
				invalid.Version++
				_, err = organizationSources(tx, fence, []ontology.SourceHandle{invalid})
				require.ErrorIs(t, err, ontology.ErrInvalid)
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
	_, err = f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{
		TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{handles[0].ID}, Reason: "withdraw current source", IdempotencyKey: uuid.NewString(), RequestHash: testHash("batch source retraction")})
	require.NoError(t, err)
	check(5)
	_, err = f.knowledge.RetractEvidence(f.actor(1, "member"), knowledge.RetractEvidenceInput{
		TeamID: f.team, OwnerProfileID: f.owners[1], EvidenceIDs: []string{handles[1].ID}, Reason: "withdraw peer source", IdempotencyKey: uuid.NewString(), RequestHash: testHash("batch peer retraction")})
	require.NoError(t, err)
	check(4)
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
		require.EqualValues(t, 1, counter.queries.Load())
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

func TestOntologyOrganizationFreshnessStopsAfterStaleBatch(t *testing.T) {
	for _, staleIndex := range []int{-1, 0, ontology.MaxOrganizationSources, 2 * ontology.MaxOrganizationSources} {
		t.Run(fmt.Sprintf("stale_index_%d", staleIndex), func(t *testing.T) {
			f, handles, _ := organizationReadFixture(t, 2*ontology.MaxOrganizationSources+1)
			handles = handles[3:4]
			contextData, err := f.store.ReadOrganization(context.Background(), f.team, handles)
			require.NoError(t, err)
			require.Len(t, contextData.Records, 41)
			var support ontology.SourceHandle
			if staleIndex >= 0 {
				support = f.organizationEvidence(t, 1, "Additional support for one freshness dependency.", nil)
				record := contextData.Records[staleIndex].Record
				record.Sources = append(record.Sources, f.source(t, support))
				_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(uuid.NewString(), contextData.Revision,
					ontology.Change{Record: record, ExpectedVersion: record.Version}))
				require.NoError(t, err)
				contextData, err = f.store.ReadOrganization(context.Background(), f.team, handles)
				require.NoError(t, err)
			}
			receipt := ontology.OrganizationReceipt{ID: uuid.NewString(), OperationKey: uuid.NewString(), ProviderIdentity: "freshness-fixture",
				Sources: []ontology.SourceDependency{f.source(t, handles[0])}}
			receipt.InputHash, err = ontology.OrganizationInputHash(ontology.OrganizationInput{OperationKey: receipt.OperationKey, Sources: handles}, receipt.ProviderIdentity)
			require.NoError(t, err)
			receipt.BatchHash, err = ontology.OrganizationBatchHash(receipt.Sources, receipt.ProviderIdentity)
			require.NoError(t, err)
			receipt.ContextHash, err = ontology.OrganizationContextHash(contextData)
			require.NoError(t, err)
			receipt.Dependencies, err = ontology.OrganizationDependencies(contextData)
			require.NoError(t, err)
			require.Len(t, receipt.Dependencies, 41)
			receipt.Result = ontology.OrganizationResult{AssessmentID: receipt.ID, Outcomes: []ontology.OrganizationOutcome{{Source: handles[0], Status: "unchanged"}}}
			require.NoError(t, ontology.ValidateOrganizationReceipt(receipt))
			if staleIndex >= 0 {
				require.NotEqual(t, handles[0], support)
				_, err = f.knowledge.RetractEvidence(f.actor(1, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[1], EvidenceIDs: []string{support.ID},
					Reason: "withdraw dependency support", IdempotencyKey: uuid.NewString(), RequestHash: testHash("withdraw freshness support")})
				require.NoError(t, err)
			}
			before := f.canonicalSnapshot(t)
			counter := &organizationReadCounter{Interface: f.app.Logger}
			f.app = f.app.Session(&gorm.Session{Logger: counter})
			f.store = NewStore(f.app, f.rls)
			require.NoError(t, f.store.withScope(context.Background(), f.team, true, func(tx *gorm.DB, fence scope) error {
				counter.queries.Store(0)
				sources, err := organizationSources(tx, fence, handles)
				require.NoError(t, err)
				fingerprint, err := ontology.SourceFingerprint(sources[0])
				require.NoError(t, err)
				require.Equal(t, receipt.Sources[0].Fingerprint, fingerprint)
				ids := make([]string, len(receipt.Dependencies))
				for i, dependency := range receipt.Dependencies {
					ids[i] = dependency.ID
				}
				catalog, err := loadHeads(tx, fence, ids, nil)
				require.NoError(t, err)
				records := make([]ontology.Record, len(receipt.Dependencies))
				for i, dependency := range receipt.Dependencies {
					record, ok := catalog[dependency.ID]
					require.True(t, ok)
					require.Equal(t, dependency.Version, record.Version)
					require.False(t, record.Retired)
					records[i] = record
				}
				preludeReads := counter.queries.Load()
				counter.queries.Store(0)
				views, err := f.store.organizationViews(tx, fence, records)
				require.NoError(t, err)
				eagerReads := preludeReads + counter.queries.Load()
				require.Len(t, views, len(records))
				for i, view := range views {
					require.Equal(t, i != staleIndex, view.Current, "dependency %d", i)
				}
				counter.queries.Store(0)
				current, err := f.store.organizationInputsCurrent(tx, fence, receipt)
				require.NoError(t, err)
				actualReads := counter.queries.Load()
				if staleIndex < 0 {
					require.True(t, current)
					receipt.ContextHash = testHash("different freshness context")
					current, err = f.store.organizationInputsCurrent(tx, fence, receipt)
					require.NoError(t, err)
					require.False(t, current)
					return nil
				}
				require.False(t, current)
				prefix := receipt
				end := min((staleIndex/ontology.MaxOrganizationSources+1)*ontology.MaxOrganizationSources, len(records))
				prefix.Dependencies = prefix.Dependencies[:end]
				counter.queries.Store(0)
				current, err = f.store.organizationInputsCurrent(tx, fence, prefix)
				require.NoError(t, err)
				require.False(t, current)
				require.Equal(t, counter.queries.Load(), actualReads, "dependencies after the stale group must add no reads")
				if end < len(records) {
					require.Less(t, actualReads, eagerReads)
				} else {
					require.Equal(t, eagerReads, actualReads)
				}
				t.Logf("dependencies=%d stale_index=%d eager_sql_reads=%d early_exit_sql_reads=%d avoided=%d", len(records), staleIndex, eagerReads, actualReads, eagerReads-actualReads)
				return nil
			}))
			require.Equal(t, before, f.canonicalSnapshot(t))
		})
	}
}

func TestOntologyOrganizationBatchedDependencyBounds(t *testing.T) {
	f := newOrganizationFixture(t)
	ctx := f.actor(0, "manager")
	before := f.canonicalSnapshot(t)
	var roots []ontology.Record
	var revision int64
	publish := func(changes ...ontology.Change) {
		t.Helper()
		result, err := f.store.PublishManager(ctx, f.team, testPublication(uuid.NewString(), revision, changes...))
		require.NoError(t, err)
		revision = result.Revision
	}
	for _, key := range []string{"bounded-left", "bounded-right"} {
		root := testTopic(key)
		for batch := range ontology.MaxDependencyRecords / (2 * ontology.MaxChanges) {
			var changes []ontology.Change
			branch := testTopic(fmt.Sprintf("%s-branch-%02d", key, batch))
			for leaf := range ontology.MaxChanges - 1 {
				child := testTopic(fmt.Sprintf("%s-leaf-%02d-%02d", key, batch, leaf))
				branch.Dependencies = append(branch.Dependencies, ontology.RevisionRef{ID: child.ID, Version: 1})
				changes = append(changes, ontology.Change{Record: child})
			}
			publish(append(changes, ontology.Change{Record: branch})...)
			root.Dependencies = append(root.Dependencies, ontology.RevisionRef{ID: branch.ID, Version: 1})
		}
		publish(ontology.Change{Record: root})
		view, err := f.store.GetRecord(ctx, f.team, root.ID, 0)
		require.NoError(t, err)
		require.True(t, view.Current)
		roots = append(roots, view.Record)
	}
	require.NoError(t, f.store.withScope(ctx, f.team, true, func(tx *gorm.DB, fence scope) error {
		_, err := f.store.currentViews(tx, fence, roots)
		require.ErrorIs(t, err, ontology.ErrContextBound)
		views, err := f.store.organizationViews(tx, fence, roots)
		require.NoError(t, err)
		require.Len(t, views, len(roots))
		for index, view := range views {
			singleton, err := f.store.currentView(tx, fence, roots[index])
			require.NoError(t, err)
			require.Equal(t, singleton, view)
			require.True(t, view.Current)
		}
		dependencies, err := ontology.OrganizationDependencies(ontology.OrganizationContext{Candidates: views})
		require.NoError(t, err)
		require.Len(t, dependencies, len(roots))
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
	for _, mutation := range []string{"source", "definition", "retirement", "new_topical_candidate", "generation"} {
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
					case "new_topical_candidate":
						record = testTopic("postgresql")
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

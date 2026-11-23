//go:build integration && ontology_study

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type pendingStudyRow struct {
	Version, Revision                                        int64
	Status, Reason, Fingerprint, Meaning, Search, Vocabulary string
	PendingAt, UpdatedAt                                     time.Time
}

func readPendingStudyRow(t *testing.T, f *ontologyFixture, source ontology.SourceHandle) pendingStudyRow {
	t.Helper()
	var row pendingStudyRow
	require.NoError(t, f.admin.Raw(`SELECT source_version,revision,status,reason,fingerprint,meaning_key,search_tsv::text,
		vocabulary_words::text,pending_at,updated_at FROM ontology_maintenance_sources
		WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND source_kind=? AND source_id=?`,
		f.team, f.space, f.generation, source.Kind, source.ID).Row().Scan(&row.Version, &row.Revision, &row.Status, &row.Reason,
		&row.Fingerprint, &row.Meaning, &row.Search, &row.Vocabulary, &row.PendingAt, &row.UpdatedAt))
	return row
}

func pendingStudyTurn(t *testing.T, f *ontologyFixture, window *ontology.MaintenanceWindow) *ontology.MaintenanceTurn {
	t.Helper()
	turn, err := f.store.ClaimMaintenanceTurn(context.Background(), window.ID, time.Now(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, turn)
	require.Equal(t, f.team, turn.TeamID)
	t.Cleanup(func() { require.NoError(t, f.store.ReleaseMaintenanceTurn(context.Background(), *turn)) })
	return turn
}

func pendingStudyPage(ctx context.Context, store *Store, turn *ontology.MaintenanceTurn, observe func(maintenanceDiscoveryEvent)) (ontology.MaintenanceDiscoveryResult, studyQueueChanges, error) {
	var changes studyQueueChanges
	ctx = context.WithValue(ctx, studyQueueKey{}, &changes)
	ctx = context.WithValue(ctx, maintenanceDiscoveryObservationKey{}, maintenanceDiscoveryObserver(func(event maintenanceDiscoveryEvent) {
		changes.observe(event)
		if observe != nil {
			observe(event)
		}
	}))
	result, err := store.DiscoverMaintenance(ctx, *turn, ontology.MaintenancePageSize)
	if !result.Committed {
		changes = studyQueueChanges{}
	}
	return result, changes, err
}

func pendingStudyDrainMarkers(t *testing.T, f *ontologyFixture, turn *ontology.MaintenanceTurn) studyQueueChanges {
	t.Helper()
	var total studyQueueChanges
	for range 12 {
		result, changes, err := pendingStudyPage(context.Background(), f.store, turn, nil)
		require.NoError(t, err)
		require.True(t, result.Committed)
		total.PendingSkips += changes.PendingSkips
		total.RefreshedPending += changes.RefreshedPending
		total.ClaimReads += changes.ClaimReads
		total.DefinitionPages += changes.DefinitionPages
		if markers, _ := studyMarkerInfo(t, f); markers == 0 {
			return total
		}
	}
	t.Fatal("study fixture did not drain its markers")
	return total
}

func assertUnclaimedRefresh(t *testing.T, before, after pendingStudyRow, changes studyQueueChanges, skip bool) {
	t.Helper()
	require.Positive(t, changes.DefinitionPages)
	require.Equal(t, before.PendingAt, after.PendingAt)
	if skip {
		require.Equal(t, before, after, "skip must preserve queue state and both timestamps")
		require.Equal(t, 1, changes.PendingSkips)
		require.Zero(t, changes.RefreshedPending)
		require.Equal(t, changes.DefinitionPages, changes.ClaimReads)
	} else {
		require.Equal(t, before.Revision+1, after.Revision)
		require.False(t, after.UpdatedAt.Before(before.UpdatedAt))
		require.Equal(t, 1, changes.RefreshedPending)
		require.Zero(t, changes.PendingSkips)
		require.Zero(t, changes.ClaimReads)
	}
}

func pendingStudyEdit(t *testing.T, f *ontologyFixture, topic *ontology.Record, version int64) {
	t.Helper()
	topic.Definition.Description = fmt.Sprintf("PostgreSQL catalog edit %d", version)
	revalidationPublish(t, f, ontology.Change{ExpectedVersion: version, Record: *topic})
}

func TestOntologyPendingRefreshStudy(t *testing.T) {
	if os.Getenv("ONTOLOGY_STUDY_VARIANT") != "pending-refresh" {
		t.Skip("the pending-refresh study requires its hosted Go overlay")
	}
	arm := os.Getenv("ONTOLOGY_STUDY_ACTIVE_ARM")
	require.Contains(t, studyPendingArms, arm)
	skip := arm == "base-skip" || arm == "vector-skip"
	ctx := context.Background()

	t.Run("three-commits-before-pickup", func(t *testing.T) {
		f := newOrganizationFixture(t)
		observeStudyQueue(t, f)
		config := concurrencyConfig(t, f, 8, 8)
		window := maintenanceWindow(t, f, config)
		a := discoveryEvidence(t, f, 1)
		topic := testTopic("postgresql")
		revalidationPublish(t, f, ontology.Change{Record: topic})
		revalidationDiscover(t, f, window)
		turn := pendingStudyTurn(t, f, window)
		other, err := NewStore(f.app, f.rls).ClaimMaintenanceTurn(ctx, window.ID, time.Now(), time.Minute)
		require.NoError(t, err)
		require.Nil(t, other, "eight slots allow one preparer per team")
		original := readPendingStudyRow(t, f, a)
		require.Equal(t, "pending", original.Status)
		for version := int64(1); version <= 3; version++ {
			pendingStudyEdit(t, f, &topic, version)
			before := readPendingStudyRow(t, f, a)
			require.Equal(t, original.PendingAt, before.PendingAt)
			result, changes, err := pendingStudyPage(ctx, f.store, turn, nil)
			require.NoError(t, err)
			require.True(t, result.Committed)
			require.Equal(t, 1, result.Counts.CompletedMarkers)
			assertUnclaimedRefresh(t, before, readPendingStudyRow(t, f, a), changes, skip)
		}
		require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn))
		seen := make(chan assessment.Request, 1)
		service, _ := maintenanceServiceFixture(t, f, config, func(request assessment.Request, _ *assessment.Response) {
			select {
			case seen <- request:
			default:
			}
		})
		progress, err := service.RunTurn(ctx)
		require.NoError(t, err)
		require.True(t, progress)
		select {
		case request := <-seen:
			require.Len(t, request.Definitions, 1)
			require.Equal(t, topic.Definition.Description, request.Definitions[0].Description)
		default:
			t.Fatal("no provider request after consuming the three topic edits")
		}
		require.Contains(t, studyRecall(t, f, topic.Definition.Key).Sources, a)
	})

	t.Run("active-group-peer-keeps-the-correction", func(t *testing.T) {
		f := newOrganizationFixture(t)
		observeStudyQueue(t, f)
		const sharedMeaning = "PostgreSQL storage for Atlas."
		a := pendingStudyEvidence(t, f, 1, sharedMeaning)
		b := pendingStudyEvidence(t, f, 2, sharedMeaning)
		group := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind,
			Override: &ontology.Override{Action: ontology.GroupTogether, Members: []ontology.SourceHandle{a, b}},
			Sources:  []ontology.SourceDependency{f.source(t, a), f.source(t, b)}}
		topic := testTopic("postgresql")
		revalidationPublish(t, f, ontology.Change{Record: topic}, ontology.Change{Record: group})
		config := concurrencyConfig(t, f, 8, 8)
		window := maintenanceWindow(t, f, config)
		_, claim := maintenanceClaim(t, f, window)
		require.Equal(t, []ontology.SourceHandle{a, b}, claim.Sources, "b must be a non-seed member of the claimed closure")
		arrived, resume := make(chan struct{}), make(chan struct{})
		resumeProvider := sync.OnceFunc(func() { close(resume) })
		defer resumeProvider()
		started := sync.OnceFunc(func() { close(arrived) })
		service, _ := organizationFixtureService(t, f, nil, func(_ assessment.Request, _ *assessment.Response) {
			started()
			<-resume
		})
		type outcome struct {
			result ontology.OrganizationResult
			err    error
		}
		done := make(chan outcome, 1)
		go func() {
			result, err := service.Organize(ontology.WithMaintenanceClaim(ctx, *claim), f.team,
				ontology.OrganizationInput{OperationKey: "maintenance:" + claim.ID, Sources: claim.Sources})
			done <- outcome{result, err}
		}()
		select {
		case <-arrived:
		case result := <-done:
			t.Fatalf("organization exited before HTTP request: %v", result.err)
		case <-time.After(10 * time.Second):
			t.Fatal("no provider request for claimed group")
		}
		fix := testTopic("manager-replacement")
		fix.Definition.Description = "PostgreSQL correction"
		override := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind,
			Override: &ontology.Override{Action: ontology.SetClassification, DefinitionID: fix.ID, Members: []ontology.SourceHandle{b}},
			Sources:  []ontology.SourceDependency{f.source(t, b)}}
		before := readPendingStudyRow(t, f, b)
		revalidationPublish(t, f, ontology.Change{Record: fix}, ontology.Change{Record: override})
		turn := pendingStudyTurn(t, f, window)
		changes := pendingStudyDrainMarkers(t, f, turn)
		after := readPendingStudyRow(t, f, b)
		require.Greater(t, after.Revision, claim.Revisions[ontology.SourceKey(b)])
		require.Equal(t, before.PendingAt, after.PendingAt)
		require.Positive(t, changes.RefreshedPending)
		require.Zero(t, changes.PendingSkips)
		require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn))
		resumeProvider()
		select {
		case stale := <-done:
			require.Error(t, stale.err)
			require.Equal(t, "stale_input", stale.result.FailureCode)
			require.False(t, stale.result.Current)
			require.NoError(t, f.store.CompleteMaintenanceBatch(ctx, *claim, stale.result, stale.result.FailureCode, time.Now()))
		case <-time.After(10 * time.Second):
			t.Fatal("claimed work did not reconcile")
		}
		require.Equal(t, "pending", readPendingStudyRow(t, f, b).Status)
		current, _ := maintenanceServiceFixture(t, f, config, nil)
		drainMaintenance(t, current)
		require.Equal(t, "organized", readPendingStudyRow(t, f, b).Status)
		require.Contains(t, studyRecall(t, f, fix.Definition.Key).Sources, b)
	})

	t.Run("repairs-changed-queue-fields-and-withdrawal", func(t *testing.T) {
		f := newOrganizationFixture(t)
		observeStudyQueue(t, f)
		a := discoveryEvidence(t, f, 2)
		topic := testTopic("postgresql")
		revalidationPublish(t, f, ontology.Change{Record: topic})
		window := maintenanceWindow(t, f, concurrencyConfig(t, f, 8, 8))
		_, claim := maintenanceClaim(t, f, window)
		require.NoError(t, f.store.CompleteMaintenanceBatch(ctx, *claim, ontology.OrganizationResult{}, "maintenance_paused", time.Now()))
		before := readPendingStudyRow(t, f, a)
		require.Equal(t, "maintenance_paused", before.Reason)
		turn := pendingStudyTurn(t, f, window)
		pendingStudyEdit(t, f, &topic, 1)
		changes := pendingStudyDrainMarkers(t, f, turn)
		normal := readPendingStudyRow(t, f, a)
		require.Empty(t, normal.Reason)
		require.Equal(t, before.Revision+1, normal.Revision)
		require.Equal(t, 1, changes.RefreshedPending)
		require.Zero(t, changes.PendingSkips)

		repairs := []string{"source_version=source_version+1", "fingerprint='different'", "meaning_key='different'",
			"search_tsv=to_tsvector('simple','different')", "vocabulary_words=ARRAY['different']"}
		for index, repair := range repairs {
			before := readPendingStudyRow(t, f, a)
			pendingStudyEdit(t, f, &topic, int64(index+2))
			var changed bool
			result, changes, err := pendingStudyPage(ctx, f.store, turn, func(event maintenanceDiscoveryEvent) {
				if event.Phase == "source_selected" && event.Source == a && !changed {
					updated := f.admin.Exec(`UPDATE ontology_maintenance_sources SET `+repair+
						` WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND source_id=?`, f.team, f.space, f.generation, a.ID)
					require.NoError(t, updated.Error)
					require.EqualValues(t, 1, updated.RowsAffected)
					changed = true
				}
			})
			require.NoError(t, err)
			require.True(t, result.Committed && changed)
			repaired := readPendingStudyRow(t, f, a)
			require.Equal(t, before.Revision+1, repaired.Revision, repair)
			require.Equal(t, normal.PendingAt, repaired.PendingAt, repair)
			repaired.Revision, repaired.UpdatedAt = normal.Revision, normal.UpdatedAt
			require.Equal(t, normal, repaired, repair)
			require.Equal(t, 1, changes.RefreshedPending)
			require.Zero(t, changes.PendingSkips)
		}
		pendingStudyEdit(t, f, &topic, int64(len(repairs)+2))
		_, err := f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{TeamID: f.team,
			OwnerProfileID: f.owners[0], EvidenceIDs: []string{a.ID}, Reason: "withdraw before refresh",
			IdempotencyKey: uuid.NewString(), RequestHash: testHash("withdraw before refresh")})
		require.NoError(t, err)
		changes = pendingStudyDrainMarkers(t, f, turn)
		require.Zero(t, changes.PendingSkips)
		require.Equal(t, "unavailable", readPendingStudyRow(t, f, a).Status)
	})

	t.Run("expired-claim-and-cancelled-page", func(t *testing.T) {
		f := newOrganizationFixture(t)
		observeStudyQueue(t, f)
		a := discoveryEvidence(t, f, 1)
		topic := testTopic("postgresql")
		revalidationPublish(t, f, ontology.Change{Record: topic})
		window := maintenanceWindow(t, f, concurrencyConfig(t, f, 8, 8))
		_, claim := maintenanceClaim(t, f, window)
		turn := pendingStudyTurn(t, f, window)
		pendingStudyEdit(t, f, &topic, 1)
		require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_batches SET lease_until=clock_timestamp()-interval '1 second'
			WHERE batch_id=?::uuid`, claim.ID).Error)
		before := readPendingStudyRow(t, f, a)
		cancelCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		reached := false
		result, changes, err := pendingStudyPage(cancelCtx, f.store, turn, func(event maintenanceDiscoveryEvent) {
			if event.Phase == "source_finished" && event.Source == a {
				reached = true
				cancel()
			}
		})
		require.Error(t, err)
		require.True(t, reached)
		require.False(t, result.Committed)
		require.Empty(t, changes)
		require.Equal(t, before, readPendingStudyRow(t, f, a))
		markers, _ := studyMarkerInfo(t, f)
		require.Equal(t, 1, markers)
		result, changes, err = pendingStudyPage(ctx, NewStore(f.app, f.rls), turn, nil)
		require.NoError(t, err)
		require.True(t, result.Committed)
		require.Equal(t, 1, result.Counts.CompletedMarkers)
		assertUnclaimedRefresh(t, before, readPendingStudyRow(t, f, a), changes, skip)
		require.ErrorIs(t, f.store.CompleteMaintenanceBatch(ctx, *claim, ontology.OrganizationResult{}, "", time.Now()), ontology.ErrLeaseLost)
	})

	t.Run("colliding-claim-keys-across-teams-and-generations", func(t *testing.T) {
		f := newOrganizationFixture(t)
		other := maintenanceOtherTeam(t, f)
		oldGeneration := f.generation
		require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
			return tx.Exec(`UPDATE memory_spaces SET generation=generation+1 WHERE team_id=?::uuid AND id=?::uuid`, f.team, f.space).Error
		}))
		f.generation++
		credentials := access.NewCredentialRepository(f.app, f.rls, nil)
		manager, err := credentials.GetActiveByID(ctx, f.credentials[0].ID)
		require.NoError(t, err)
		require.NotNil(t, manager)
		require.Equal(t, f.generation, manager.TeamSharedSpaceGeneration)
		f.credentials[0] = manager
		observeStudyQueue(t, f)
		a := discoveryEvidence(t, f, 1)
		foreign := discoveryEvidence(t, other, 1)
		require.Equal(t, a, foreign)
		topic := testTopic("postgresql")
		revalidationPublish(t, f, ontology.Change{Record: topic})
		window := maintenanceWindow(t, f, concurrencyConfig(t, f, 8, 8))
		// Retain a scoped live turn while installing batch fixtures for the other scopes.
		turn, err := f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now(), time.Minute)
		require.NoError(t, err)
		require.NotNil(t, turn)
		if turn.TeamID == other.team {
			require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn))
			turn = pendingStudyTurn(t, f, window)
		} else {
			t.Cleanup(func() { require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn)) })
		}
		require.Equal(t, f.team, turn.TeamID)
		pendingStudyDrainMarkers(t, f, turn)
		payload, err := json.Marshal([]ontology.SourceHandle{a})
		require.NoError(t, err)
		for _, scope := range []scope{{TeamID: other.team, SpaceID: other.space, Generation: other.generation}, {TeamID: f.team, SpaceID: f.space, Generation: oldGeneration}} {
			require.NoError(t, f.admin.Exec(`INSERT INTO ontology_maintenance_batches(batch_id,run_id,window_id,team_id,shared_space_id,space_generation,lease_token,lease_until,sources,revisions,created_at)
				VALUES(?::uuid,?::uuid,?::uuid,?::uuid,?::uuid,?,?::uuid,clock_timestamp()+interval '5 minutes',?::jsonb,'{}'::jsonb,clock_timestamp())`,
				uuid.NewString(), turn.RunID, window.ID, scope.TeamID, scope.SpaceID, scope.Generation, uuid.NewString(), string(payload)).Error)
		}
		var forced, restricted bool
		require.NoError(t, f.app.Raw(`SELECT relforcerowsecurity,NOT (rolsuper OR rolbypassrls)
			FROM pg_class JOIN pg_roles ON rolname=current_user WHERE pg_class.oid='ontology_maintenance_batches'::regclass`).Row().Scan(&forced, &restricted))
		require.True(t, forced && restricted)
		before := readPendingStudyRow(t, f, a)
		pendingStudyEdit(t, f, &topic, 1)
		result, changes, err := pendingStudyPage(ctx, f.store, turn, nil)
		require.NoError(t, err)
		require.True(t, result.Committed)
		require.Equal(t, 1, result.Counts.CompletedMarkers)
		assertUnclaimedRefresh(t, before, readPendingStudyRow(t, f, a), changes, skip)
	})
}

func pendingStudyEvidence(t *testing.T, f *ontologyFixture, index int, text string) ontology.SourceHandle {
	t.Helper()
	id := fmt.Sprintf("00000000-0000-4000-8000-%012d", index)
	result, err := f.knowledge.CreateIngestForTest(context.Background(), knowledge.CreateIngestInput{TeamID: f.team,
		OwnerProfileID: f.owners[0], IdempotencyKey: uuid.NewString(), RequestHash: testHash(text),
		Evidence: []knowledge.EvidenceInput{{FragmentID: id, Content: text, ForceInsert: true}}})
	require.NoError(t, err)
	return ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: result.Evidence[0].FragmentID, Version: 1}
}

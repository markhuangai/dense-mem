//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	privacy "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyMigrationRLSAndHistory(t *testing.T) {
	f := newOntologyFixtureWithMaintenance(t, false)
	require.NoError(t, privacy.NewPrivateMemoryRepository(f.app, f.rls).Prepare(context.Background()))
	before := f.canonicalSnapshot(t)
	var forced, secured int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FILTER(WHERE relforcerowsecurity),count(*) FILTER(WHERE relrowsecurity)
		FROM pg_class WHERE relname=ANY(ARRAY['ontology_catalog_heads','ontology_publications','ontology_record_heads',
		'ontology_record_revisions','ontology_source_dependencies','ontology_revision_dependencies'])`).Row().Scan(&forced, &secured))
	require.Equal(t, 6, forced)
	require.Equal(t, 6, secured)
	var super, bypass bool
	require.NoError(t, f.app.Raw(`SELECT rolsuper,rolbypassrls FROM pg_roles WHERE rolname=current_user`).Row().Scan(&super, &bypass))
	require.False(t, super)
	require.False(t, bypass)
	migrator, err := storage.NewMigrator(f.admin)
	require.NoError(t, err)
	require.NoError(t, migrator.RunDown(context.Background()))
	require.Equal(t, before, f.canonicalSnapshot(t))
	require.NoError(t, migrator.RunUp(context.Background()))
	require.NoError(t, migrator.RunDown(context.Background()))
	require.NoError(t, migrator.RunDown(context.Background()))
	require.NoError(t, f.admin.Exec(`GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO ontology_app`).Error)
	seed, err := f.store.SeedDefinitions(context.Background(), f.team, ontology.SeedInput{OperationKey: "seed", ExpectedRevision: 0, Limit: 20})
	require.NoError(t, err)
	require.Len(t, seed.Records, 8)
	replay, err := f.store.SeedDefinitions(context.Background(), f.team, ontology.SeedInput{OperationKey: "seed", ExpectedRevision: 0, Limit: 20})
	require.NoError(t, err)
	require.True(t, replay.Existing)
	require.Equal(t, seed.ID, replay.ID)
	_, err = f.store.SeedDefinitions(context.Background(), f.team, ontology.SeedInput{OperationKey: "seed", ExpectedRevision: 0, Limit: 1})
	require.ErrorIs(t, err, ontology.ErrConflict)
	page, err := f.store.ListRecords(f.actor(1, "member"), f.team, "", "", 3)
	require.NoError(t, err)
	require.Len(t, page.Records, 3)
	require.NotEmpty(t, page.NextID)
	next, err := f.store.ListRecords(f.actor(1, "member"), f.team, "", page.NextID, 200)
	require.NoError(t, err)
	require.Len(t, next.Records, 5)
	history, err := f.store.History(f.actor(0, "manager"), f.team, 0, 20)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, "seed", history[0].Origin)
	require.NoError(t, migrator.RunDown(context.Background()))
	require.ErrorContains(t, migrator.RunDown(context.Background()), "cannot roll back populated ontology history")
	require.NoError(t, migrator.RunUp(context.Background()))
	require.Error(t, f.admin.Exec(`UPDATE ontology_record_revisions SET body='{}'::jsonb`).Error)
	require.Error(t, f.admin.Exec(`DELETE FROM ontology_publications`).Error)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyAtomicPublicationReplayAndRaces(t *testing.T) {
	f := newOntologyFixture(t)
	before := f.canonicalSnapshot(t)
	first := testTopic("engineering")
	initial := testPublication("first", 0, ontology.Change{Record: first})
	result, err := f.store.PublishManager(f.actor(0, "manager"), f.team, initial)
	require.NoError(t, err)
	replay, err := f.store.PublishManager(f.actor(0, "manager"), f.team, initial)
	require.NoError(t, err)
	require.True(t, replay.Existing)
	require.Equal(t, result.ID, replay.ID)
	changed := initial
	changed.Reason = "different operation"
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, changed)
	require.ErrorIs(t, err, ontology.ErrConflict)
	collision := testTopic("databases")
	collision.Definition.Aliases = []string{"ENGINEERING"}
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("collision", 1, ontology.Change{Record: testTopic("valid")}, ontology.Change{Record: collision}))
	require.ErrorIs(t, err, ontology.ErrInvalid)
	history, err := f.store.History(f.actor(0, "manager"), f.team, 0, 20)
	require.NoError(t, err)
	require.Len(t, history, 1)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, key := range []string{"race-a", "race-b"} {
		wait.Add(1)
		go func(key string) {
			defer wait.Done()
			<-start
			_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(key, 1, ontology.Change{Record: testTopic(key)}))
			results <- err
		}(key)
	}
	close(start)
	wait.Wait()
	close(results)
	passed, conflicts := 0, 0
	for err := range results {
		if err == nil {
			passed++
		} else if errors.Is(err, ontology.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	require.Equal(t, 1, passed)
	require.Equal(t, 1, conflicts)
	view, err := f.store.GetRecord(f.actor(1, "member"), f.team, first.ID, 0)
	require.NoError(t, err)
	require.True(t, view.Current)
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("bad-version", 2, ontology.Change{ExpectedVersion: 0, Record: view.Record}))
	require.ErrorIs(t, err, ontology.ErrConflict)
	view.Definition.Label = "Engineering work"
	updated, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("edit", 2, ontology.Change{ExpectedVersion: 1, Record: view.Record}))
	require.NoError(t, err)
	rollback, err := f.store.Rollback(f.actor(0, "manager"), f.team, updated.ID, "rollback", 3, "restore previous label")
	require.NoError(t, err)
	require.Equal(t, int64(4), rollback.Revision)
	replayedRollback, err := f.store.Rollback(f.actor(0, "manager"), f.team, updated.ID, "rollback", 3, "restore previous label")
	require.NoError(t, err)
	require.True(t, replayedRollback.Existing)
	require.Equal(t, rollback.ID, replayedRollback.ID)
	current, err := f.store.GetRecord(f.actor(0, "manager"), f.team, first.ID, 0)
	require.NoError(t, err)
	require.Equal(t, "engineering", current.Definition.Label)
	require.Equal(t, int64(3), current.Version)
	old, err := f.store.GetRecord(f.actor(0, "manager"), f.team, first.ID, 2)
	require.NoError(t, err)
	require.Equal(t, "Engineering work", old.Definition.Label)
	require.False(t, old.Current)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyRejectedInputsDoNotCommit(t *testing.T) {
	f := newOntologyFixture(t)
	_, err := f.store.PublishAutomatic(context.Background(), f.team, testPublication("bad", 0, ontology.Change{Record: ontology.Record{ID: uuid.NewString(), Kind: ontology.AssignmentKind}}))
	require.ErrorIs(t, err, ontology.ErrInvalid)
	_, err = f.store.PublishManager(f.actor(1, "member"), f.team, testPublication("member", 0, ontology.Change{Record: testTopic("member")}))
	require.ErrorIs(t, err, ontology.ErrUnauthorized)
	_, err = f.store.PublishAutomatic(f.actor(0, "manager"), f.team, testPublication("request-automatic", 0, ontology.Change{Record: testTopic("automatic")}))
	require.ErrorIs(t, err, ontology.ErrUnauthorized)
	page, err := f.store.ListRecords(context.Background(), f.team, "", "", 20)
	require.NoError(t, err)
	require.Empty(t, page.Records)
	require.Zero(t, page.Revision)
	var missing int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_publications WHERE team_id=?::uuid`, f.team).Row().Scan(&missing))
	require.Zero(t, missing)
	require.NoError(t, f.rls.WithTeamTx(context.Background(), f.app, f.team, func(tx *gorm.DB) error {
		return tx.Exec(`INSERT INTO ontology_catalog_heads(team_id,shared_space_id,space_generation) VALUES (?::uuid,?::uuid,?)`, f.team, f.space, f.generation).Error
	}))
	_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("oversized", 0, ontology.Change{Record: ontology.Record{ID: uuid.NewString(), Kind: ontology.Topic, Definition: &ontology.Definition{Key: "oversized", Label: "oversized", Description: strings.Repeat("x", 1001)}}}))
	require.ErrorIs(t, err, ontology.ErrInvalid)
}

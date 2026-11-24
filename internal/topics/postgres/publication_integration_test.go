//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	privacy "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyMigrationRLSAndHistory(t *testing.T) {
	f := newOntologyFixture(t)
	require.NoError(t, privacy.NewPrivateMemoryRepository(f.app, f.rls).Prepare(context.Background()))
	before := f.canonicalSnapshot(t)
	var forced, secured int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FILTER(WHERE relforcerowsecurity),count(*) FILTER(WHERE relrowsecurity)
 FROM pg_class WHERE relname=ANY(ARRAY['topic_catalog_heads','topic_publications','topic_record_heads',
 'topic_record_revisions','topic_source_dependencies','topic_revision_dependencies'])`).Row().Scan(&forced, &secured))
	require.Equal(t, 6, forced)
	require.Equal(t, 6, secured)
	var super, bypass bool
	require.NoError(t, f.app.Raw(`SELECT rolsuper,rolbypassrls FROM pg_roles WHERE rolname=current_user`).Row().Scan(&super, &bypass))
	require.False(t, super)
	require.False(t, bypass)
	var changes []topic.Change
	for _, key := range []string{"one", "two", "three", "four", "five", "six", "seven", "eight"} {
		changes = append(changes, topic.Change{Record: testTopic(key)})
	}
	input := testPublication("initial-topics", 0, changes...)
	initial, err := f.store.PublishManager(f.actor(0, "manager"), f.team, input)
	require.NoError(t, err)
	require.Len(t, initial.Records, 8)
	replay, err := f.store.PublishManager(f.actor(0, "manager"), f.team, input)
	require.NoError(t, err)
	require.True(t, replay.Existing)
	require.Equal(t, initial.ID, replay.ID)
	input.Reason = "changed replay"
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, input)
	require.ErrorIs(t, err, topic.ErrConflict)
	page, err := f.store.ListRecords(f.actor(1, "member"), f.team, topic.Topic, "", 3)
	require.NoError(t, err)
	require.Len(t, page.Records, 3)
	require.NotEmpty(t, page.NextID)
	next, err := f.store.ListRecords(f.actor(1, "member"), f.team, topic.Topic, page.NextID, 200)
	require.NoError(t, err)
	require.Len(t, next.Records, 5)
	history, err := f.store.History(f.actor(0, "manager"), f.team, 0, 20)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, "manager", history[0].Origin)
	require.ErrorContains(t, f.admin.Exec(`UPDATE topic_record_revisions SET fingerprint=fingerprint`).Error, "topic_record_revisions is append-only")
	require.ErrorContains(t, f.admin.Exec(`UPDATE topic_revision_payloads SET body=body`).Error, "topic_revision_payloads is append-only")
	require.ErrorContains(t, f.admin.Exec(`DELETE FROM topic_publications`).Error, "topic_publications is append-only")
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyAtomicPublicationReplayAndRaces(t *testing.T) {
	f := newOntologyFixture(t)
	before := f.canonicalSnapshot(t)
	first := testTopic("engineering")
	initial := testPublication("first", 0, topic.Change{Record: first})
	result, err := f.store.PublishManager(f.actor(0, "manager"), f.team, initial)
	require.NoError(t, err)
	replay, err := f.store.PublishManager(f.actor(0, "manager"), f.team, initial)
	require.NoError(t, err)
	require.True(t, replay.Existing)
	require.Equal(t, result.ID, replay.ID)
	changed := initial
	changed.Reason = "different operation"
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, changed)
	require.ErrorIs(t, err, topic.ErrConflict)
	collision := testTopic("databases")
	collision.Definition.Aliases = []string{"ENGINEERING"}
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("collision", 1, topic.Change{Record: testTopic("valid")}, topic.Change{Record: collision}))
	require.ErrorIs(t, err, topic.ErrInvalid)
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
			_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(key, 1, topic.Change{Record: testTopic(key)}))
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
		} else if errors.Is(err, topic.ErrConflict) {
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
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("bad-version", 2, topic.Change{ExpectedVersion: 0, Record: view.Record}))
	require.ErrorIs(t, err, topic.ErrConflict)
	view.Definition.Label = "Engineering work"
	updated, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("edit", 2, topic.Change{ExpectedVersion: 1, Record: view.Record}))
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
	_, err := f.store.PublishAutomatic(context.Background(), f.team, testPublication("bad", 0, topic.Change{Record: topic.Record{ID: uuid.NewString(), Kind: topic.AssignmentKind}}))
	require.ErrorIs(t, err, topic.ErrInvalid)
	_, err = f.store.PublishManager(f.actor(1, "member"), f.team, testPublication("member", 0, topic.Change{Record: testTopic("member")}))
	require.ErrorIs(t, err, topic.ErrUnauthorized)
	_, err = f.store.PublishAutomatic(f.actor(0, "manager"), f.team, testPublication("request-automatic", 0, topic.Change{Record: testTopic("automatic")}))
	require.ErrorIs(t, err, topic.ErrUnauthorized)
	page, err := f.store.ListRecords(context.Background(), f.team, "", "", 20)
	require.NoError(t, err)
	require.Empty(t, page.Records)
	require.Zero(t, page.Revision)
	var missing int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM topic_publications WHERE team_id=?::uuid`, f.team).Row().Scan(&missing))
	require.Zero(t, missing)
	require.NoError(t, f.rls.WithTeamTx(context.Background(), f.app, f.team, func(tx *gorm.DB) error {
		return tx.Exec(`INSERT INTO topic_catalog_heads(team_id,shared_space_id,space_generation) VALUES (?::uuid,?::uuid,?)`, f.team, f.space, f.generation).Error
	}))
	_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("oversized", 0, topic.Change{Record: topic.Record{ID: uuid.NewString(), Kind: topic.Topic, Definition: &topic.Definition{Key: "oversized", Label: "oversized", Description: strings.Repeat("x", 1001)}}}))
	require.ErrorIs(t, err, topic.ErrInvalid)
}

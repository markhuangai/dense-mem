//go:build integration

package postgres

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	community "github.com/markhuangai/dense-mem/internal/community/contract"
	"github.com/markhuangai/dense-mem/internal/domain"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	recallapp "github.com/markhuangai/dense-mem/internal/recall"
	"github.com/markhuangai/dense-mem/internal/settings"
	settingspg "github.com/markhuangai/dense-mem/internal/settings/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func pauseCommunityQuery(t *testing.T, db *gorm.DB, fragment string) (<-chan struct{}, func()) {
	t.Helper()
	selected, resume := make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	var release sync.Once
	name := "topic-race-" + uuid.NewString()
	require.NoError(t, db.Callback().Row().After("gorm:row").Register(name, func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), fragment) && once.CompareAndSwap(false, true) {
			close(selected)
			<-resume
		}
	}))
	finish := func() { release.Do(func() { close(resume) }) }
	t.Cleanup(func() { finish(); _ = db.Callback().Row().Remove(name) })
	return selected, finish
}

func TestCommunityTopicRecallUsesOneWithdrawalSnapshot(t *testing.T) {
	f := newTopicProjectionFixture(t)
	f.seedCohort(t)
	f.drain(t, 10)
	selected, resume := pauseCommunityQuery(t, f.appDB, "WITH params AS")
	type response struct {
		records []CommunityRecallRecord
		err     error
	}
	completed := make(chan response, 1)
	input := CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 10, RelationshipLimit: 20}
	go func() {
		records, _, err := f.store.RecallCommunities(f.actor(f.reader), input)
		completed <- response{records, err}
	}()
	select {
	case <-selected:
	case <-time.After(5 * time.Second):
		t.Fatal("Recall did not reach topic selection")
	}
	f.retract(t, 0)
	resume()
	result := <-completed
	require.NoError(t, result.err)
	require.Len(t, result.records, 3)
	for _, record := range result.records {
		if record.LogicalCommunityID == f.topics["runtime"] {
			require.Equal(t, 9, record.RelationshipCount)
			require.Len(t, record.Relationships, 9)
			found := false
			for _, relationship := range record.Relationships {
				if relationship.RelationshipID == f.sources[0].RelationshipID {
					found = true
					require.Contains(t, relationship.EvidenceIDs, f.evidenceIDs[0])
				}
			}
			require.True(t, found, "hydration mixed a later withdrawal into the earlier validated snapshot")
		}
	}
	current, _, err := f.store.RecallCommunities(f.actor(f.reader), input)
	require.NoError(t, err)
	require.Len(t, current, 2)
}

func TestCommunityTopicRecallModeSwitchRejectsUnvalidatedLegacy(t *testing.T) {
	f := newTopicProjectionFixture(t)
	f.seedCohort(t)
	f.drain(t, 10)
	f.retract(t, 4)
	service := f.recallService(t)
	selected, resume := pauseCommunityQuery(t, f.appDB, "SELECT key, value, updated_at")
	type response struct {
		result *recallapp.RecallResult
		err    error
	}
	completed := make(chan response, 1)
	zero, ten, twenty := 0, 10, 20
	go func() {
		result, err := service.Recall(f.actor(f.reader), recallapp.RecallRequest{Query: "marker", Limit: 1,
			RelationshipLimit: &zero, CommunityLimit: &ten, CommunityRelationshipLimit: &twenty})
		completed <- response{result, err}
	}()
	select {
	case <-selected:
	case <-time.After(5 * time.Second):
		t.Fatal("Recall did not reach the configuration snapshot")
	}
	f.setMode(t, false)
	resume()
	result := <-completed
	require.NoError(t, result.err)
	require.Empty(t, result.result.RelatedCommunities, "mode switch exposed stale legacy communities without legacy validation")
	for _, degradation := range result.result.Degradations {
		require.NotEqual(t, "communities", degradation.Frontier, "disabled ontology is an intentional empty community result")
	}
}

func TestCommunityTopicRecallModePreconditionUsesSelectionSnapshot(t *testing.T) {
	f := newTopicProjectionFixture(t)
	f.seedCohort(t)
	f.drain(t, 10)
	for _, enabled := range []bool{false, true} {
		f.setMode(t, enabled)
		for _, expected := range []bool{false, true} {
			records, _, err := f.store.RecallCommunities(f.actor(f.reader), CommunityRecallInput{TeamID: f.teamID,
				Query: "marker", Limit: 10, ExpectedOntologyEnabled: &expected})
			if enabled == expected {
				require.NoError(t, err)
				if enabled {
					require.NotEmpty(t, records)
				} else {
					require.Empty(t, records)
				}
			} else {
				require.ErrorIs(t, err, community.ErrCommunitySourceStale)
				require.Empty(t, records)
			}
		}
	}
	selected, resume := pauseCommunityQuery(t, f.appDB, "SELECT "+communityOntologyModeSQL)
	type response struct {
		records []CommunityRecallRecord
		err     error
	}
	completed := make(chan response, 1)
	enabled := true
	input := CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 10, ExpectedOntologyEnabled: &enabled}
	go func() {
		records, _, err := f.store.RecallCommunities(f.actor(f.reader), input)
		completed <- response{records, err}
	}()
	select {
	case <-selected:
	case <-time.After(5 * time.Second):
		t.Fatal("Recall did not reach the mode precondition")
	}
	f.setMode(t, false)
	resume()
	result := <-completed
	require.NoError(t, result.err)
	require.Len(t, result.records, 3)
	for _, record := range result.records {
		require.Contains(t, f.currentIDs(t), record.LogicalCommunityID, "selection mixed a later mode into the validated snapshot")
	}
	_, _, err := f.store.RecallCommunities(f.actor(f.reader), input)
	require.ErrorIs(t, err, community.ErrCommunitySourceStale)
}

func TestCommunityTopicRecallCoverageTracksSelectionSnapshot(t *testing.T) {
	f := newTopicProjectionFixture(t)
	f.seedCohort(t)
	f.drain(t, 10)
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		updated := tx.Exec(`UPDATE ontology_maintenance_teams SET discovery_kind=4 WHERE team_id=?::uuid`, f.teamID)
		if updated.Error != nil {
			return updated.Error
		}
		require.EqualValues(t, 1, updated.RowsAffected)
		if err := tx.Exec(`UPDATE ontology_maintenance_sources SET status='organized' WHERE team_id=?::uuid AND eligible`, f.teamID).Error; err != nil {
			return err
		}
		return tx.Exec(`DELETE FROM ontology_maintenance_markers WHERE team_id=?::uuid`, f.teamID).Error
	}))
	coverage, err := f.store.TopicProjectionCoverage(f.actor(f.reader), f.teamID)
	require.NoError(t, err)
	require.True(t, coverage.CoverageComplete)
	service := f.recallService(t)
	selected, resume := pauseCommunityQuery(t, f.appDB, "AND discovery_kind=4)")
	type response struct {
		result *recallapp.RecallResult
		err    error
	}
	completed := make(chan response, 1)
	zero, ten, twenty := 0, 10, 20
	request := recallapp.RecallRequest{Query: "marker", Limit: 1, RelationshipLimit: &zero,
		CommunityLimit: &ten, CommunityRelationshipLimit: &twenty}
	go func() {
		result, err := service.Recall(f.actor(f.reader), request)
		completed <- response{result, err}
	}()
	select {
	case <-selected:
	case <-time.After(5 * time.Second):
		t.Fatal("Recall did not reach complete coverage validation")
	}
	f.retract(t, 0)
	resume()
	result := <-completed
	require.NoError(t, result.err)
	partial := recallapp.RecallDegradationResult{Frontier: "communities", Code: "community_projection_partial", Optional: true,
		Message: "community topic coverage is incomplete; eligible current topics and direct retrieval remain available"}
	if len(result.result.RelatedCommunities) == 3 {
		require.NotContains(t, result.result.Degradations, partial)
	} else {
		require.Len(t, result.result.RelatedCommunities, 2)
		require.Contains(t, result.result.Degradations, partial, "selection omitted a stale topic without partial coverage")
	}
	current, err := service.Recall(f.actor(f.reader), request)
	require.NoError(t, err)
	require.Len(t, current.RelatedCommunities, 2)
	require.Contains(t, current.Degradations, partial)
	for index := 1; index < len(f.sources); index++ {
		f.retract(t, index)
	}
	empty, err := service.Recall(f.actor(f.reader), request)
	require.NoError(t, err)
	require.Empty(t, empty.RelatedCommunities)
	require.Contains(t, empty.Degradations, partial, "empty selection still needs its coverage metadata")
}

func TestCommunityTopicPublicationUsesAdmissionLockOrder(t *testing.T) {
	f := newTopicProjectionFixture(t)
	f.topic(t, "lock-order", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	work, _, err := f.store.ClaimTopicProjection(ctx, time.Now())
	require.NoError(t, err)
	require.NotNil(t, work)
	require.Empty(t, work.Inputs)
	before := f.canonicalSnapshot(t)
	completed := make(chan struct{})
	var publicationErr error
	err = f.rls.WithSystemTx(ctx, f.adminDB, func(tx *gorm.DB) error {
		var paused bool
		if err := tx.Raw(`SELECT paused FROM ontology_maintenance_state WHERE singleton FOR UPDATE`).Row().Scan(&paused); err != nil {
			return err
		}
		go func() {
			defer close(completed)
			publicationErr = f.store.AppendTopicProjection(ctx, community.TopicProjectionBatch{Work: *work}, time.Now())
		}()
		t.Cleanup(func() {
			cancel()
			select {
			case <-completed:
			case <-time.After(5 * time.Second):
				t.Error("projection publication did not stop")
			}
		})
		require.Eventually(t, func() bool {
			var waiting bool
			err := f.adminDB.Raw(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database()
				AND wait_event_type='Lock' AND query='SELECT paused FROM ontology_maintenance_state WHERE singleton FOR UPDATE')`).Row().Scan(&waiting)
			return err == nil && waiting
		}, 5*time.Second, 10*time.Millisecond)
		var topic string
		return tx.Raw(`SELECT topic_id::text FROM community_topic_work WHERE team_id=?::uuid AND topic_id=?::uuid FOR UPDATE NOWAIT`,
			work.TeamID, work.TopicID).Row().Scan(&topic)
	})
	require.NoError(t, err, "publication held topic work while waiting for admission, reversing discovery's lock order")
	select {
	case <-completed:
		require.NoError(t, publicationErr)
	case <-ctx.Done():
		t.Fatal("projection publication did not finish")
	}
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestCommunityTopicCapacityIsSharedWithMaintenance(t *testing.T) {
	f := newTopicProjectionFixture(t, func(f *topicProjectionFixture) {
		config := settings.NewAppConfigService(settingspg.NewAppConfigRepository(f.appDB, f.rls), nil)
		_, err := config.UpdateOntologyMaintenanceSettings(context.Background(), map[string]string{domain.AppConfigOntologyConcurrency: "1"}, "control", "", "")
		require.NoError(t, err)
	})
	f.seedCohort(t)
	_, err := f.ontology.PublishAutomatic(context.Background(), f.otherTeam, ontology.Publication{OperationKey: uuid.NewString(), Reason: "second-instance capacity fixture", Changes: []ontology.Change{{Record: ontology.Record{ID: uuid.NewString(), Kind: ontology.Topic, Definition: &ontology.Definition{Key: "foreign-capacity", Label: "Foreign capacity"}}}}})
	require.NoError(t, err)
	instanceB := NewStore(f.appDB.Session(&gorm.Session{NewDB: true}), f.rls).WithTopics(f.ontology.NewTopicCatalogReader(), f.ontology.NewTopicMembershipReader(), f.ontology.NewTopicProjectionAdmission(), f.ontology.NewTopicProjectionRelease())
	work, _, err := f.store.ClaimTopicProjection(context.Background(), time.Now())
	require.NoError(t, err)
	require.NotNil(t, work)
	other, _, err := instanceB.ClaimTopicProjection(context.Background(), time.Now())
	require.NoError(t, err)
	require.Nil(t, other, "separate projection instance exceeded global concurrency")
	turn, err := f.ontology.ClaimMaintenanceTurn(context.Background(), f.windowID, time.Now(), time.Minute)
	require.NoError(t, err)
	require.Nil(t, turn, "organization overlapped the admitted projection")
	require.NoError(t, f.store.FailTopicProjection(context.Background(), *work, "interrupted", time.Now()))
	turn, err = f.ontology.ClaimMaintenanceTurn(context.Background(), f.windowID, time.Now(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, turn)
	other, _, err = instanceB.ClaimTopicProjection(context.Background(), time.Now())
	require.NoError(t, err)
	require.Nil(t, other, "projection overlapped an admitted organization turn")
	require.NoError(t, f.ontology.ReleaseMaintenanceTurn(context.Background(), *turn))
	require.ErrorContains(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE ontology_maintenance_windows SET overrun=true WHERE window_id=?::uuid`, f.windowID).Error
	}), "ontology window identity, policy and historical accounting are immutable")
	other, _, err = instanceB.ClaimTopicProjection(context.Background(), time.Now())
	require.NoError(t, err)
	require.NotNil(t, other, "rejected spending write blocked independent projection work")
	f.expireLease(t, other.CommunityID)
	recovered, _, err := f.store.ClaimTopicProjection(context.Background(), time.Now())
	require.NoError(t, err)
	require.NotNil(t, recovered)
	require.ErrorIs(t, instanceB.AppendTopicProjection(context.Background(), community.TopicProjectionBatch{Work: *other}, time.Now()), community.ErrCommunityRunAlreadyClaimed)
	require.NoError(t, f.store.FailTopicProjection(context.Background(), *recovered, "interrupted", time.Now()))
}

func TestCommunityTopicBusyTeamDoesNotStarveAvailableTeam(t *testing.T) {
	f := newTopicProjectionFixture(t)
	ctx := context.Background()
	f.topic(t, "available-capacity", []ontology.SourceHandle{{Kind: ontology.RelationshipSource, ID: f.sources[0].RelationshipID, Version: int64(f.sources[0].RelationshipVersion)}})
	_, err := f.ontology.PublishAutomatic(ctx, f.otherTeam, ontology.Publication{OperationKey: uuid.NewString(), Reason: "busy team rotation fixture", Changes: []ontology.Change{{Record: ontology.Record{ID: uuid.NewString(), Kind: ontology.Topic, Definition: &ontology.Definition{Key: "foreign-rotation", Label: "Foreign rotation"}}}}})
	require.NoError(t, err)
	config := settings.NewAppConfigService(settingspg.NewAppConfigRepository(f.appDB, f.rls), nil)
	_, err = config.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyConcurrency: "2"}, "control", "", "")
	require.NoError(t, err)
	policy, err := config.OntologyMaintenanceRuntimeConfig(ctx)
	require.NoError(t, err)
	window, err := f.ontology.EnsureMaintenanceWindow(ctx, policy, time.Now())
	require.NoError(t, err)
	require.NotNil(t, window)
	now := window.EndsAt.Add(time.Minute)
	window, err = f.ontology.EnsureMaintenanceWindow(ctx, policy, now)
	require.NoError(t, err)
	require.NotNil(t, window)
	require.Equal(t, 2, window.Policy.MaxConcurrency)
	f.windowID = window.ID
	turn, err := f.ontology.ClaimMaintenanceTurn(ctx, f.windowID, now, 15*time.Minute)
	require.NoError(t, err)
	require.NotNil(t, turn)
	defer func() { require.NoError(t, f.ontology.ReleaseMaintenanceTurn(ctx, *turn)) }()
	require.NoError(t, f.store.withProjectionTx(ctx, func(tx *gorm.DB) error {
		if _, err := f.store.discoverTopicProjections(ctx, tx); err != nil {
			return err
		}
		return tx.Exec(`UPDATE community_topic_work SET last_turn=CASE WHEN team_id=?::uuid THEN ?::timestamptz ELSE ?::timestamptz END`, turn.TeamID, now.Add(-2*time.Minute), now.Add(-time.Minute)).Error
	}))
	work, _, err := f.store.ClaimTopicProjection(ctx, now)
	require.NoError(t, err)
	require.Nil(t, work, "busy team must not receive a second lease")
	work, _, err = f.store.ClaimTopicProjection(ctx, now.Add(time.Second))
	require.NoError(t, err)
	require.NotNil(t, work, "refused candidate starved another team with available capacity")
	require.NotEqual(t, turn.TeamID, work.TeamID)
	require.NoError(t, f.store.FailTopicProjection(ctx, *work, "interrupted", now.Add(time.Second)))
}

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

func TestCommunityTopicModeSwitchOrdersLegacyPublication(t *testing.T) {
	f := newTopicProjectionFixture(t)
	f.setMode(t, false)
	run, err := f.store.ClaimCommunityRun(context.Background(), CommunityRunClaimInput{TeamID: f.teamID, WindowKey: uuid.NewString(), SourceFingerprint: "fixture-sources"})
	require.NoError(t, err)
	input := f.publication
	input.RunID = run.RunID
	input.Communities = append([]CommunityPublishRecord(nil), input.Communities...)
	for i := range input.Communities {
		input.Communities[i].CommunityID = uuid.NewString()
	}
	selected, resume := pauseCommunityQuery(t, f.appDB, "SELECT value='true' FROM app_config WHERE key='ONTOLOGY_MAINTENANCE_ENABLED' FOR SHARE")
	published := make(chan error, 1)
	go func() { published <- f.store.PublishCommunitySnapshot(context.Background(), input) }()
	select {
	case <-selected:
	case <-time.After(5 * time.Second):
		t.Fatal("legacy publisher did not reach the mode fence")
	}
	enabled := make(chan error, 1)
	go func() {
		enabled <- f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
			return tx.Exec(`UPDATE app_config SET value='true' WHERE key='ONTOLOGY_MAINTENANCE_ENABLED'`).Error
		})
	}()
	require.Eventually(t, func() bool {
		var blocked bool
		err := f.adminDB.Raw(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE query LIKE '%UPDATE app_config SET value%' AND state='active' AND wait_event_type='Lock')`).Row().Scan(&blocked)
		return err == nil && blocked
	}, 5*time.Second, 20*time.Millisecond)
	resume()
	require.NoError(t, <-published)
	require.NoError(t, <-enabled)
	require.ErrorIs(t, f.store.PublishCommunitySnapshot(context.Background(), input), ontology.ErrMaintenanceDisabled)
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
		records, err := f.store.RecallCommunities(f.actor(f.reader), input)
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
	current, err := f.store.RecallCommunities(f.actor(f.reader), input)
	require.NoError(t, err)
	require.Len(t, current, 2)
}

func TestCommunityTopicCapacityIsSharedWithMaintenance(t *testing.T) {
	f := newTopicProjectionFixture(t)
	f.seedCohort(t)
	_, err := f.ontology.PublishAutomatic(context.Background(), f.otherTeam, ontology.Publication{OperationKey: uuid.NewString(), Reason: "second-instance capacity fixture", Changes: []ontology.Change{{Record: ontology.Record{ID: uuid.NewString(), Kind: ontology.Topic, Definition: &ontology.Definition{Key: "foreign-capacity", Label: "Foreign capacity"}}}}})
	require.NoError(t, err)
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE ontology_maintenance_windows SET policy=jsonb_set(policy,'{max_concurrency}','1') WHERE window_id=?::uuid`, f.windowID).Error
	}))
	instanceB := NewStore(f.appDB.Session(&gorm.Session{NewDB: true}), f.rls).WithTopics(f.ontology.NewTopicCatalogReader(), f.ontology.NewTopicMembershipReader(), f.ontology.NewTopicProjectionAdmission(), f.ontology.NewTopicProjectionRelease())
	work, err := f.store.ClaimTopicProjection(context.Background(), time.Now())
	require.NoError(t, err)
	require.NotNil(t, work)
	other, err := instanceB.ClaimTopicProjection(context.Background(), time.Now())
	require.NoError(t, err)
	require.Nil(t, other, "separate projection instance exceeded global concurrency")
	turn, err := f.ontology.ClaimMaintenanceTurn(context.Background(), f.windowID, time.Now(), time.Minute)
	require.NoError(t, err)
	require.Nil(t, turn, "organization overlapped the admitted projection")
	require.NoError(t, f.store.FailTopicProjection(context.Background(), *work, "interrupted", time.Now()))
	turn, err = f.ontology.ClaimMaintenanceTurn(context.Background(), f.windowID, time.Now(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, turn)
	other, err = instanceB.ClaimTopicProjection(context.Background(), time.Now())
	require.NoError(t, err)
	require.Nil(t, other, "projection overlapped an admitted organization turn")
	require.NoError(t, f.ontology.ReleaseMaintenanceTurn(context.Background(), *turn))
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE ontology_maintenance_windows SET overrun=true WHERE window_id=?::uuid`, f.windowID).Error
	}))
	other, err = instanceB.ClaimTopicProjection(context.Background(), time.Now())
	require.NoError(t, err)
	require.NotNil(t, other, "model budget overrun blocked independent projection work")
	f.expireLease(t, other.CommunityID)
	recovered, err := f.store.ClaimTopicProjection(context.Background(), time.Now())
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
		if err := f.store.discoverTopicProjections(ctx, tx); err != nil {
			return err
		}
		return tx.Exec(`UPDATE community_topic_work SET last_turn=CASE WHEN team_id=?::uuid THEN ?::timestamptz ELSE ?::timestamptz END`, turn.TeamID, now.Add(-2*time.Minute), now.Add(-time.Minute)).Error
	}))
	work, err := f.store.ClaimTopicProjection(ctx, now)
	require.NoError(t, err)
	require.Nil(t, work, "busy team must not receive a second lease")
	work, err = f.store.ClaimTopicProjection(ctx, now.Add(time.Second))
	require.NoError(t, err)
	require.NotNil(t, work, "refused candidate starved another team with available capacity")
	require.NotEqual(t, turn.TeamID, work.TeamID)
	require.NoError(t, f.store.FailTopicProjection(ctx, *work, "interrupted", now.Add(time.Second)))
}

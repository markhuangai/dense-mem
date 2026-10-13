//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	communityapp "github.com/markhuangai/dense-mem/internal/community/service"
	"github.com/markhuangai/dense-mem/internal/settings"
	settingspg "github.com/markhuangai/dense-mem/internal/settings/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCommunityTopicStatusOmitsHistoricalRunOnlyWhileEnabled(t *testing.T) {
	f := newTopicProjectionFixture(t)
	f.seedCohort(t)
	f.drain(t, 10)
	historical := uuid.NewString()
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`INSERT INTO community_snapshot_runs(team_id,space_id,space_generation,run_id,window_key,algorithm_kind,status,error,started_at)
		 VALUES(?::uuid,?::uuid,?,?::uuid,?,'louvain','failed','historical-error-content-canary',now()+interval '1 day')`, f.teamID, f.space, f.generation, historical, "historical-"+historical).Error
	}))
	config := settings.NewAppConfigService(settingspg.NewAppConfigRepository(f.appDB, f.rls), nil)
	api := communityapp.New(communityapp.Dependencies{Store: f.store, AppConfig: config})
	for _, enabled := range []bool{true, false, true} {
		f.setMode(t, enabled)
		status, err := api.Status(f.actor(f.reader), f.teamID)
		require.NoError(t, err)
		require.True(t, status.LegacyProcessingRetired)
		if enabled {
			require.Nil(t, status.LatestRun)
			require.NotNil(t, status.ProjectionCoverage)
			require.Equal(t, 3, status.CurrentCommunityCount)
		} else {
			require.Nil(t, status.ProjectionCoverage)
			require.NotNil(t, status.LatestRun)
			require.Equal(t, historical, status.LatestRun.RunID)
			require.Equal(t, "failed", status.LatestRun.Status)
			require.Equal(t, "community run failed", status.LatestRun.Error)
		}
	}
}

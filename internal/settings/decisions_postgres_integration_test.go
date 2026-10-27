//go:build integration

package settings_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	auditapp "github.com/markhuangai/dense-mem/internal/audit"
	auditpostgres "github.com/markhuangai/dense-mem/internal/audit/postgres"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	densehttp "github.com/markhuangai/dense-mem/internal/http"
	"github.com/markhuangai/dense-mem/internal/settings"
	settingspostgres "github.com/markhuangai/dense-mem/internal/settings/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func decisionServiceForPostgres(db *gorm.DB, rls *storagepostgres.RLS, provider string) *settings.AppConfigServiceImpl {
	return settings.NewAppConfigServiceWithDecisions(settingspostgres.NewAppConfigRepository(db, rls), auditapp.New(auditpostgres.NewStore(db, rls)), settings.DecisionSettingsOptions{Provider: domain.DecisionProviderStatus{Configured: provider != "disabled", Provider: provider, Model: map[string]string{"openai": "gpt-6-luna", "typesafe": "jev-1.14.0"}[provider]}})
}

func preserveDecisionConfig(t *testing.T, db *gorm.DB, rls *storagepostgres.RLS) func() {
	t.Helper()
	ctx := context.Background()
	keys := []string{domain.AppConfigDecisionDeduplication, domain.AppConfigDecisionSecurity, domain.AppConfigDecisionConflict, domain.AppConfigDecisionReranking, domain.AppConfigUpdateTimeKey}
	original, err := settingspostgres.NewAppConfigRepository(db, rls).List(ctx)
	require.NoError(t, err)
	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
			for _, key := range keys {
				entry, exists := original[key]
				if exists {
					if err := tx.Exec(`INSERT INTO app_config(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=EXCLUDED.updated_at`, key, entry.Value, entry.UpdatedAt).Error; err != nil {
						return err
					}
				} else {
					if err := tx.Exec(`DELETE FROM app_config WHERE key=?`, key).Error; err != nil {
						return err
					}
				}
			}
			return nil
		}))
	}
	t.Cleanup(restore)
	return restore
}

func TestDecisionSettingsPostgresPersistenceRefreshAndRollback(t *testing.T) {
	db, rls, cleanup := openSettingsPostgres(t)
	defer cleanup()
	restore := preserveDecisionConfig(t, db, rls)
	defer restore()
	ctx := context.Background()
	writer, reader := decisionServiceForPostgres(db, rls, "openai"), decisionServiceForPostgres(db, rls, "openai")
	before, err := reader.DecisionRuntimeConfig(ctx)
	require.NoError(t, err)
	_, err = writer.UpdateDecisionSettings(ctx, map[string]string{domain.AppConfigDecisionDeduplication: "false"}, "control", "", "decision-postgres")
	require.NoError(t, err)
	cached, err := reader.DecisionRuntimeConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, before, cached, "other instance retains one cached revision until refresh")
	time.Sleep(settings.DefaultAppConfigCacheCheckInterval + 100*time.Millisecond)
	refreshed, err := reader.DecisionRuntimeConfig(ctx)
	require.NoError(t, err)
	require.False(t, refreshed.Effective.Deduplication)
	require.NotEqual(t, before.Revision, refreshed.Revision)
	restarted, err := decisionServiceForPostgres(db, rls, "typesafe").DecisionRuntimeConfig(ctx)
	require.NoError(t, err)
	require.False(t, restarted.Effective.Deduplication)
	require.True(t, restarted.Overrides.Deduplication)
	entries, err := settingspostgres.NewAppConfigRepository(db, rls).List(ctx)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`ALTER TABLE app_config ADD CONSTRAINT decision_fixture_rollback CHECK(key <> 'DECISION_SECURITY_REVIEW_ENABLED' OR value <> 'false')`).Error)
	constraintActive := true
	defer func() {
		if constraintActive {
			require.NoError(t, db.Exec(`ALTER TABLE app_config DROP CONSTRAINT IF EXISTS decision_fixture_rollback`).Error)
		}
	}()
	_, err = writer.UpdateDecisionSettings(ctx, map[string]string{domain.AppConfigDecisionConflict: "false", domain.AppConfigDecisionSecurity: "false"}, "control", "", "decision-rollback")
	require.Error(t, err)
	require.NoError(t, db.Exec(`ALTER TABLE app_config DROP CONSTRAINT IF EXISTS decision_fixture_rollback`).Error)
	constraintActive = false
	rolledBack, err := settingspostgres.NewAppConfigRepository(db, rls).List(ctx)
	require.NoError(t, err)
	for _, key := range []string{domain.AppConfigDecisionConflict, domain.AppConfigDecisionSecurity, domain.AppConfigUpdateTimeKey} {
		require.Equal(t, entries[key], rolledBack[key], "all supplied settings and revision roll back together")
	}
	var audits int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM audit_log WHERE operation='APP_CONFIG_UPDATE' AND entity_id='decisions' AND correlation_id='decision-postgres'`).Scan(&audits).Error)
	require.Equal(t, int64(1), audits)
	restore()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	time.Sleep(settings.DefaultAppConfigCacheCheckInterval + 100*time.Millisecond)
	stale, err := reader.DecisionRuntimeConfig(ctx)
	require.NoError(t, err)
	require.True(t, stale.Stale)
	require.False(t, stale.Effective.Deduplication)
	require.Equal(t, refreshed.Revision, stale.Revision)
	_, err = decisionServiceForPostgres(db, rls, "openai").DecisionRuntimeConfig(ctx)
	require.Error(t, err, "no valid snapshot cannot become enabled defaults")
}

func TestDecisionSettingsControlAuthorizationAndStrictPatch(t *testing.T) {
	db, rls, cleanup := openSettingsPostgres(t)
	defer cleanup()
	restore := preserveDecisionConfig(t, db, rls)
	defer restore()
	service := decisionServiceForPostgres(db, rls, "openai")
	e, err := densehttp.NewControlPortalServerWithCapabilityBindings(&config.Config{ControlPortalToken: "decision-settings-control"}, nil, nil, nil, densehttp.ControlPortalBindings{Telemetry: densehttp.ControlPortalTelemetry{Config: service, Decisions: service}}, densehttp.HealthConfig{}, nil, nil)
	require.NoError(t, err)
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		denied := controlRequest(e, method, "/control/api/config/decisions", "wrong-token", `{"items":[{"key":"DECISION_SECURITY_REVIEW_ENABLED","value":"false"}]}`)
		require.Equal(t, http.StatusUnauthorized, denied.Code)
	}
	for _, body := range []string{
		`{"items":[{"key":"DECISION_SECURITY_REVIEW_ENABLED","value":"false"},{"key":"DECISION_SECURITY_REVIEW_ENABLED","value":"true"}]}`,
		`{"items":[{"key":"DECISION_SECURITY_REVIEW_ENABLED","value":"false","value":"true"}]}`,
		`{"items":[{"key":"DECISION_SECURITY_REVIEW_ENABLED","value":"false","unknown":true}]}`,
		`{"items":[{"key":"AI_DECISION_API_KEY","value":"forged"}]}`,
		`{"items":[{"key":"DECISION_SECURITY_REVIEW_ENABLED","value":null}]}`,
		`{"items":[{"key":"DECISION_SECURITY_REVIEW_ENABLED","value":false}]}`,
		`{"items":[{"key":"DECISION_SECURITY_REVIEW_ENABLED","value":"1"}]}`,
		`{"items":[]}`, `{"items":null}`, `{}`, `{"items":[]} {}`,
	} {
		rejected := controlRequest(e, http.MethodPatch, "/control/api/config/decisions", "decision-settings-control", body)
		require.Equal(t, http.StatusUnprocessableEntity, rejected.Code, body)
	}
	updated := controlRequest(e, http.MethodPatch, "/control/api/config/decisions", "decision-settings-control", `{"items":[{"key":"DECISION_SECURITY_REVIEW_ENABLED","value":"false"}]}`)
	require.Equal(t, http.StatusOK, updated.Code, updated.Body.String())
	var envelope struct {
		Data domain.DecisionConfigSettings `json:"data"`
	}
	require.NoError(t, json.Unmarshal(updated.Body.Bytes(), &envelope))
	require.False(t, envelope.Data.Effective.Effective.SecurityReview)
	require.True(t, envelope.Data.Effective.Effective.Deduplication)
	for _, item := range envelope.Data.Items {
		if item.Key == domain.AppConfigDecisionSecurity {
			require.Equal(t, "override", item.Source)
		} else {
			require.Equal(t, "default", item.Source)
		}
	}
	require.NotContains(t, updated.Body.String(), "API_KEY")
	require.NotContains(t, updated.Body.String(), "decision-settings-control")
	read := controlRequest(e, http.MethodGet, "/control/api/config/decisions", "decision-settings-control", "")
	require.Equal(t, http.StatusOK, read.Code)
}

func TestDecisionSettingsRefreshWaitHonorsCancellation(t *testing.T) {
	db, rls, cleanup := openSettingsPostgres(t)
	defer cleanup()
	svc := decisionServiceForPostgres(db, rls, "openai")
	before, err := svc.DecisionRuntimeConfig(context.Background())
	require.NoError(t, err)
	lock := db.Begin()
	require.NoError(t, lock.Error)
	lockActive := true
	defer func() {
		if lockActive {
			require.NoError(t, lock.Rollback().Error)
		}
	}()
	require.NoError(t, lock.Exec(`LOCK TABLE app_config IN ACCESS EXCLUSIVE MODE`).Error)
	freshCtx, cancelFresh := context.WithTimeout(context.Background(), time.Second)
	defer cancelFresh()
	fresh, err := svc.DecisionRuntimeConfig(freshCtx)
	require.NoError(t, err, "fresh snapshots must not read the locked settings table")
	require.Equal(t, before, fresh)
	time.Sleep(settings.DefaultAppConfigCacheCheckInterval + 100*time.Millisecond)
	refreshCtx, cancelRefresh := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelRefresh()
	refreshed := make(chan error, 1)
	go func() {
		_, err := svc.DecisionRuntimeConfig(refreshCtx)
		refreshed <- err
	}()
	require.Eventually(t, func() bool {
		var blocked int64
		err := db.Raw(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query ILIKE '%app_config%'`).Scan(&blocked).Error
		return err == nil && blocked > 0
	}, 5*time.Second, 10*time.Millisecond, "the first refresh must be blocked in a real PostgreSQL read")
	readerCtx, cancelReader := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelReader()
	canceled := make(chan error, 1)
	go func() {
		_, err := svc.DecisionRuntimeConfig(readerCtx)
		canceled <- err
	}()
	select {
	case err := <-canceled:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("canceled settings reader remained blocked behind another refresh")
	}
	require.NoError(t, lock.Rollback().Error)
	lockActive = false
	require.NoError(t, <-refreshed)
	after, err := svc.DecisionRuntimeConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, before.Preferences, after.Preferences)
	require.Equal(t, before.Revision, after.Revision)
	require.False(t, after.Stale)
}

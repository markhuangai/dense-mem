package settings

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/stretchr/testify/require"
)

func TestDecisionPreferencesDefaultAndPartialUpdates(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	repo := newAppConfigRepoStub(now, nil)
	svc := NewAppConfigServiceWithDecisions(repo, nil, DecisionSettingsOptions{Provider: domain.DecisionProviderStatus{Configured: true, Provider: "openai", Model: "gpt-6-luna"}})
	svc.now = func() time.Time { return now }
	initial, err := svc.GetDecisionSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, domain.DecisionTasks{Deduplication: true, SecurityReview: true, ConflictReview: true, RecallReranking: true}, initial.Effective.Effective)
	for _, item := range initial.Items {
		require.Equal(t, "default", item.Source)
		require.Empty(t, item.Value)
	}
	now = now.Add(time.Second)
	updated, err := svc.UpdateDecisionSettings(ctx, map[string]string{domain.AppConfigDecisionSecurity: "false"}, "control", "", "correlation")
	require.NoError(t, err)
	require.False(t, updated.Effective.Effective.SecurityReview)
	require.True(t, updated.Effective.Effective.Deduplication)
	require.True(t, initial.Effective.Effective.SecurityReview, "running operations retain their snapshot")
	_, exists := repo.entries[domain.AppConfigDecisionDeduplication]
	require.False(t, exists, "save must not persist unsupplied defaults")
	restarted := NewAppConfigServiceWithDecisions(repo, nil, DecisionSettingsOptions{Provider: domain.DecisionProviderStatus{Configured: true, Provider: "typesafe", Model: "jev-1.14.0"}})
	snapshot, err := restarted.DecisionRuntimeConfig(ctx)
	require.NoError(t, err)
	require.False(t, snapshot.Effective.SecurityReview)
	require.True(t, snapshot.Overrides.SecurityReview)
	disabled, err := NewAppConfigService(repo, nil).DecisionRuntimeConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, domain.DecisionTasks{}, disabled.Effective)
	require.True(t, disabled.Preferences.Deduplication)
	require.False(t, disabled.Preferences.SecurityReview)
}

func TestDecisionSettingsRetainStaleOverridesAndLogCause(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	var logs bytes.Buffer
	repo := newAppConfigRepoStub(now, map[string]string{domain.AppConfigDecisionDeduplication: "false"})
	logger := observability.NewWithHandler(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc := NewAppConfigServiceWithDecisions(repo, nil, DecisionSettingsOptions{Provider: domain.DecisionProviderStatus{Configured: true}, Logger: logger})
	svc.now = func() time.Time { return now }
	valid, err := svc.DecisionRuntimeConfig(ctx)
	require.NoError(t, err)
	repo.getUpdateErr = errors.New("settings database unavailable")
	now = now.Add(6 * time.Second)
	stale, err := svc.DecisionRuntimeConfig(ctx)
	require.NoError(t, err)
	require.True(t, stale.Stale)
	require.False(t, stale.Effective.Deduplication)
	require.Equal(t, valid.Revision, stale.Revision)
	require.Equal(t, valid.LastVerifiedAt, stale.LastVerifiedAt)
	require.Contains(t, logs.String(), "decision.settings.stale")
	require.Contains(t, logs.String(), "settings database unavailable")
	_, err = svc.UpdateDecisionSettings(ctx, map[string]string{domain.AppConfigDecisionSecurity: "false"}, "control", "", "")
	require.ErrorContains(t, err, "stale")
	fresh := NewAppConfigServiceWithDecisions(repo, nil, DecisionSettingsOptions{Provider: domain.DecisionProviderStatus{Configured: true}, Logger: logger})
	_, err = fresh.DecisionRuntimeConfig(ctx)
	require.Error(t, err)
	require.Contains(t, logs.String(), "no_valid_snapshot")
	repo.getUpdateErr = nil
	now = now.Add(6 * time.Second)
	recovered, err := svc.DecisionRuntimeConfig(ctx)
	require.NoError(t, err)
	require.False(t, recovered.Stale)
	require.False(t, recovered.Effective.Deduplication)
	repo.entries[domain.AppConfigDecisionDeduplication] = domain.AppConfigEntry{Key: domain.AppConfigDecisionDeduplication, Value: "invalid"}
	repo.entries[domain.AppConfigUpdateTimeKey] = domain.AppConfigEntry{Value: "invalid-revision"}
	now = now.Add(6 * time.Second)
	retained, err := svc.DecisionRuntimeConfig(ctx)
	require.NoError(t, err)
	require.True(t, retained.Stale)
	require.Equal(t, recovered.Revision, retained.Revision)
	require.False(t, retained.Preferences.Deduplication)
}

func TestDecisionSettingsRejectNonBooleanAndUnknownValues(t *testing.T) {
	for _, values := range []map[string]string{nil, {"AI_DECISION_WORKLOADS": "all"}, {"update_time": "forged"}, {domain.AppConfigDecisionSecurity: ""}, {domain.AppConfigDecisionSecurity: "1"}, {domain.AppConfigDecisionSecurity: "TRUE"}, {domain.AppConfigDecisionSecurity: " false "}} {
		_, err := NewAppConfigService(newAppConfigRepoStub(time.Now(), nil), nil).UpdateDecisionSettings(context.Background(), values, "control", "", "")
		require.ErrorIs(t, err, ErrInvalidAppConfig)
	}
}

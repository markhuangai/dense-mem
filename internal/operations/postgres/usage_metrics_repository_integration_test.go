//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestUsageMetricsSnapshotLabelsSSOOwnershipAlias(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "usage-metrics-sso-label"))
	apiKeyID := uuid.MustParse(createLedgerProfile(t, adminDB, rls, teamID.String(), "API Usage Key"))
	ssoProfileID := uuid.New()
	identityID := uuid.New()
	firstCredentialID, secondCredentialID := uuid.New(), uuid.New()
	bucketStart := time.Now().UTC().Truncate(time.Hour)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO actor_identities (
				id, kind, team_id, provider, subject, display_name, active
			) VALUES (?, 'human', NULL, 'usage-metrics-provider', ?, 'Current IdP Name', true)
		`, identityID, "usage-metrics-subject-"+identityID.String()).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO team_memberships (
				actor_identity_id, team_id, status, maximum_grants, sso_profile_name
			) VALUES (?, ?, 'active', ARRAY['read']::text[], 'SSO Usage User')
		`, identityID, teamID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO ownership_aliases (
				team_id, legacy_owner_id, canonical_identity_id, credential_id, reason
			) VALUES (?, ?, ?, NULL, 'sso')
		`, teamID, ssoProfileID, identityID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO credentials (id, actor_identity_id, owner_identity_id, team_id, kind, name, scopes, status)
			VALUES
				(?, ?, ?, ?, 'session', 'SSO Usage A', ARRAY['read']::text[], 'active'),
				(?, ?, ?, ?, 'session', 'SSO Usage B', ARRAY['read']::text[], 'active')
		`, firstCredentialID, identityID, identityID, teamID, secondCredentialID, identityID, identityID, teamID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO usage_metric_buckets (
				bucket_start, team_id, key_id, route, method, status_class, request_count
			) VALUES (?, ?, ?, '/api', 'GET', 2, 3)
		`, bucketStart, teamID, apiKeyID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO usage_metric_buckets (
				bucket_start, team_id, key_id, route, method, status_class, request_count
			) VALUES (?, ?, ?, '/sso', 'GET', 2, 6)
		`, bucketStart, teamID, ssoProfileID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO usage_credential_buckets (
				bucket_start, team_id, credential_id, route, method, status_class, request_count
			) VALUES
				(?, ?, ?, '/sso', 'GET', 2, 3),
				(?, ?, ?, '/sso', 'GET', 2, 3)
		`, bucketStart, teamID, firstCredentialID, bucketStart, teamID, secondCredentialID).Error
	}))

	repo := NewUsageMetricsRepository(appDB, rls)
	snapshot, err := repo.Snapshot(ctx, domain.UsageMetricsFilter{
		From:   bucketStart.Add(-time.Hour),
		To:     bucketStart.Add(time.Hour),
		TeamID: &teamID,
	})
	require.NoError(t, err)
	require.Len(t, snapshot.Keys, 2)
	require.EqualValues(t, 9, snapshot.System.Requests)
	var firstFound, secondFound *domain.UsageKeyMetric
	for index := range snapshot.Keys {
		switch snapshot.Keys[index].KeyID {
		case firstCredentialID:
			firstFound = &snapshot.Keys[index]
		case secondCredentialID:
			secondFound = &snapshot.Keys[index]
		}
	}
	require.NotNil(t, firstFound)
	require.Equal(t, "SSO Usage A", firstFound.KeyName)
	require.NotNil(t, secondFound)
	require.Equal(t, "SSO Usage B", secondFound.KeyName)
	require.Equal(t, "", secondFound.KeySuffix)
	require.EqualValues(t, 3, secondFound.Requests)
	require.NotNil(t, secondFound.MCPToolCalls)
	require.Zero(t, *secondFound.MCPToolCalls)
	require.NotNil(t, secondFound.MCPToolFailures)
	require.Zero(t, *secondFound.MCPToolFailures)
	filtered, err := repo.Snapshot(ctx, domain.UsageMetricsFilter{
		From: bucketStart.Add(-time.Hour), To: bucketStart.Add(time.Hour), TeamID: &teamID, CredentialID: &firstCredentialID,
	})
	require.NoError(t, err)
	require.EqualValues(t, 3, filtered.System.Requests)
	require.Len(t, filtered.Keys, 1)
	require.Equal(t, firstCredentialID, filtered.Keys[0].KeyID)
	otherTeam := uuid.New()
	wrongTeam, err := repo.Snapshot(ctx, domain.UsageMetricsFilter{
		From: bucketStart.Add(-time.Hour), To: bucketStart.Add(time.Hour), TeamID: &otherTeam, CredentialID: &firstCredentialID,
	})
	require.NoError(t, err)
	require.Zero(t, wrongTeam.System.Requests)
	require.Empty(t, wrongTeam.Keys)
}

func TestUsageCredentialBucketsEnforceTeamRLS(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamA := createLedgerTeam(t, adminDB, rls, "usage-credential-rls-a")
	teamB := createLedgerTeam(t, adminDB, rls, "usage-credential-rls-b")
	bucketStart := time.Now().UTC().Truncate(time.Minute)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO usage_credential_buckets (
				bucket_start, team_id, credential_id, route, method, status_class, request_count
			) VALUES
				(?, ?, ?, '/mcp', 'POST', 2, 2),
				(?, ?, ?, '/mcp', 'POST', 2, 3)
		`, bucketStart, teamA, uuid.New(), bucketStart, teamB, uuid.New()).Error
	}))

	for _, test := range []struct {
		teamID string
		count  int64
	}{
		{teamID: teamA, count: 2},
		{teamID: teamB, count: 3},
	} {
		require.NoError(t, rls.WithTeamTx(ctx, appDB, test.teamID, func(tx *gorm.DB) error {
			var count int64
			if err := tx.Raw("SELECT COALESCE(SUM(request_count), 0) FROM usage_credential_buckets").Scan(&count).Error; err != nil {
				return err
			}
			require.Equal(t, test.count, count)
			return nil
		}))
	}

	require.Error(t, rls.WithTeamTx(ctx, appDB, teamA, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO usage_credential_buckets (
				bucket_start, team_id, credential_id, route, method, status_class, request_count
			) VALUES (?, ?, ?, '/mcp', 'POST', 2, 1)
		`, bucketStart, teamB, uuid.New()).Error
	}))
}

func TestUsageMetricsPruneKeepsThirtyDayBoundaryInBothBucketTables(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "usage-metrics-prune")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "Usage Prune Owner")
	credentialID := uuid.New()
	cutoff := time.Now().UTC().AddDate(0, 0, -30).Truncate(time.Minute)
	starts := []time.Time{cutoff.Add(-time.Minute), cutoff, cutoff.Add(time.Minute)}
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		for _, start := range starts {
			if err := tx.Exec(`
				INSERT INTO usage_metric_buckets (bucket_start, team_id, key_id, route, method, status_class, request_count)
				VALUES (?, ?, ?, '/mcp', 'POST', 2, 1)
			`, start, teamID, ownerID).Error; err != nil {
				return err
			}
			if err := tx.Exec(`
				INSERT INTO usage_credential_buckets (bucket_start, team_id, credential_id, route, method, status_class, request_count)
				VALUES (?, ?, ?, '/mcp', 'POST', 2, 1)
			`, start, teamID, credentialID).Error; err != nil {
				return err
			}
		}
		return nil
	}))

	require.NoError(t, NewUsageMetricsRepository(appDB, rls).PruneBefore(ctx, cutoff))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		for _, table := range []string{"usage_metric_buckets", "usage_credential_buckets"} {
			var retained, stale int64
			if err := tx.Table(table).Where("team_id = ? AND bucket_start >= ?", teamID, cutoff).Count(&retained).Error; err != nil {
				return err
			}
			if err := tx.Table(table).Where("team_id = ? AND bucket_start < ?", teamID, cutoff).Count(&stale).Error; err != nil {
				return err
			}
			require.EqualValues(t, 2, retained, table)
			require.Zero(t, stale, table)
		}
		return nil
	}))
}

func TestUsageMetricsFlushRetryIsIdempotentAndAtomic(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "usage-metrics-flush-retry"))
	keyID := uuid.MustParse(createLedgerProfile(t, adminDB, rls, teamID.String(), "Flush Retry Key"))
	bucketStart := time.Now().UTC().Truncate(time.Minute)
	bucket := domain.UsageMetricBucket{
		BucketStart: bucketStart, TeamID: teamID, KeyID: keyID, CredentialID: uuid.New(),
		Route: "/mcp", Method: "POST", StatusClass: 2,
		RequestCount: 2, ErrorCount: 1, MCPToolCalls: 3, MCPToolFailures: 1, TotalLatencyMS: 30, MaxLatencyMS: 20,
		LastSeenAt: bucketStart,
	}
	repo := NewUsageMetricsRepository(appDB, rls)
	flushID := uuid.New()
	require.NoError(t, repo.UpsertBuckets(ctx, flushID, []domain.UsageMetricBucket{bucket}))
	require.NoError(t, repo.UpsertBuckets(ctx, flushID, []domain.UsageMetricBucket{bucket}))

	invalid := bucket
	invalid.StatusClass = 0
	require.Error(t, repo.UpsertBuckets(ctx, uuid.New(), []domain.UsageMetricBucket{bucket, invalid}))

	snapshot, err := repo.Snapshot(ctx, domain.UsageMetricsFilter{
		From: bucketStart.Add(-time.Minute), To: bucketStart.Add(time.Minute), TeamID: &teamID,
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, snapshot.System.Requests)
	require.EqualValues(t, 1, snapshot.System.Errors)
	require.NotNil(t, snapshot.System.MCPToolCalls)
	require.EqualValues(t, 3, *snapshot.System.MCPToolCalls)
	require.NotNil(t, snapshot.System.MCPToolFailures)
	require.EqualValues(t, 1, *snapshot.System.MCPToolFailures)
	require.Len(t, snapshot.Keys, 1)
	require.Equal(t, bucket.CredentialID, snapshot.Keys[0].KeyID)
	require.EqualValues(t, 2, snapshot.Keys[0].Requests)
}

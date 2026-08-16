package repository

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
			INSERT INTO usage_metric_buckets (
				bucket_start, team_id, key_id, route, method, status_class, request_count
			) VALUES (?, ?, ?, '/api', 'GET', 2, 3)
		`, bucketStart, teamID, apiKeyID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO usage_metric_buckets (
				bucket_start, team_id, key_id, route, method, status_class, request_count
			) VALUES (?, ?, ?, '/sso', 'GET', 2, 3)
		`, bucketStart, teamID, ssoProfileID).Error
	}))

	repo := NewUsageMetricsRepository(appDB, rls)
	snapshot, err := repo.Snapshot(ctx, domain.UsageMetricsFilter{
		From:   bucketStart.Add(-time.Hour),
		To:     bucketStart.Add(time.Hour),
		TeamID: &teamID,
	})
	require.NoError(t, err)
	require.Len(t, snapshot.Keys, 2)
	require.Equal(t, apiKeyID, snapshot.Keys[0].KeyID)
	require.Equal(t, ssoProfileID, snapshot.Keys[1].KeyID)
	var apiFound, ssoFound *domain.UsageKeyMetric
	for index := range snapshot.Keys {
		switch snapshot.Keys[index].KeyID {
		case apiKeyID:
			apiFound = &snapshot.Keys[index]
		case ssoProfileID:
			ssoFound = &snapshot.Keys[index]
		}
	}
	require.NotNil(t, apiFound)
	require.Equal(t, "API Usage Key", apiFound.KeyName)
	require.NotNil(t, ssoFound)
	require.Equal(t, "SSO Usage User", ssoFound.KeyName)
	require.Equal(t, "", ssoFound.KeySuffix)
	require.EqualValues(t, 3, ssoFound.Requests)
}

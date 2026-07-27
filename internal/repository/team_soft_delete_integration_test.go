package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestTeamSoftDeletePreservesSemanticLedgerAndRejectsFutureWork(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-delete-tombstone")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner-delete-tombstone")
	otherTeamID := createLedgerTeam(t, adminDB, rls, "team-delete-transfer-target")
	insertSearchTestContract(t, adminDB, rls, "team-delete-search-read", 3, "exact", "")
	ssoProfileID := uuid.NewString()
	err := rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO team_profiles (
			    id, team_id, key_hash, key_prefix, name, scopes, role,
			    auth_source, sso_subject, sso_entitlement_status
			)
			VALUES (
			    ?::uuid, ?::uuid, NULL, NULL, 'sso-delete-tombstone', ARRAY['read']::text[], 'member',
			    'sso', 'sso-delete-subject', 'active'
			)
		`, ssoProfileID, teamID).Error
	})
	require.NoError(t, err)
	ledger := NewLedgerRepository(appDB, rls)

	created, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "team-delete-preserve",
		RequestHash:    "team-delete-preserve-hash",
		Evidence: []EvidenceInput{{
			Content: "Team deletion preserves accepted evidence provenance.",
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, created)
	searchRepo := NewSearchRepository(appDB, rls)
	searchDoc, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "evidence",
		SourceID:       created.Evidence[0].FragmentID,
		SourceVersion:  1,
		DocumentText:   "Team deletion preserves accepted evidence provenance.",
	})
	require.NoError(t, err)
	require.NotEmpty(t, searchDoc.SearchDocumentID)
	ssoRepo := NewSSORepository(appDB, rls)
	provider := &domain.SSOProvider{
		Name:         "team delete oidc",
		Kind:         domain.SSOProviderKindGenericOIDC,
		IssuerURL:    "https://idp.example.test",
		ClientID:     "client",
		GroupsScopes: []string{},
		Enabled:      true,
	}
	require.NoError(t, ssoRepo.CreateProvider(ctx, provider))
	mapping := &domain.SSOGroupMapping{
		ID:         uuid.New(),
		ProviderID: provider.ID,
		TeamID:     uuid.MustParse(teamID),
		GroupID:    "group-delete",
		GroupName:  "Delete Team Group",
		Scopes:     []string{"read"},
		Role:       "member",
		Enabled:    true,
	}
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO sso_group_mappings (
			    id, provider_id, team_id, group_id, group_name, scopes, role, enabled
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?, ?, ?::text[], ?, true
			)
		`, mapping.ID, mapping.ProviderID, mapping.TeamID, mapping.GroupID, mapping.GroupName, pq.Array(mapping.Scopes), mapping.Role).Error
	}))

	profileRepo := NewProfileRepository(appDB, rls)
	require.NoError(t, profileRepo.SoftDelete(ctx, uuid.MustParse(teamID)))

	err = rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var status string
		var deleted bool
		if err := tx.Raw(`
			SELECT status, deleted_at IS NOT NULL
			FROM teams
			WHERE id = ?::uuid
		`, teamID).Row().Scan(&status, &deleted); err != nil {
			return err
		}
		assert.Equal(t, "deleted", status)
		assert.True(t, deleted)

		var revokedCount int64
		if err := tx.Raw(`
			SELECT COUNT(*)
			FROM team_profiles
			WHERE team_id = ?::uuid
			  AND id = ANY(?::uuid[])
			  AND revoked_at IS NOT NULL
		`, teamID, pq.Array([]string{ownerID, ssoProfileID})).Scan(&revokedCount).Error; err != nil {
			return err
		}
		assert.Equal(t, int64(2), revokedCount)

		var ingestCount, fragmentCount int64
		if err := tx.Raw(`
			SELECT count(*)
			FROM knowledge_ingests
			WHERE team_id = ?::uuid
		`, teamID).Scan(&ingestCount).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT count(*)
			FROM evidence_fragments
			WHERE team_id = ?::uuid
		`, teamID).Scan(&fragmentCount).Error; err != nil {
			return err
		}
		assert.Equal(t, int64(1), ingestCount)
		assert.Equal(t, int64(1), fragmentCount)
		return nil
	})
	require.NoError(t, err)

	_, err = ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "team-delete-rejected",
		RequestHash:    "team-delete-rejected-hash",
		Evidence: []EvidenceInput{{
			Content: "This write must not commit after the team is deleted.",
		}},
	})
	require.ErrorIs(t, err, ErrTeamInactive)

	claimed, err := ledger.ClaimNextPlacementRun(ctx, teamID, "worker-deleted-team", time.Minute)
	require.ErrorIs(t, err, ErrTeamInactive)
	require.Nil(t, claimed)

	hits, err := searchRepo.SearchFullText(ctx, FullTextSearchInput{
		TeamID: teamID,
		Query:  "provenance",
		Limit:  5,
	})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Equal(t, searchDoc.SearchDocumentID, hits[0].SearchDocumentID)

	mapping.TeamID = uuid.MustParse(otherTeamID)
	err = ssoRepo.UpdateMapping(ctx, mapping)
	require.ErrorIs(t, err, ErrTeamInactive)
}

//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func createLedgerSSOIdentity(t *testing.T, db *gorm.DB, rls storagepostgres.RLSHelper, teamID uuid.UUID) uuid.UUID {
	t.Helper()
	providerID, identityID, membershipID := uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO sso_providers (id, name, kind, issuer_url, client_id) VALUES (?, ?, 'generic_oidc', 'https://issuer.example.test', ?)`, providerID, "provider-"+providerID.String(), "client-"+providerID.String()).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO sso_identities (id, provider_id, subject, email, display_name) VALUES (?, ?, ?, ?, ?)`, identityID, providerID, "subject-"+identityID.String(), "user-"+identityID.String()+"@example.test", "SSO test user").Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO actor_identities (id, kind, team_id, provider, subject, display_name, active, created_at, updated_at) VALUES (?, 'human', NULL, ?, ?, 'SSO test user', true, ?, ?)`, identityID, providerID.String(), "subject-"+identityID.String(), now, now).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO team_memberships (id, actor_identity_id, team_id, status, team_admin, maximum_grants, sso_provider_id, sso_group_id, sso_entitlement_status, created_at, updated_at) VALUES (?, ?, ?, 'active', false, ARRAY['read','write']::text[], ?, 'test-group', 'active', ?, ?)`, membershipID, identityID, teamID, providerID, now, now).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO membership_grants (membership_id, grant_name, source) VALUES (?, 'read', 'explicit'), (?, 'write', 'explicit')`, membershipID, membershipID).Error
	}))
	return identityID
}

func createOwnedCredential(t *testing.T, repo *accesspostgres.CredentialRepositoryImpl, teamID, ownerID uuid.UUID, name string, binding domain.CredentialMemoryBinding) *domain.Credential {
	t.Helper()
	id := uuid.New()
	prefix := "dm_" + strings.ReplaceAll(id.String(), "-", "")[:20]
	credential := &domain.Credential{ID: id, TeamID: teamID, Name: name, KeyHash: "hash-" + id.String(), KeyPrefix: prefix, KeySuffix: "suffix", Scopes: []string{"read", "write"}, RateLimit: 60, OwnerIdentityID: &ownerID, MemoryBinding: binding}
	require.NoError(t, repo.CreateCredential(context.Background(), credential))
	return credential
}

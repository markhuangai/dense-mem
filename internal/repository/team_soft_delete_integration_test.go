package repository

import (
	"context"
	"errors"
	"strings"
	"sync"
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
	secondaryProfileID := createLedgerProfile(t, adminDB, rls, teamID, "secondary-delete-tombstone")
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
	apiKeyRepo := NewCredentialRepository(appDB, rls)
	apiRows, err := apiKeyRepo.UpdateNameForTeam(ctx, uuid.MustParse(teamID), uuid.MustParse(ownerID), "owner-before-delete")
	require.NoError(t, err)
	require.EqualValues(t, 1, apiRows)
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

	profileRepo := NewTeamRepository(appDB, rls)
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
			FROM credentials
			WHERE team_id = ?::uuid
			  AND id = ANY(?::uuid[])
			  AND status = 'revoked'
			  AND revoked_at IS NOT NULL
		`, teamID, pq.Array([]string{ownerID, secondaryProfileID})).Scan(&revokedCount).Error; err != nil {
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

	apiRows, err = apiKeyRepo.UpdateScopesForTeam(ctx, uuid.MustParse(teamID), uuid.MustParse(ownerID), []string{"read"})
	require.ErrorIs(t, err, ErrTeamInactive)
	require.Zero(t, apiRows)

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

func TestTeamHardDeleteRemovesEmptyTeamMemorySpaceCatalog(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "team-hard-delete-empty"))

	require.NoError(t, NewTeamRepository(appDB, rls).HardDelete(ctx, teamID))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var teamCount, spaceCount int64
		if err := tx.Raw(`SELECT COUNT(*) FROM teams WHERE id = ?`, teamID).Scan(&teamCount).Error; err != nil {
			return err
		}
		if err := tx.Raw(`SELECT COUNT(*) FROM memory_spaces WHERE team_id = ?`, teamID).Scan(&spaceCount).Error; err != nil {
			return err
		}
		require.Zero(t, teamCount)
		require.Zero(t, spaceCount)
		return nil
	}))
}

func TestSSORuntimeEntitlementsExcludeArchivedTeams(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	activeTeamID := createLedgerTeam(t, adminDB, rls, "sso-active-team")
	archivedTeamID := createLedgerTeam(t, adminDB, rls, "sso-archived-team")
	repo := NewSSORepository(appDB, rls)
	provider := &domain.SSOProvider{
		Name:         "sso-active-team-filter",
		Kind:         domain.SSOProviderKindGenericOIDC,
		IssuerURL:    "https://idp.example.test",
		ClientID:     "client",
		Enabled:      true,
		Scopes:       []string{"openid"},
		GroupClaims:  []string{"groups"},
		GroupsScopes: []string{},
	}
	require.NoError(t, repo.CreateProvider(ctx, provider))

	activeMapping := domain.SSOGroupMapping{
		ID:         uuid.New(),
		ProviderID: provider.ID,
		TeamID:     uuid.MustParse(activeTeamID),
		GroupID:    "shared-group",
		GroupName:  "Active team",
		Scopes:     []string{"read"},
		Role:       "member",
		Enabled:    true,
	}
	archivedMapping := activeMapping
	archivedMapping.ID = uuid.New()
	archivedMapping.TeamID = uuid.MustParse(archivedTeamID)
	archivedMapping.GroupName = "Archived team"
	require.NoError(t, repo.CreateMapping(ctx, &activeMapping))
	require.NoError(t, repo.CreateMapping(ctx, &archivedMapping))
	activeMapping.GroupName = "Active team updated"
	require.NoError(t, repo.UpdateMapping(ctx, &activeMapping))

	identity := domain.SSOIdentity{
		ProviderID:  provider.ID,
		Subject:     "subject-active-team-filter",
		Email:       "sso@example.test",
		DisplayName: "SSO User",
	}
	require.NoError(t, repo.UpsertIdentity(ctx, &identity))
	activeProfile, err := repo.UpsertTeamMembershipForMapping(ctx, identity, activeMapping, "active-profile")
	require.NoError(t, err)
	archivedProfile, err := repo.UpsertTeamMembershipForMapping(ctx, identity, archivedMapping, "archived-profile")
	require.NoError(t, err)
	duplicateAliasID := uuid.New()
	validSession := domain.SSOSession{
		SessionHash:  "sso-session-alias-filter-" + uuid.NewString(),
		IdentityID:   identity.ID,
		ProviderID:   provider.ID,
		MembershipID: activeProfile.ID,
		OwnerID:      activeProfile.OwnerID,
		TeamID:       activeMapping.TeamID,
		CSRFHash:     "sso-session-alias-filter-csrf",
		ExpiresAt:    time.Now().UTC().Add(time.Hour),
		CreatedAt:    time.Now().UTC(),
	}
	require.NoError(t, repo.CreateSession(ctx, validSession))
	credentialAliasPrefix := strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO credentials (
				id, actor_identity_id, team_id, kind, key_hash, key_prefix, key_suffix, name, scopes
			) VALUES (?, ?, ?, 'api_key', 'sso-alias-filter-hash', ?, 'suffix', 'sso-alias-filter', ARRAY['read']::text[])
		`, duplicateAliasID, identity.ID, activeMapping.TeamID, credentialAliasPrefix).Error; err != nil {
			return err
		}
		// Keep this credential alias newest so a latest-alias lookup would fail the membership-only assertion below.
		return tx.Exec(`
			INSERT INTO ownership_aliases (
				team_id, legacy_owner_id, canonical_identity_id, credential_id, reason, created_at
			) VALUES (?, ?, ?, ?, 'credential', now() + interval '1 second')
		`, activeMapping.TeamID, duplicateAliasID, identity.ID, duplicateAliasID).Error
	}))
	err = repo.CreateSession(ctx, domain.SSOSession{
		SessionHash: "sso-session-credential-alias-" + uuid.NewString(),
		IdentityID:  identity.ID,
		ProviderID:  provider.ID,
		OwnerID:     duplicateAliasID,
		TeamID:      activeMapping.TeamID,
		CSRFHash:    "sso-session-credential-alias-csrf",
		ExpiresAt:   time.Now().UTC().Add(time.Hour),
		CreatedAt:   time.Now().UTC(),
	})
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.NoError(t, repo.UpdateSessionTeam(ctx, validSession.SessionHash, activeMapping.TeamID))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE teams
			SET status = 'archived',
			    updated_at = now()
			WHERE id = ?::uuid
		`, archivedTeamID).Error
	}))

	_, err = repo.UpsertTeamMembershipForMapping(ctx, identity, archivedMapping, "archived-profile")
	require.ErrorIs(t, err, ErrTeamInactive)

	mappings, err := repo.ListMappingsForGroups(ctx, provider.ID, []string{"shared-group"})
	require.NoError(t, err)
	require.Len(t, mappings, 1)
	assert.Equal(t, activeMapping.TeamID, mappings[0].TeamID)
	assert.Equal(t, activeMapping.GroupName, mappings[0].GroupName)

	teams, err := repo.ListTeamMembershipsForIdentity(ctx, identity.ID)
	require.NoError(t, err)
	require.Len(t, teams, 1)
	require.Equal(t, activeProfile.ID, teams[0].Membership.ID)
	assert.Equal(t, activeMapping.TeamID, teams[0].Team.ID)
	assert.Equal(t, "active-profile", teams[0].Membership.Name)

	loadedActive, err := repo.GetTeamMembershipByOwnerID(ctx, activeProfile.OwnerID)
	require.NoError(t, err)
	require.NotNil(t, loadedActive)
	assert.Equal(t, "active-profile", loadedActive.Membership.Name)
	loadedArchived, err := repo.GetTeamMembershipByOwnerID(ctx, archivedProfile.OwnerID)
	require.NoError(t, err)
	assert.Nil(t, loadedArchived)
}

func TestActiveTeamMutationGuardSerializesWithTeamDelete(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-delete-lock-guard")
	release := make(chan struct{})
	var releaseOnce sync.Once
	closeRelease := func() {
		releaseOnce.Do(func() {
			close(release)
		})
	}
	t.Cleanup(closeRelease)
	locked := make(chan error, 1)
	done := make(chan error, 1)

	go func() {
		done <- rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
			err := ensureActiveTeamForMutation(ctx, tx, teamID)
			locked <- err
			if err != nil {
				return err
			}
			<-release
			return nil
		})
	}()

	require.NoError(t, <-locked)

	err := rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`SET LOCAL lock_timeout = '100ms'`).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE teams
			SET status = 'deleted',
			    deleted_at = now(),
			    updated_at = now()
			WHERE id = ?::uuid
		`, teamID).Error
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "lock timeout")
	closeRelease()
	require.NoError(t, <-done)
}

func TestActiveTeamMutationGuardsAllowConcurrentTransactions(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-concurrent-mutation-guards")
	release := make(chan struct{})
	var releaseOnce sync.Once
	closeRelease := func() {
		releaseOnce.Do(func() {
			close(release)
		})
	}
	t.Cleanup(closeRelease)
	locked := make(chan error, 1)
	done := make(chan error, 1)

	go func() {
		done <- rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
			err := ensureActiveTeamForMutation(ctx, tx, teamID)
			locked <- err
			if err != nil {
				return err
			}
			<-release
			return nil
		})
	}()
	require.NoError(t, <-locked)

	err := rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		if err := tx.Exec(`SET LOCAL lock_timeout = '100ms'`).Error; err != nil {
			return err
		}
		if err := ensureActiveTeamForMutation(ctx, tx, teamID); err != nil {
			return err
		}
		active, err := embeddingJobFinalizationTeamActive(ctx, tx, teamID)
		if err != nil {
			return err
		}
		if !active {
			return errors.New("active team reported inactive")
		}
		return nil
	})
	require.NoError(t, err)
	closeRelease()
	require.NoError(t, <-done)
}

func TestTombstonedTeamTerminalizesEmbeddingJobsWithoutUpdatingDocuments(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-delete-terminal-embedding")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner-delete-terminal-embedding")
	insertSearchTestContract(t, adminDB, rls, "team-delete-terminal-embedding", 3, "exact", "")
	searchRepo := NewSearchRepository(appDB, rls)

	for _, sourceID := range []string{uuid.NewString(), uuid.NewString(), uuid.NewString()} {
		_, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
			TeamID:         teamID,
			OwnerProfileID: ownerID,
			SourceKind:     "evidence",
			SourceID:       sourceID,
			SourceVersion:  1,
			DocumentText:   "A claimed job must become terminal after team deletion.",
		})
		require.NoError(t, err)
	}

	const workerID = "worker-delete-terminal-embedding"
	jobs, err := searchRepo.ClaimEmbeddingJobs(ctx, ClaimEmbeddingJobsInput{
		TeamID:   teamID,
		WorkerID: workerID,
		Limit:    2,
		Lease:    time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, jobs, 2)

	profileRepo := NewTeamRepository(appDB, rls)
	require.NoError(t, profileRepo.SoftDelete(ctx, uuid.MustParse(teamID)))

	err = searchRepo.CompleteEmbeddingJob(ctx, CompleteEmbeddingJobInput{
		TeamID:           teamID,
		EmbeddingJobID:   jobs[0].EmbeddingJobID,
		WorkerID:         workerID,
		ExpectedAttempts: jobs[0].Attempts,
		Embedding:        []float32{1, 0, 0},
	})
	require.ErrorIs(t, err, ErrTeamInactive)

	failed, err := searchRepo.FailEmbeddingJob(ctx, FailEmbeddingJobInput{
		TeamID:           teamID,
		EmbeddingJobID:   jobs[1].EmbeddingJobID,
		WorkerID:         workerID,
		ExpectedAttempts: jobs[1].Attempts,
		Error:            "team deleted",
		Terminal:         true,
	})
	require.Nil(t, failed)
	require.ErrorIs(t, err, ErrTeamInactive)

	err = rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var terminalJobs, pendingJobs, unchangedDocuments int64
		if err := tx.Raw(`
			SELECT COUNT(*)
			FROM embedding_jobs
			WHERE team_id = ?::uuid
			  AND status = 'stale'
			  AND completed_at IS NOT NULL
			  AND lease_until IS NULL
		`, teamID).Scan(&terminalJobs).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*)
			FROM embedding_jobs
			WHERE team_id = ?::uuid
			  AND status IN ('queued', 'processing')
		`, teamID).Scan(&pendingJobs).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*)
			FROM search_documents
			WHERE team_id = ?::uuid
			  AND search_state = 'pending'
			  AND embedding IS NULL
		`, teamID).Scan(&unchangedDocuments).Error; err != nil {
			return err
		}
		assert.Equal(t, int64(3), terminalJobs)
		assert.Zero(t, pendingJobs)
		assert.Equal(t, int64(3), unchangedDocuments)
		return nil
	})
	require.NoError(t, err)
}

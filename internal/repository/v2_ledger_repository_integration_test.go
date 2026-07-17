package repository

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

const (
	v2LedgerTestRole     = "densemem_rls_test"
	v2LedgerTestPassword = "densemem_rls_test"
)

func setupV2LedgerRepositoryDB(t *testing.T) (*gorm.DB, *gorm.DB, *storagepostgres.RLS, func()) {
	t.Helper()

	dsn := storagepostgres.GetTestDSN()
	if dsn == "" {
		t.Skip("set DATABASE_URL to run V2 ledger PostgreSQL integration tests")
	}
	if os.Getenv("DENSE_MEM_ALLOW_DESTRUCTIVE_POSTGRES_TESTS") != "1" {
		t.Skip("set DENSE_MEM_ALLOW_DESTRUCTIVE_POSTGRES_TESTS=1 to run destructive V2 ledger PostgreSQL integration tests")
	}

	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)

	migrator, err := storagepostgres.NewMigrator(db)
	require.NoError(t, err)
	require.NoError(t, migrator.RunUp(context.Background()))

	rls := storagepostgres.NewRLS()
	require.NoError(t, rls.WithMigrationTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Exec(fmt.Sprintf(`
			DO $$
			BEGIN
				IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%[1]s') THEN
					CREATE ROLE %[1]s LOGIN PASSWORD '%[2]s' NOSUPERUSER NOBYPASSRLS;
				ELSE
					ALTER ROLE %[1]s WITH LOGIN PASSWORD '%[2]s' NOSUPERUSER NOBYPASSRLS;
				END IF;
			END $$;
			GRANT USAGE ON SCHEMA public TO %[1]s;
			GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %[1]s;
			GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %[1]s;
		`, v2LedgerTestRole, v2LedgerTestPassword)).Error
	}))

	appDB, err := gorm.Open(gormpostgres.Open(v2LedgerAppDSN(t, dsn)), &gorm.Config{})
	require.NoError(t, err)

	cleanup := func() {
		_ = rls.WithMigrationTx(context.Background(), db, truncateV2LedgerFixtures)
		if sqlDB, err := appDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		_ = rls.WithMigrationTx(context.Background(), db, func(tx *gorm.DB) error {
			return tx.Exec(fmt.Sprintf(`
				REASSIGN OWNED BY %[1]s TO CURRENT_USER;
				DROP OWNED BY %[1]s;
				DROP ROLE IF EXISTS %[1]s;
			`, v2LedgerTestRole)).Error
		})
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}
	require.NoError(t, rls.WithMigrationTx(context.Background(), db, truncateV2LedgerFixtures))
	return db, appDB, rls, cleanup
}

func v2LedgerAppDSN(t *testing.T, dsn string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		t.Skip("V2 ledger RLS integration tests require DATABASE_URL in URL form")
	}
	parsed.User = url.UserPassword(v2LedgerTestRole, v2LedgerTestPassword)
	return parsed.String()
}

func truncateV2LedgerFixtures(tx *gorm.DB) error {
	return tx.Exec(`
		TRUNCATE
			embedding_jobs,
			search_documents,
			hypotheses,
			review_tasks,
			relationship_cross_references,
			entity_correction_events,
			entity_resolution_events,
			relationship_transition_events,
			relationship_support_decision_events,
			relationship_evidence_supports,
			verification_events,
			relationship_observations,
			relationship_records,
			value_records,
			entity_names,
			entity_records,
			placement_outcomes,
			placement_items,
			placement_runs,
			evidence_quarantines,
			evidence_security_signals,
			evidence_security_events,
			evidence_fragments,
			evidence_source_revisions,
			evidence_sources,
			knowledge_ingests,
			semantic_profile_refs,
			semantic_team_refs,
			team_profiles,
			teams
		CASCADE
	`).Error
}

func createV2LedgerTeam(t *testing.T, db *gorm.DB, rls *storagepostgres.RLS, teamName string) string {
	t.Helper()

	teamID := uuid.NewString()
	err := rls.WithMigrationTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO teams (id, name, description, metadata, config)
			VALUES (?::uuid, ?, '', '{}'::jsonb, '{}'::jsonb)
		`, teamID, teamName).Error
	})
	require.NoError(t, err)
	return teamID
}

func createV2LedgerProfile(t *testing.T, db *gorm.DB, rls *storagepostgres.RLS, teamID string, profileName string) string {
	t.Helper()

	profileID := uuid.NewString()
	keyPrefix := strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	err := rls.WithMigrationTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO team_profiles (
			    id, team_id, key_hash, key_prefix, key_suffix, name, scopes, role
			) VALUES (
			    ?::uuid, ?::uuid, ?, ?, ?, ?, ARRAY['read','write']::text[], 'member'
			)
		`, profileID, teamID, "hash-"+profileID, keyPrefix, keyPrefix[:6], profileName).Error
	})
	require.NoError(t, err)
	return profileID
}

func TestV2LedgerCreateIngestIsIdempotentAndOwnerScoped(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamA := createV2LedgerTeam(t, adminDB, rls, "team-a")
	ownerA := createV2LedgerProfile(t, adminDB, rls, teamA, "owner-a")
	ownerB := createV2LedgerProfile(t, adminDB, rls, teamA, "owner-b")
	teamC := createV2LedgerTeam(t, adminDB, rls, "team-c")
	ownerC := createV2LedgerProfile(t, adminDB, rls, teamC, "owner-c")
	repo := NewV2LedgerRepository(appDB, rls)

	first, err := repo.CreateIngest(ctx, V2CreateIngestInput{
		TeamID:         teamA,
		OwnerProfileID: ownerA,
		IdempotencyKey: "idem-1",
		RequestHash:    "request-hash",
		Evidence: []V2EvidenceInput{{
			Content: "Dense-Mem v2 stores exact evidence durably before acknowledgement.",
			Labels:  []string{"v2", "ledger"},
			InitialEvent: &V2SecurityEventDraft{
				EventKind: "deterministic_scan",
				Decision:  "pass",
			},
		}},
	})
	require.NoError(t, err)
	require.False(t, first.Existing)
	require.Len(t, first.Evidence, 1)

	err = rls.WithTeamProfileTx(ctx, appDB, teamA, ownerA, func(tx *gorm.DB) error {
		result := tx.Exec(`
			UPDATE evidence_fragments
			SET content = 'rewritten'
			WHERE team_id = ?::uuid
			  AND fragment_id = ?::uuid
		`, teamA, first.Evidence[0].FragmentID)
		require.NoError(t, result.Error)
		assert.Equal(t, int64(0), result.RowsAffected, "profile transactions must not rewrite append-only evidence")

		result = tx.Exec(`
			DELETE FROM evidence_fragments
			WHERE team_id = ?::uuid
			  AND fragment_id = ?::uuid
		`, teamA, first.Evidence[0].FragmentID)
		require.NoError(t, result.Error)
		assert.Equal(t, int64(0), result.RowsAffected, "profile transactions must not delete append-only evidence")
		return nil
	})
	require.NoError(t, err)

	err = rls.WithMigrationTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE evidence_fragments
			SET content = 'rewritten'
			WHERE team_id = ?::uuid
			  AND fragment_id = ?::uuid
		`, teamA, first.Evidence[0].FragmentID).Error
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "append-only")

	err = rls.WithMigrationTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			DELETE FROM evidence_fragments
			WHERE team_id = ?::uuid
			  AND fragment_id = ?::uuid
		`, teamA, first.Evidence[0].FragmentID).Error
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "append-only")

	second, err := repo.CreateIngest(ctx, V2CreateIngestInput{
		TeamID:         teamA,
		OwnerProfileID: ownerA,
		IdempotencyKey: "idem-1",
		RequestHash:    "request-hash",
		Evidence: []V2EvidenceInput{{
			Content: "Dense-Mem v2 stores exact evidence durably before acknowledgement.",
		}},
	})
	require.NoError(t, err)
	require.True(t, second.Existing)
	assert.Equal(t, first.IngestID, second.IngestID)
	assert.Equal(t, first.PlacementRunID, second.PlacementRunID)

	_, err = repo.CreateIngest(ctx, V2CreateIngestInput{
		TeamID:         teamA,
		OwnerProfileID: ownerA,
		IdempotencyKey: "idem-1",
		RequestHash:    "different-request-hash",
		Evidence: []V2EvidenceInput{{
			Content: "Dense-Mem v2 stores exact evidence durably before acknowledgement.",
		}},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrV2IdempotencyConflict), fmt.Sprintf("err=%v", err))

	err = rls.WithTeamProfileTx(ctx, appDB, teamA, ownerB, func(tx *gorm.DB) error {
		var currentTeam, currentProfile, txMode string
		require.NoError(t, tx.Raw(`SELECT current_setting('app.current_team_id', true)`).Scan(&currentTeam).Error)
		require.NoError(t, tx.Raw(`SELECT current_setting('app.current_profile_id', true)`).Scan(&currentProfile).Error)
		require.NoError(t, tx.Raw(`SELECT current_setting('app.tx_mode', true)`).Scan(&txMode).Error)
		assert.Equal(t, teamA, currentTeam)
		assert.Equal(t, ownerB, currentProfile)
		assert.Equal(t, "profile", txMode)

		var count int64
		if err := tx.Raw(`SELECT count(*) FROM evidence_fragments`).Scan(&count).Error; err != nil {
			return err
		}
		assert.Equal(t, int64(1), count, "same-team profile should read evidence")

		result := tx.Exec(`
			UPDATE knowledge_ingests
			SET status = 'failed'
			WHERE team_id = ?::uuid
			  AND ingest_id = ?::uuid
		`, teamA, first.IngestID)
		require.NoError(t, result.Error)
		assert.Equal(t, int64(0), result.RowsAffected, "same-team different-owner profile must not mutate owner rows")

		return tx.Exec(`
			INSERT INTO evidence_fragments (
			    team_id, ingest_id, owner_profile_id, evidence_index, content, content_hash
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, 9, 'bad owner write', 'sha256:bad'
			)
		`, teamA, first.IngestID, ownerB).Error
	})
	require.Error(t, err)

	err = rls.WithTeamProfileTx(ctx, appDB, teamC, ownerC, func(tx *gorm.DB) error {
		var count int64
		if err := tx.Raw(`SELECT count(*) FROM evidence_fragments`).Scan(&count).Error; err != nil {
			return err
		}
		assert.Equal(t, int64(0), count, "cross-team profile must not see evidence")
		return nil
	})
	require.NoError(t, err)
}

func TestV2LedgerCreateIngestConcurrentIdempotency(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "team-concurrent")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "owner-concurrent")
	repo := NewV2LedgerRepository(appDB, rls)

	const workers = 8
	results := make(chan *V2CreateIngestResult, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := repo.CreateIngest(ctx, V2CreateIngestInput{
				TeamID:         teamID,
				OwnerProfileID: ownerID,
				IdempotencyKey: "concurrent-idem",
				RequestHash:    "concurrent-request",
				Evidence: []V2EvidenceInput{{
					Content: "Concurrent duplicate ingests must collapse to one durable ingest.",
				}},
			})
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	ingestIDs := map[string]struct{}{}
	placementRunIDs := map[string]struct{}{}
	for result := range results {
		require.NotNil(t, result)
		ingestIDs[result.IngestID] = struct{}{}
		placementRunIDs[result.PlacementRunID] = struct{}{}
	}
	require.Len(t, ingestIDs, 1)
	require.Len(t, placementRunIDs, 1)

	err := rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		var ingestCount int64
		if err := tx.Raw(`
			SELECT count(*)
			FROM knowledge_ingests
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND idempotency_key = 'concurrent-idem'
		`, teamID, ownerID).Scan(&ingestCount).Error; err != nil {
			return err
		}
		assert.Equal(t, int64(1), ingestCount)

		var fragmentCount int64
		if err := tx.Raw(`
			SELECT count(*)
			FROM evidence_fragments
			WHERE team_id = ?::uuid
		`, teamID).Scan(&fragmentCount).Error; err != nil {
			return err
		}
		assert.Equal(t, int64(1), fragmentCount)
		return nil
	})
	require.NoError(t, err)
}

func TestV2LedgerSourceRevisionCompareAndSet(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "team-source")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "source-owner")
	otherOwnerID := createV2LedgerProfile(t, adminDB, rls, teamID, "source-other-owner")
	repo := NewV2LedgerRepository(appDB, rls)

	first, err := repo.AdvanceSourceRevision(ctx, V2AdvanceSourceRevisionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKey:      "doc://policy",
		RevisionToken:  "rev-1",
		ContentHash:    "sha256:first",
	})
	require.NoError(t, err)
	require.NotEmpty(t, first.SourceID)

	second, err := repo.AdvanceSourceRevision(ctx, V2AdvanceSourceRevisionInput{
		TeamID:                        teamID,
		OwnerProfileID:                ownerID,
		SourceKey:                     "doc://policy",
		RevisionToken:                 "rev-2",
		ExpectedPreviousRevisionToken: "rev-1",
		ContentHash:                   "sha256:second",
	})
	require.NoError(t, err)
	assert.Equal(t, first.SourceID, second.SourceID)
	assert.NotEqual(t, first.SourceRevisionID, second.SourceRevisionID)

	created, err := repo.CreateIngest(ctx, V2CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		Evidence: []V2EvidenceInput{{
			Content:          "The source revision lineage must be preserved on evidence fragments.",
			SourceID:         second.SourceID,
			SourceRevisionID: second.SourceRevisionID,
		}},
	})
	require.NoError(t, err)
	require.Len(t, created.Evidence, 1)
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		var sourceID, sourceRevisionID string
		if err := tx.Raw(`
			SELECT source_id::text, source_revision_id::text
			FROM evidence_fragments
			WHERE team_id = ?::uuid
			  AND fragment_id = ?::uuid
		`, teamID, created.Evidence[0].FragmentID).Row().Scan(&sourceID, &sourceRevisionID); err != nil {
			return err
		}
		assert.Equal(t, second.SourceID, sourceID)
		assert.Equal(t, second.SourceRevisionID, sourceRevisionID)
		return nil
	})
	require.NoError(t, err)

	_, err = repo.CreateIngest(ctx, V2CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: otherOwnerID,
		Evidence: []V2EvidenceInput{{
			Content:          "Another owner must not attach evidence to this source revision.",
			SourceID:         second.SourceID,
			SourceRevisionID: second.SourceRevisionID,
		}},
	})
	require.Error(t, err)

	_, err = repo.AdvanceSourceRevision(ctx, V2AdvanceSourceRevisionInput{
		TeamID:                        teamID,
		OwnerProfileID:                ownerID,
		SourceKey:                     "doc://policy",
		RevisionToken:                 "rev-3",
		ExpectedPreviousRevisionToken: "rev-1",
		ContentHash:                   "sha256:third",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrV2SourceRevisionConflict), fmt.Sprintf("err=%v", err))
}

func TestV2LedgerPlacementClaimIsTeamLocal(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamA := createV2LedgerTeam(t, adminDB, rls, "team-placement-a")
	ownerA := createV2LedgerProfile(t, adminDB, rls, teamA, "owner-a")
	teamB := createV2LedgerTeam(t, adminDB, rls, "team-placement-b")
	_ = createV2LedgerProfile(t, adminDB, rls, teamB, "owner-b")
	repo := NewV2LedgerRepository(appDB, rls)

	created, err := repo.CreateIngest(ctx, V2CreateIngestInput{
		TeamID:         teamA,
		OwnerProfileID: ownerA,
		IdempotencyKey: "placement-claim",
		RequestHash:    "placement-claim-request",
		Evidence: []V2EvidenceInput{{
			Content: "Placement claim must stay inside one authenticated team.",
		}},
	})
	require.NoError(t, err)

	none, err := repo.ClaimNextPlacementRun(ctx, teamB, "worker-b", 30*time.Second)
	require.NoError(t, err)
	require.Nil(t, none)

	claimed, err := repo.ClaimNextPlacementRun(ctx, teamA, "worker-a", 30*time.Second)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, created.PlacementRunID, claimed.PlacementRunID)
	assert.Equal(t, "processing", claimed.Status)
	assert.Equal(t, 1, claimed.Attempts)
	require.NotNil(t, claimed.LeaseUntil)

	err = repo.FinishPlacementRun(ctx, teamA, claimed.PlacementRunID, "worker-b", "completed", "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrV2PlacementLeaseConflict), fmt.Sprintf("err=%v", err))

	require.NoError(t, repo.FinishPlacementRun(ctx, teamA, claimed.PlacementRunID, "worker-a", "completed", ""))
}

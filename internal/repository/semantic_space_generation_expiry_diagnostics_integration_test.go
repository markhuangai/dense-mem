package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSemanticExpiryIgnoresSealedGenerationAndProcessesActiveRows(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "semantic-expiry-space-generation")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	repo := NewLedgerRepository(appDB, rls)
	spaceRepo := NewMemorySpaceRepository(appDB, rls)
	privateSpace, err := spaceRepo.EnsureCredentialPrivate(ctx, uuid.MustParse(teamID), uuid.New())
	require.NoError(t, err)
	var privateGeneration int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT generation FROM memory_spaces WHERE id = ?::uuid`, privateSpace.ID).Row().Scan(&privateGeneration)
	}))

	sharedIngest := createSemanticIngest(t, ctx, repo, teamID, ownerID, "expiry-shared", "active shared review")
	privateIngest, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, SpaceID: privateSpace.ID.String(), SpaceGeneration: privateGeneration,
		IdempotencyKey: "expiry-private", RequestHash: sha256Hex("sealed private review"),
		Evidence: []EvidenceInput{{Content: "sealed private review"}},
	})
	require.NoError(t, err)

	insertExpiredReview := func(ingest *CreateIngestResult, spaceID string, generation int64, marker string) {
		t.Helper()
		require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
			if err := tx.Exec(`
				UPDATE placement_runs
				SET status = 'awaiting_review', completed_at = now()
				WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
			`, teamID, ingest.PlacementRunID).Error; err != nil {
				return err
			}
			if err := tx.Exec(`
				UPDATE placement_items
				SET status = 'awaiting_review', category = 'candidate'
				WHERE team_id = ?::uuid AND placement_item_id = ?::uuid
			`, teamID, ingest.Items[0].PlacementItemID).Error; err != nil {
				return err
			}
			return tx.Exec(`
				INSERT INTO review_tasks (
				    team_id, owner_profile_id, ingest_id, placement_item_id,
				    task_type, status, reason, payload, dedupe_key,
				    space_id, space_generation, expires_at
				) VALUES (
				    ?::uuid, ?::uuid, ?::uuid, ?::uuid,
				    'identity_needs_review', 'open', 'ambiguous_entity',
				    jsonb_build_object('mention_ref', ?::text), '', NULLIF(?, '')::uuid, NULLIF(?, 0)::bigint, now() - interval '1 minute'
				)
			`, teamID, ownerID, ingest.IngestID, ingest.Items[0].PlacementItemID, marker, spaceID, generation).Error
		}))
	}
	insertExpiredReview(sharedIngest, "", 0, "shared")
	insertExpiredReview(privateIngest, privateSpace.ID.String(), privateGeneration, "private")

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE memory_spaces
			SET lifecycle_state = 'sealed', generation = generation + 1, sealed_at = now(), updated_at = now()
			WHERE id = ?::uuid
		`, privateSpace.ID).Error
	}))

	expired, err := repo.ExpirePlacementAssessmentReviews(ctx, ExpirePlacementAssessmentReviewsInput{
		TeamID: teamID, Now: time.Now().UTC(),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), expired)

	var sharedTask, privateTask, sharedItem, privateItem, sharedRun, privateRun string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT
				(SELECT status FROM review_tasks WHERE team_id = ?::uuid AND ingest_id = ?::uuid),
				(SELECT status FROM review_tasks WHERE team_id = ?::uuid AND ingest_id = ?::uuid),
				(SELECT status FROM placement_items WHERE team_id = ?::uuid AND ingest_id = ?::uuid),
				(SELECT status FROM placement_items WHERE team_id = ?::uuid AND ingest_id = ?::uuid),
				(SELECT status FROM placement_runs WHERE team_id = ?::uuid AND ingest_id = ?::uuid),
				(SELECT status FROM placement_runs WHERE team_id = ?::uuid AND ingest_id = ?::uuid)
		`, teamID, sharedIngest.IngestID, teamID, privateIngest.IngestID,
			teamID, sharedIngest.IngestID, teamID, privateIngest.IngestID,
			teamID, sharedIngest.IngestID, teamID, privateIngest.IngestID).Row().Scan(
			&sharedTask, &privateTask, &sharedItem, &privateItem, &sharedRun, &privateRun,
		)
	}))
	assert.Equal(t, "expired", sharedTask)
	assert.Equal(t, "open", privateTask)
	assert.Equal(t, "completed", sharedItem)
	assert.Equal(t, "awaiting_review", privateItem)
	assert.Equal(t, "completed", sharedRun)
	assert.Equal(t, "awaiting_review", privateRun)
}

func TestSubmissionDiagnosticsHideSealedGenerationAndKeepActiveHydration(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-diagnostics-space-generation")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	repo := NewLedgerRepository(appDB, rls)
	privateSpace, err := NewMemorySpaceRepository(appDB, rls).EnsureCredentialPrivate(ctx, uuid.MustParse(teamID), uuid.New())
	require.NoError(t, err)
	var privateGeneration int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT generation FROM memory_spaces WHERE id = ?::uuid`, privateSpace.ID).Row().Scan(&privateGeneration)
	}))
	shared, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "diagnostics-shared",
		RequestHash: sha256Hex("active shared diagnostics"), Evidence: []EvidenceInput{{Content: "active shared diagnostics"}},
	})
	require.NoError(t, err)
	private, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, SpaceID: privateSpace.ID.String(), SpaceGeneration: privateGeneration,
		IdempotencyKey: "diagnostics-private", RequestHash: sha256Hex("sealed private diagnostics"),
		Evidence: []EvidenceInput{{Content: "sealed private diagnostics"}},
	})
	require.NoError(t, err)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE memory_spaces
			SET lifecycle_state = 'sealed', generation = generation + 1, sealed_at = now(), updated_at = now()
			WHERE id = ?::uuid
		`, privateSpace.ID).Error
	}))

	page, err := repo.ListSubmissionDiagnostics(ctx, SubmissionDiagnosticFilter{TeamID: teamID, Limit: 10})
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Len(t, page.Records, 1)
	assert.Equal(t, shared.IngestID, page.Records[0].Placement.IngestID)
	assert.Equal(t, 1, page.Records[0].EvidenceCount)

	detail, err := repo.GetSubmissionDiagnostic(ctx, teamID, shared.IngestID)
	require.NoError(t, err)
	require.NotNil(t, detail)
	assert.Equal(t, shared.IngestID, detail.Placement.IngestID)
	assert.Len(t, detail.Placement.Evidence, 1)
	assert.Len(t, detail.Placement.Items, 1)

	_, err = repo.GetSubmissionDiagnostic(ctx, teamID, private.IngestID)
	assert.ErrorIs(t, err, ErrSubmissionDiagnosticNotFound)
}

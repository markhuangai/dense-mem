//go:build integration

package postgres

import (
	"context"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"

	"errors"
	privacypostgres "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func TestKnownEvidenceFenceSerializesConcurrentQuarantine(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "known-support-quarantine-race")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "known-support-quarantine-owner")
	ledger := NewStore(appDB, rls)
	semantic := NewStore(appDB, rls)
	sharedSpace, err := privacypostgres.NewMemorySpaceRepository(appDB, rls).GetTeamShared(ctx, uuid.MustParse(teamID))
	require.NoError(t, err)
	require.NotNil(t, sharedSpace)
	known := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "known-support-quarantine-source", "Evidence held by the known-evidence fence.")
	loaded, err := loadKnownEvidenceSnapshotForTest(ctx, semantic, knowledgecontract.SubmissionAssessmentKnownEvidenceInput{
		TeamID: teamID, OwnerProfileID: ownerID, SpaceID: sharedSpace.ID.String(), EvidenceIDs: []string{known.Evidence[0].FragmentID},
	})
	require.NoError(t, err)
	require.Len(t, loaded.Evidence, 1)
	commitInput := CommitSubmissionAssessmentInput{
		RememberCommitScope:   RememberCommitScope{TeamID: teamID, OwnerProfileID: ownerID, IngestID: uuid.NewString()},
		KnownEvidenceSnapshot: loaded.Evidence,
	}

	commitReady := make(chan struct{})
	releaseCommit := make(chan struct{})
	commitErr := make(chan error, 1)
	go func() {
		commitErr <- rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
			if err := reauthorizeSubmissionKnownEvidence(ctx, tx, commitInput); err != nil {
				return err
			}
			close(commitReady)
			<-releaseCommit
			return nil
		})
	}()
	<-commitReady

	quarantineStarted := make(chan struct{})
	quarantineErr := make(chan error, 1)
	go func() {
		quarantineErr <- rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
			close(quarantineStarted)
			return insertEvidenceQuarantine(ctx, tx, CreateIngestInput{TeamID: teamID, OwnerProfileID: ownerID}, known.IngestID, known.Evidence[0].FragmentID, "concurrent quarantine")
		})
	}()
	<-quarantineStarted
	select {
	case err := <-quarantineErr:
		t.Fatalf("quarantine completed before the known-evidence fence released: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseCommit)
	require.NoError(t, <-commitErr)
	require.NoError(t, <-quarantineErr)
	var quarantineCount int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT count(*)
			FROM evidence_quarantines
			WHERE team_id = ?::uuid AND fragment_id = ?::uuid AND status = 'active'
		`, teamID, known.Evidence[0].FragmentID).Scan(&quarantineCount).Error
	}))
	require.EqualValues(t, 1, quarantineCount)
	commitFenceErr := rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return reauthorizeSubmissionKnownEvidence(ctx, tx, commitInput)
	})
	require.ErrorIs(t, commitFenceErr, ErrSubmissionAssessmentKnownEvidenceStale, "anchor-only known evidence must fail reauthorization after quarantine")
}

func TestKnownEvidenceFenceAllowsVisibleCrossOwnerEvidenceUnderRLS(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "known-support-cross-owner-fence")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "known-support-cross-owner-fence-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "known-support-cross-owner-fence-owner-b")
	ledger := NewStore(appDB, rls)
	semantic := NewStore(appDB, rls)
	sharedSpace, err := privacypostgres.NewMemorySpaceRepository(appDB, rls).GetTeamShared(ctx, uuid.MustParse(teamID))
	require.NoError(t, err)
	require.NotNil(t, sharedSpace)
	known := createSemanticIngest(t, ctx, ledger, teamID, ownerA, "known-support-cross-owner-fence-known", "Visible shared evidence belongs to owner A.")

	loaded, err := loadKnownEvidenceSnapshotForTest(ctx, semantic, knowledgecontract.SubmissionAssessmentKnownEvidenceInput{
		TeamID: teamID, OwnerProfileID: ownerB, SpaceID: sharedSpace.ID.String(), EvidenceIDs: []string{known.Evidence[0].FragmentID},
	})
	require.NoError(t, err)
	require.Len(t, loaded.Evidence, 1)
	require.Equal(t, ownerA, loaded.Evidence[0].OwnerProfileID)

	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerB, func(tx *gorm.DB) error {
		return reauthorizeSubmissionKnownEvidence(ctx, tx, CommitSubmissionAssessmentInput{
			RememberCommitScope:   RememberCommitScope{TeamID: teamID, OwnerProfileID: ownerB, IngestID: uuid.NewString()},
			KnownEvidenceSnapshot: loaded.Evidence,
		})
	})
	require.NoError(t, err)
}

func TestKnownEvidenceFenceRejectsSnapshotAfterSameTransactionSourceAdvance(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "known-support-source-advance")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "known-support-source-advance-owner")
	ledger := NewStore(appDB, rls)
	semantic := NewStore(appDB, rls)
	sharedSpace, err := privacypostgres.NewMemorySpaceRepository(appDB, rls).GetTeamShared(ctx, uuid.MustParse(teamID))
	require.NoError(t, err)
	require.NotNil(t, sharedSpace)
	const sourceKey = "document://known-support-source-advance"

	knownContent := "Known evidence belongs to source revision one."
	known, err := createTestIngest(ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "known-support-source-advance-rev-1",
		RequestHash: sha256Hex(knownContent),
		Evidence: []EvidenceInput{{
			Content: knownContent, SourceType: "document", Authority: "primary", SourceKey: sourceKey,
			SourceRevisionToken: "rev-1", SourceRevisionContentHash: sha256Hex(knownContent),
		}},
	})
	require.NoError(t, err)
	require.Len(t, known.Evidence, 1)
	loaded, err := loadKnownEvidenceSnapshotForTest(ctx, semantic, knowledgecontract.SubmissionAssessmentKnownEvidenceInput{
		TeamID: teamID, OwnerProfileID: ownerID, SpaceID: sharedSpace.ID.String(), EvidenceIDs: []string{known.Evidence[0].FragmentID},
	})
	require.NoError(t, err)
	require.Len(t, loaded.Evidence, 1)

	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		advancedContent := "The source now belongs to revision two."
		_, err := advanceSourceRevisionInTx(ctx, tx, AdvanceSourceRevisionInput{
			TeamID: teamID, OwnerProfileID: ownerID, SourceKey: sourceKey,
			RevisionToken: "rev-2", ExpectedPreviousRevisionToken: "rev-1",
			ContentHash: sha256Hex(advancedContent),
		}, nil)
		if err != nil {
			return err
		}
		return reauthorizeSubmissionKnownEvidence(ctx, tx, CommitSubmissionAssessmentInput{
			RememberCommitScope:   RememberCommitScope{TeamID: teamID, OwnerProfileID: ownerID, IngestID: uuid.NewString()},
			KnownEvidenceSnapshot: loaded.Evidence,
		})
	})
	require.ErrorIs(t, err, ErrSubmissionAssessmentKnownEvidenceStale,
		"a source advance in the same transaction must not make superseded known evidence supportable")
}

func TestKnownEvidenceFenceFailsClosedWhenSourceRevisionIsLocked(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "known-support-source-lock")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "known-support-source-lock-owner")
	ledger := NewStore(appDB, rls)
	semantic := NewStore(appDB, rls)
	sharedSpace, err := privacypostgres.NewMemorySpaceRepository(appDB, rls).GetTeamShared(ctx, uuid.MustParse(teamID))
	require.NoError(t, err)
	require.NotNil(t, sharedSpace)
	known, err := createTestIngest(ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "known-source-lock", RequestHash: "known-source-lock-hash",
		Evidence: []EvidenceInput{{
			Content: "Source-backed known evidence.", SourceKey: "document://known-source-lock", SourceRevisionToken: "rev-1",
		}},
	})
	require.NoError(t, err)
	require.Len(t, known.Evidence, 1)
	loaded, err := loadKnownEvidenceSnapshotForTest(ctx, semantic, knowledgecontract.SubmissionAssessmentKnownEvidenceInput{
		TeamID: teamID, OwnerProfileID: ownerID, SpaceID: sharedSpace.ID.String(), EvidenceIDs: []string{known.Evidence[0].FragmentID},
	})
	require.NoError(t, err)
	require.Len(t, loaded.Evidence, 1)

	lockHeld := make(chan struct{})
	releaseLock := make(chan struct{})
	lockErr := make(chan error, 1)
	go func() {
		lockErr <- rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
			var sourceID string
			if err := tx.Raw(`
				SELECT source_id::text
				FROM evidence_sources
				WHERE team_id = ?::uuid AND source_id = ?::uuid AND owner_profile_id = ?::uuid
				FOR UPDATE
			`, teamID, known.Evidence[0].SourceID, ownerID).Row().Scan(&sourceID); err != nil {
				return err
			}
			close(lockHeld)
			<-releaseLock
			return nil
		})
	}()
	<-lockHeld
	commitErr := rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return reauthorizeSubmissionKnownEvidence(ctx, tx, CommitSubmissionAssessmentInput{
			RememberCommitScope:   RememberCommitScope{TeamID: teamID, OwnerProfileID: ownerID, IngestID: uuid.NewString()},
			KnownEvidenceSnapshot: loaded.Evidence,
		})
	})
	require.ErrorIs(t, commitErr, ErrSubmissionAssessmentKnownEvidenceStale)
	close(releaseLock)
	require.NoError(t, <-lockErr)
}

func TestKnownEvidenceFenceFailsClosedWhenPrivateSpaceSeals(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "known-support-space-lock")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "known-support-space-lock-owner")
	ledger := NewStore(appDB, rls)
	semantic := NewStore(appDB, rls)
	privateSpace, err := ensurePrivateSpaceForOwner(t, appDB, rls, ctx, uuid.MustParse(teamID), uuid.MustParse(ownerID))
	require.NoError(t, err)
	generation := privateSpaceGeneration(t, ctx, adminDB, rls, privateSpace.ID)
	privateCtx := requestctx.WithAllowedSpaces(ctx, []domain.MemorySpaceAccess{{ID: privateSpace.ID, Kind: domain.MemorySpacePrivate}})
	known, err := createTestIngest(privateCtx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, SpaceID: privateSpace.ID.String(), SpaceGeneration: generation,
		IdempotencyKey: "known-space-lock", RequestHash: "known-space-lock-hash",
		Evidence: []EvidenceInput{{Content: "Private known evidence becomes stale when its space seals."}},
	})
	require.NoError(t, err)
	require.Len(t, known.Evidence, 1)
	loaded, err := loadKnownEvidenceSnapshotForTest(privateCtx, semantic, knowledgecontract.SubmissionAssessmentKnownEvidenceInput{
		TeamID: teamID, OwnerProfileID: ownerID, SpaceID: privateSpace.ID.String(), EvidenceIDs: []string{known.Evidence[0].FragmentID},
	})
	require.NoError(t, err)
	require.Len(t, loaded.Evidence, 1)
	commitInput := CommitSubmissionAssessmentInput{
		RememberCommitScope:   RememberCommitScope{TeamID: teamID, OwnerProfileID: ownerID, IngestID: uuid.NewString()},
		KnownEvidenceSnapshot: loaded.Evidence,
	}
	require.NoError(t, rls.WithTeamProfileTx(privateCtx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return reauthorizeSubmissionKnownEvidence(ctx, tx, commitInput)
	}), "visible private known evidence must reauthorize before the space race")

	sealReady := make(chan struct{})
	releaseSeal := make(chan struct{})
	sealErr := make(chan error, 1)
	released := false
	defer func() {
		if !released {
			close(releaseSeal)
		}
	}()
	go func() {
		sealErr <- rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
			result := tx.Exec(`
				UPDATE memory_spaces
				SET lifecycle_state = 'sealed', generation = generation + 1,
				    sealed_at = now(), updated_at = now()
				WHERE team_id = ?::uuid AND id = ?::uuid AND lifecycle_state = 'active'
			`, teamID, privateSpace.ID)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errors.New("private known-evidence space was not sealed")
			}
			close(sealReady)
			<-releaseSeal
			return nil
		})
	}()
	select {
	case <-sealReady:
	case err := <-sealErr:
		require.NoError(t, err)
		return
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for private-space seal")
	}

	commitErr := rls.WithTeamProfileTx(privateCtx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return reauthorizeSubmissionKnownEvidence(ctx, tx, commitInput)
	})
	close(releaseSeal)
	released = true
	require.ErrorIs(t, commitErr, ErrSubmissionAssessmentKnownEvidenceStale)
	require.NoError(t, <-sealErr)
}

func privateSpaceGeneration(t *testing.T, ctx context.Context, db *gorm.DB, rls *storagepostgres.RLS, spaceID uuid.UUID) int64 {
	t.Helper()
	var generation int64
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT generation FROM memory_spaces WHERE id = ?::uuid`, spaceID).Row().Scan(&generation)
	}))
	return generation
}

// Snapshot the real visible row before exercising the authoritative commit fence.
func loadKnownEvidenceSnapshotForTest(ctx context.Context, store *Store, input knowledgecontract.SubmissionAssessmentKnownEvidenceInput) (knowledgecontract.SubmissionAssessmentKnownEvidenceResult, error) {
	result := knowledgecontract.SubmissionAssessmentKnownEvidenceResult{}
	err := store.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		for _, id := range input.EvidenceIDs {
			var owner string
			if err := tx.Raw(`SELECT owner_profile_id::text FROM evidence_fragments WHERE team_id=?::uuid AND fragment_id=?::uuid AND (?='' OR space_id=?::uuid)`, input.TeamID, id, input.SpaceID, input.SpaceID).Row().Scan(&owner); err != nil {
				return err
			}
			row, err := loadSubmissionKnownEvidenceFenceRow(ctx, tx, SubmissionAssessmentKnownEvidence{TeamID: input.TeamID, FragmentID: id, OwnerProfileID: owner})
			if err != nil {
				return err
			}
			result.Evidence = append(result.Evidence, SubmissionAssessmentKnownEvidence(row))
		}
		return nil
	})
	return result, err
}

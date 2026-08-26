package repository

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLedgerCreateIngestConcurrentIdempotencyConflictRollsBackLoser(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-concurrent-conflict")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner-concurrent-conflict")
	repo := NewLedgerRepository(appDB, rls)

	inputs := []CreateIngestInput{
		{
			TeamID:         teamID,
			OwnerProfileID: ownerID,
			IdempotencyKey: "concurrent-conflict",
			RequestHash:    "hash-a",
			Evidence:       []EvidenceInput{{Content: "Concurrent winner A"}},
		},
		{
			TeamID:         teamID,
			OwnerProfileID: ownerID,
			IdempotencyKey: "concurrent-conflict",
			RequestHash:    "hash-b",
			Evidence:       []EvidenceInput{{Content: "Concurrent winner B"}},
		},
	}
	outcomes := make(chan createIngestOutcome, len(inputs))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, input := range inputs {
		input := input
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := createTestIngest(ctx, repo, input)
			outcomes <- createIngestOutcome{result: result, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(outcomes)

	successes := 0
	conflicts := 0
	for outcome := range outcomes {
		if outcome.err != nil {
			if errors.Is(outcome.err, ErrIdempotencyConflict) {
				conflicts++
				continue
			}
			require.NoError(t, outcome.err)
		}
		require.NotNil(t, outcome.result)
		successes++
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)

	err := rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		var ingestCount int64
		if err := tx.Raw(`
			SELECT count(*)
			FROM knowledge_ingests
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND idempotency_key = 'concurrent-conflict'
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

		var content string
		if err := tx.Raw(`
			SELECT content
			FROM evidence_fragments
			WHERE team_id = ?::uuid
			LIMIT 1
		`, teamID).Scan(&content).Error; err != nil {
			return err
		}
		assert.Contains(t, []string{"Concurrent winner A", "Concurrent winner B"}, content)
		return nil
	})
	require.NoError(t, err)
}

func TestLedgerCreateIngestConcurrentSourceCASConflictRollsBackLoser(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-concurrent-source")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner-concurrent-source")
	repo := NewLedgerRepository(appDB, rls)

	inputs := []CreateIngestInput{
		{
			TeamID:         teamID,
			OwnerProfileID: ownerID,
			IdempotencyKey: "source-race-a",
			RequestHash:    "source-race-hash-a",
			Evidence: []EvidenceInput{{
				Content:                   "Source race winner A",
				SourceKey:                 "doc://source-race",
				SourceRevisionToken:       "rev-a",
				SourceRevisionContentHash: "sha256:source-race-a",
			}},
		},
		{
			TeamID:         teamID,
			OwnerProfileID: ownerID,
			IdempotencyKey: "source-race-b",
			RequestHash:    "source-race-hash-b",
			Evidence: []EvidenceInput{{
				Content:                   "Source race winner B",
				SourceKey:                 "doc://source-race",
				SourceRevisionToken:       "rev-b",
				SourceRevisionContentHash: "sha256:source-race-b",
			}},
		},
	}
	outcomes := make(chan createIngestOutcome, len(inputs))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, input := range inputs {
		input := input
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := createTestIngest(ctx, repo, input)
			outcomes <- createIngestOutcome{result: result, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(outcomes)

	successes := 0
	conflicts := 0
	for outcome := range outcomes {
		if outcome.err != nil {
			if errors.Is(outcome.err, ErrSourceRevisionConflict) {
				conflicts++
				continue
			}
			require.NoError(t, outcome.err)
		}
		require.NotNil(t, outcome.result)
		successes++
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)

	err := rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		var sourceCount int64
		if err := tx.Raw(`
			SELECT count(*)
			FROM evidence_sources
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND source_key = 'doc://source-race'
		`, teamID, ownerID).Scan(&sourceCount).Error; err != nil {
			return err
		}
		assert.Equal(t, int64(1), sourceCount)

		var revisionCount int64
		if err := tx.Raw(`
			SELECT count(*)
			FROM evidence_source_revisions r
			JOIN evidence_sources s
			  ON s.team_id = r.team_id
			 AND s.source_id = r.source_id
			WHERE r.team_id = ?::uuid
			  AND s.source_key = 'doc://source-race'
		`, teamID).Scan(&revisionCount).Error; err != nil {
			return err
		}
		assert.Equal(t, int64(1), revisionCount)

		var ingestCount int64
		if err := tx.Raw(`
			SELECT count(*)
			FROM knowledge_ingests
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND idempotency_key LIKE 'source-race-%'
		`, teamID, ownerID).Scan(&ingestCount).Error; err != nil {
			return err
		}
		assert.Equal(t, int64(1), ingestCount)

		var fragmentCount int64
		if err := tx.Raw(`
			SELECT count(*)
			FROM evidence_fragments
			WHERE team_id = ?::uuid
			  AND source_id IS NOT NULL
			  AND source_revision_id IS NOT NULL
		`, teamID).Scan(&fragmentCount).Error; err != nil {
			return err
		}
		assert.Equal(t, int64(1), fragmentCount)
		return nil
	})
	require.NoError(t, err)
}

type createIngestOutcome struct {
	result *EvidenceIngestResult
	err    error
}

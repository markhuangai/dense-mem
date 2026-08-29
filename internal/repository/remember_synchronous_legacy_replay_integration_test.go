package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestSynchronousRememberExistingLegacyIngestReturnsIdempotencyConflict(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "sync-remember-existing-legacy-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "sync-remember-existing-legacy-owner")
	repo := NewLedgerRepository(appDB, rls)

	for _, test := range []struct {
		name     string
		terminal bool
	}{
		{name: "accepted", terminal: false},
		{name: "rejected", terminal: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			key := "sync-existing-legacy-" + test.name
			hash := key + "-hash"
			legacyContent := "legacy Remember ingest for " + test.name
			legacy, err := repo.CreateIngest(ctx, CreateIngestInput{
				TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: key, RequestHash: hash,
				Status: string(domain.PlacementRunQueued), TelemetryRemember: true,
				Evidence: []EvidenceInput{{Content: legacyContent, ContentHash: sha256Hex(legacyContent)}},
			})
			require.NoError(t, err)
			require.False(t, legacy.Existing)

			if test.terminal {
				input := synchronousRememberAcceptedFixture(teamID, ownerID, key, hash, nil)
				input.Attempt.Outcome = "rejected"
				input.BuildCommit = nil
				_, err = repo.CommitSynchronousRememberTerminal(ctx, SynchronousRememberTerminalInput{
					CreateIngest: input.CreateIngest,
					Attempt:      input.Attempt,
					BuildTerminal: func(_ *CreateIngestResult, scope SubmissionAssessmentRunScope) (*PersistSubmissionAssessmentInput, CompleteSubmissionAssessmentInput, error) {
						return nil, CompleteSubmissionAssessmentInput{
							SubmissionAssessmentRunScope: scope,
							OutcomeKind:                  "submission_assessment_rejected",
							Status:                       string(domain.SemanticReviewRejected),
							Category:                     "rejected",
						}, nil
					},
				})
			} else {
				_, err = repo.CommitSynchronousRemember(ctx, synchronousRememberAcceptedFixture(teamID, ownerID, key, hash, nil))
			}
			require.ErrorIs(t, err, ErrIdempotencyConflict)

			_, err = repo.LoadRememberAttempt(ctx, RememberAttemptLookupInput{TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: key})
			require.ErrorIs(t, err, ErrRememberAttemptNotFound)
			var ingestCount int64
			require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
				return tx.Raw(`SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND idempotency_key = ?`, teamID, ownerID, key).Scan(&ingestCount).Error
			}))
			assert.Equal(t, int64(1), ingestCount)
		})
	}
}
